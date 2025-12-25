# Developing Beacon

## Prerequisites

- Go 1.23+
- Docker (for PostgreSQL and tests)
- Make

## Getting Started

```bash
# Clone and enter
git clone https://github.com/yourorg/beacon.git
cd beacon

# Start dependencies
docker-compose up -d

# Run migrations
go run ./cmd/beacon migrate

# Start the server
go run ./cmd/beacon serve
```

## Project Structure

```
beacon/
├── cmd/beacon/          # Entry point (serve, migrate commands)
├── internal/
│   ├── capture/         # Trigger installation and DDL utilities
│   ├── config/          # YAML config parsing and validation
│   ├── controlplane/    # HTTP API for apply/export/health
│   │   ├── api/         # HTTP handlers and middleware
│   │   └── service/     # Business logic (diff, apply, drain)
│   ├── db/              # Connection pool and migrations
│   │   └── migrations/  # SQL migration files
│   ├── dispatcher/      # Worker pool and event processing
│   │   └── retry/       # Backoff and retry logic
│   ├── httpdeliver/     # HTTP client with SSRF protection
│   ├── observability/   # Metrics and logging
│   ├── outbox/          # Event repository (claim, ack, reschedule)
│   └── testutil/        # Test helpers (postgres container)
├── scripts/             # Development utilities
└── testdata/            # Sample configs for testing
```

## Key Abstractions

### Outbox Pattern

Events flow through states: `pending` → `delivering` → `delivered` (or `dead`).

```go
// Claim events for processing
events, _ := repo.Claim(ctx, workerID, batchSize)

// On success
repo.Ack(ctx, eventID)

// On failure (will retry)
repo.Reschedule(ctx, eventID, nextVisibleAt, errMsg)

// Exhausted retries
repo.ToDead(ctx, eventID, reason)
```

### Dispatcher

The dispatcher runs a worker pool that polls the outbox:

```go
dispatcher := dispatcher.New(pool, httpClient, metrics, logger, cfg)
dispatcher.Start(ctx)  // Blocks until context cancelled
```

Workers use `FOR UPDATE SKIP LOCKED` for distributed-safe claiming.

### Capture Triggers

Triggers are installed per-table via the `capture.Installer`:

```go
installer := capture.New(pool)
installer.InstallTrigger(ctx, "public", "orders")   // Idempotent
installer.UninstallTrigger(ctx, "public", "orders")
```

The trigger function `beacon.capture_changes()` handles INSERT/UPDATE/DELETE and writes to the outbox within the same transaction.

### Config Diff/Apply

Configuration changes use a diff algorithm:

```go
service := service.NewApplyService(pool, installer)
plan, _ := service.DryRun(ctx, newConfig)  // Preview changes
service.Apply(ctx, newConfig)               // Execute changes
```

Changes are categorized as: destinations to create/update/delete, subscriptions to create/update/delete, triggers to install/uninstall.

## Commands

```bash
# Run all tests
make test

# Run tests with verbose output
go test ./... -v

# Run specific package tests
go test ./internal/outbox/... -v

# Run with race detector
go test ./... -race

# Build binary
make build

# Run linter
make lint

# Format code
make fmt
```

## Testing

### Unit Tests

Pure logic tests without external dependencies:

```bash
go test ./internal/config/...
go test ./internal/dispatcher/retry/...
```

### Integration Tests

Use testcontainers for real PostgreSQL:

```bash
go test ./internal/db/...
go test ./internal/outbox/...
go test ./internal/capture/...
```

These spin up a PostgreSQL container, run migrations, and execute tests.

### E2E Tests

Full trigger-to-outbox flow:

```bash
go test ./internal/capture/... -run E2E
```

## Database Schema

Core tables in the `beacon` schema:

| Table | Purpose |
|-------|---------|
| `destinations` | Webhook endpoints |
| `subscriptions` | Table → destination mappings |
| `outbox_events` | Pending/in-flight events |
| `worker_heartbeats` | Active worker tracking |
| `delivery_attempts` | Audit log of attempts |
| `dead_letters` | Failed events for debugging |

## How Triggers Work

Beacon uses PostgreSQL `AFTER` triggers to capture row changes. Here's the complete flow:

### 1. Trigger Installation

When you apply a config with subscriptions, Beacon installs a trigger on each referenced table:

```sql
CREATE TRIGGER beacon_capture_public_orders
  AFTER INSERT OR UPDATE OR DELETE ON public.orders
  FOR EACH ROW EXECUTE FUNCTION beacon.capture_changes();
```

One trigger per table handles all operations (INSERT/UPDATE/DELETE). The trigger is idempotent—applying config twice won't create duplicates.

### 2. The `capture_changes()` Function

When a row changes, `beacon.capture_changes()` runs **inside the same transaction**:

```sql
-- Pseudocode of what the trigger does:
FOR each matching subscription (same table, operation, enabled, not draining):

    -- Check trigger_columns filter (UPDATE only)
    IF operation = UPDATE AND trigger_columns IS SET:
        skip if none of those columns changed

    -- Filter payload columns if configured
    old_data = filter_columns(OLD, payload_columns)
    new_data = filter_columns(NEW, payload_columns)

    -- Extract primary key
    pk = beacon.extract_pk(schema, table, NEW, OLD)

    -- Build envelope payload
    payload = {
        "version": 1,
        "trigger": {"schema": "...", "table": "...", "operation": "..."},
        "pk": {...},
        "old": old_data,  -- null for INSERT
        "new": new_data   -- null for DELETE
    }

    -- Insert into outbox (same transaction!)
    INSERT INTO beacon.outbox_events (
        subscription_id, table_schema, table_name, operation,
        pk, old_data, new_data, payload
    ) VALUES (...)
```

### 3. Primary Key Extraction

The `beacon.extract_pk()` function queries `information_schema` to find primary key columns:

```sql
SELECT beacon.extract_pk('public', 'orders', '{"id": 42, "status": "new"}', NULL);
-- Returns: {"id": 42}
```

For composite keys, all columns are included:

```sql
-- Table with (tenant_id, order_id) as PK
SELECT beacon.extract_pk('public', 'tenant_orders', '{"tenant_id": 1, "order_id": 99, "status": "new"}', NULL);
-- Returns: {"tenant_id": 1, "order_id": 99}
```

### 4. Subscription Matching

The trigger only fires for subscriptions that match:

```sql
WHERE table_schema = TG_TABLE_SCHEMA
  AND table_name = TG_TABLE_NAME
  AND operation = TG_OP           -- 'INSERT', 'UPDATE', or 'DELETE'
  AND enabled = true
  AND deleted_at IS NULL
  AND NOT draining
```

Multiple subscriptions can match the same table/operation (e.g., send to different destinations).

### 5. Column Filtering

**`trigger_columns`** — Only fire on UPDATE if these columns changed:

```yaml
subscriptions:
  - name: status-changes
    table: public.orders
    events: [UPDATE]
    trigger_columns: [status, priority]  # Ignore other column updates
```

**`payload_columns`** — Only include these columns in the payload:

```yaml
subscriptions:
  - name: order-notifications
    table: public.orders
    events: [INSERT, UPDATE]
    payload_columns: [id, status, customer_email]  # Exclude sensitive data
```

### 6. Transactional Guarantee

The key insight: the outbox INSERT happens in the same transaction as your data change.

```sql
BEGIN;
  INSERT INTO orders (customer_id, total) VALUES (1, 99.99);
  -- Trigger fires HERE, inserts into beacon.outbox_events
COMMIT;  -- Both succeed or both fail
```

If the transaction rolls back, no event is created. If it commits, the event is guaranteed to exist.

### Example: Full Flow

```sql
-- 1. Your app inserts an order
INSERT INTO public.orders (id, customer_id, total, status)
VALUES (42, 1, 99.99, 'pending');

-- 2. Trigger fires, finds matching subscription "new-orders"

-- 3. Trigger inserts into outbox (same transaction)
INSERT INTO beacon.outbox_events (
    subscription_id,
    table_schema, table_name, operation,
    pk, new_data, payload, state
) VALUES (
    'uuid-of-new-orders-subscription',
    'public', 'orders', 'INSERT',
    '{"id": 42}',
    '{"id": 42, "customer_id": 1, "total": 99.99, "status": "pending"}',
    '{"version": 1, "trigger": {...}, "pk": {"id": 42}, "old": null, "new": {...}}',
    'pending'
);

-- 4. Transaction commits - both order AND event are durable

-- 5. Dispatcher polls, claims event, delivers webhook
```

## Adding a Migration

1. Create `internal/db/migrations/NNN_description.sql`
2. Migrations run in order by filename
3. Each migration runs in a transaction
4. Version tracked in `beacon.schema_version`

## Metrics

Prometheus metrics exposed at `/metrics` (authenticated):

```bash
curl -sS \
  -H "Authorization: Bearer $BEACON_CONTROLPLANE_SECRET" \
  http://localhost:8080/metrics | head
```

| Metric | Type | Description |
|--------|------|-------------|
| `beacon_delivery_total` | Counter | Deliveries by destination and status |
| `beacon_delivery_duration_seconds` | Histogram | Delivery latency |
| `beacon_dead_letters_total` | Counter | Dead-lettered events |
| `beacon_outbox_depth` | Gauge | Events by state |
| `beacon_workers_active` | Gauge | Currently processing workers |

## Debugging

### View pending events

```sql
SELECT * FROM beacon.outbox_events
WHERE state = 'pending'
ORDER BY visible_at
LIMIT 10;
```

### Check stale locks

```sql
SELECT * FROM beacon.outbox_events
WHERE state = 'delivering'
  AND locked_at < now() - interval '5 minutes';
```

### View dead letters

```sql
SELECT event_id, reason, dead_at, snapshot
FROM beacon.dead_letters
ORDER BY dead_at DESC
LIMIT 10;
```

### Check worker heartbeats

```sql
SELECT * FROM beacon.worker_heartbeats
ORDER BY last_heartbeat DESC;
```

## Architecture Decisions

### Why PostgreSQL triggers?

- Transactional consistency: events captured in same transaction as data
- No application code changes required
- Works with any language/framework that uses PostgreSQL

### Why polling instead of LISTEN/NOTIFY?

- `FOR UPDATE SKIP LOCKED` provides distributed-safe claiming
- Simpler recovery from worker crashes
- Backpressure via batch size and poll interval

### Why no external message queue?

- Fewer moving parts to operate
- PostgreSQL is already battle-tested for durability
- Outbox pattern provides exactly-once semantics with idempotent consumers

## Common Issues

### Events stuck in "delivering" state

Workers may have crashed. The reaper goroutine reclaims stale locks:

```go
// Events locked > 5 minutes are reclaimed
reaperInterval: 30 * time.Second
staleLockThreshold: 5 * time.Minute
```

### SSRF blocking localhost in tests

Use `SSRFPolicy: {"allow_private": true}` in test destinations.

### Trigger not firing

Check subscription is enabled and not draining:

```sql
SELECT name, enabled, draining, deleted_at
FROM beacon.subscriptions
WHERE table_name = 'your_table';
```
