-- Beacon Core Schema Migration
-- Creates all tables, indexes, and functions for the webhook delivery system.

-- =============================================================================
-- TABLES
-- =============================================================================

-- Destinations: webhook endpoints that receive events
CREATE TABLE beacon.destinations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT UNIQUE NOT NULL,
    url TEXT NOT NULL,
    method TEXT NOT NULL DEFAULT 'POST',
    headers JSONB NOT NULL DEFAULT '{}',
    timeout_ms INT NOT NULL DEFAULT 5000,
    max_in_flight INT NOT NULL DEFAULT 50,
    ssrf_policy JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Subscriptions: links tables/operations to destinations
CREATE TABLE beacon.subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT UNIQUE NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    deleted_at TIMESTAMPTZ,
    draining BOOLEAN NOT NULL DEFAULT false,
    table_schema TEXT NOT NULL,
    table_name TEXT NOT NULL,
    operation TEXT NOT NULL CHECK (operation IN ('INSERT', 'UPDATE', 'DELETE')),
    destination_id UUID NOT NULL REFERENCES beacon.destinations(id),
    filter JSONB,
    trigger_columns TEXT[],
    payload_columns TEXT[],
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Unique constraint: one subscription per table/operation/destination (for non-deleted)
CREATE UNIQUE INDEX idx_subscriptions_unique
    ON beacon.subscriptions (table_schema, table_name, operation, destination_id)
    WHERE deleted_at IS NULL;

-- Outbox events: transactional outbox for pending deliveries
CREATE TABLE beacon.outbox_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscription_id UUID NOT NULL REFERENCES beacon.subscriptions(id),
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    table_schema TEXT NOT NULL,
    table_name TEXT NOT NULL,
    operation TEXT NOT NULL,
    pk JSONB NOT NULL,
    old_data JSONB,
    new_data JSONB,
    payload JSONB NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'delivering', 'delivered', 'dead')),
    visible_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_by TEXT,
    locked_at TIMESTAMPTZ,
    attempts INT NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Worker heartbeats: tracks active workers for crash recovery
CREATE TABLE beacon.worker_heartbeats (
    worker_id TEXT PRIMARY KEY,
    last_heartbeat TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Delivery attempts: audit log of all delivery attempts
CREATE TABLE beacon.delivery_attempts (
    id BIGSERIAL PRIMARY KEY,
    event_id UUID NOT NULL REFERENCES beacon.outbox_events(id),
    destination_id UUID NOT NULL REFERENCES beacon.destinations(id),
    attempt INT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ NOT NULL,
    status_code INT,
    error TEXT,
    response_headers JSONB
);

-- Dead letters: events that exhausted all retries
CREATE TABLE beacon.dead_letters (
    event_id UUID PRIMARY KEY,
    dead_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    reason TEXT NOT NULL,
    snapshot JSONB NOT NULL,
    replay_count INT NOT NULL DEFAULT 0
);

-- =============================================================================
-- INDEXES
-- =============================================================================

-- Fast polling query: find pending events ready for delivery
CREATE INDEX idx_outbox_poll
    ON beacon.outbox_events (state, visible_at)
    WHERE state = 'pending';

-- Subscription-based queries
CREATE INDEX idx_outbox_subscription
    ON beacon.outbox_events (subscription_id, created_at);

-- Reaper query: find stale locks from crashed workers
CREATE INDEX idx_outbox_delivering
    ON beacon.outbox_events (locked_at)
    WHERE state = 'delivering';

-- Delivery attempts by event
CREATE INDEX idx_delivery_attempts_event
    ON beacon.delivery_attempts (event_id);

-- =============================================================================
-- FUNCTIONS
-- =============================================================================

-- extract_pk: Extracts primary key columns from a table row
CREATE OR REPLACE FUNCTION beacon.extract_pk(
    p_schema TEXT,
    p_table TEXT,
    p_new JSONB,
    p_old JSONB
) RETURNS JSONB AS $$
DECLARE
    pk_cols TEXT[];
    pk_result JSONB := '{}';
    col TEXT;
    row_data JSONB;
BEGIN
    -- Get primary key columns from information_schema
    SELECT array_agg(kcu.column_name ORDER BY kcu.ordinal_position)
    INTO pk_cols
    FROM information_schema.table_constraints tc
    JOIN information_schema.key_column_usage kcu
        ON tc.constraint_name = kcu.constraint_name
        AND tc.table_schema = kcu.table_schema
    WHERE tc.constraint_type = 'PRIMARY KEY'
        AND tc.table_schema = p_schema
        AND tc.table_name = p_table;

    IF pk_cols IS NULL THEN
        RAISE EXCEPTION 'Table %.% has no primary key', p_schema, p_table;
    END IF;

    -- Use new data if available, otherwise old data
    row_data := COALESCE(p_new, p_old);

    -- Build pk result
    FOREACH col IN ARRAY pk_cols LOOP
        pk_result := pk_result || jsonb_build_object(col, row_data -> col);
    END LOOP;

    RETURN pk_result;
END;
$$ LANGUAGE plpgsql STABLE;

-- capture_changes: Trigger function that captures row changes
CREATE OR REPLACE FUNCTION beacon.capture_changes()
RETURNS TRIGGER AS $$
DECLARE
    sub RECORD;
    should_fire BOOLEAN;
    old_filtered JSONB;
    new_filtered JSONB;
    event_payload JSONB;
    pk_value JSONB;
    max_payload_bytes INT;
BEGIN
    -- Get max payload size from setting (default 1MB)
    BEGIN
        max_payload_bytes := current_setting('beacon.max_payload_bytes')::int;
    EXCEPTION WHEN OTHERS THEN
        max_payload_bytes := 1048576;
    END;

    -- Early size check (approximate) to fail fast on obviously oversized rows
    IF TG_OP != 'DELETE' AND octet_length(to_jsonb(NEW)::text) > max_payload_bytes THEN
        RAISE EXCEPTION 'row size likely exceeds maximum payload of % bytes', max_payload_bytes;
    END IF;

    -- Loop through matching subscriptions
    FOR sub IN
        SELECT id, destination_id, trigger_columns, payload_columns
        FROM beacon.subscriptions
        WHERE table_schema = TG_TABLE_SCHEMA
          AND table_name = TG_TABLE_NAME
          AND operation = TG_OP
          AND enabled = true
          AND deleted_at IS NULL
          AND NOT draining
    LOOP
        -- Check trigger_columns filter (UPDATE only)
        should_fire := true;
        IF TG_OP = 'UPDATE' AND sub.trigger_columns IS NOT NULL THEN
            should_fire := EXISTS (
                SELECT 1 FROM unnest(sub.trigger_columns) AS col
                WHERE (to_jsonb(OLD) ->> col) IS DISTINCT FROM (to_jsonb(NEW) ->> col)
            );
        END IF;

        IF NOT should_fire THEN
            CONTINUE;
        END IF;

        -- Build filtered old/new data
        IF sub.payload_columns IS NOT NULL THEN
            old_filtered := CASE WHEN TG_OP IN ('UPDATE', 'DELETE') THEN
                (SELECT jsonb_object_agg(key, value)
                 FROM jsonb_each(to_jsonb(OLD))
                 WHERE key = ANY(sub.payload_columns))
            END;
            new_filtered := CASE WHEN TG_OP IN ('INSERT', 'UPDATE') THEN
                (SELECT jsonb_object_agg(key, value)
                 FROM jsonb_each(to_jsonb(NEW))
                 WHERE key = ANY(sub.payload_columns))
            END;
        ELSE
            old_filtered := CASE WHEN TG_OP IN ('UPDATE', 'DELETE') THEN to_jsonb(OLD) END;
            new_filtered := CASE WHEN TG_OP IN ('INSERT', 'UPDATE') THEN to_jsonb(NEW) END;
        END IF;

        -- Extract primary key
        pk_value := beacon.extract_pk(
            TG_TABLE_SCHEMA, TG_TABLE_NAME,
            CASE WHEN TG_OP != 'DELETE' THEN to_jsonb(NEW) END,
            CASE WHEN TG_OP != 'INSERT' THEN to_jsonb(OLD) END
        );

        -- Build envelope payload with version for future compatibility
        event_payload := jsonb_build_object(
            'version', 1,
            'trigger', jsonb_build_object(
                'schema', TG_TABLE_SCHEMA,
                'table', TG_TABLE_NAME,
                'operation', TG_OP
            ),
            'pk', pk_value,
            'old', old_filtered,
            'new', new_filtered
        );

        -- Final size check
        IF octet_length(event_payload::text) > max_payload_bytes THEN
            RAISE EXCEPTION 'payload exceeds maximum size of % bytes', max_payload_bytes;
        END IF;

        -- Insert into outbox
        INSERT INTO beacon.outbox_events (
            subscription_id, table_schema, table_name, operation,
            pk, old_data, new_data, payload
        ) VALUES (
            sub.id, TG_TABLE_SCHEMA, TG_TABLE_NAME, TG_OP,
            pk_value, old_filtered, new_filtered, event_payload
        );
    END LOOP;

    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

-- =============================================================================
-- UPDATED_AT TRIGGERS
-- =============================================================================

CREATE OR REPLACE FUNCTION beacon.update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER destinations_updated_at
    BEFORE UPDATE ON beacon.destinations
    FOR EACH ROW EXECUTE FUNCTION beacon.update_updated_at();

CREATE TRIGGER subscriptions_updated_at
    BEFORE UPDATE ON beacon.subscriptions
    FOR EACH ROW EXECUTE FUNCTION beacon.update_updated_at();
