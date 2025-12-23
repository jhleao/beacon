# 2. Capture Layer Specification

## Purpose

The capture layer installs PostgreSQL triggers on user tables to detect INSERT/UPDATE/DELETE operations and write events to the outbox. It provides a single, reusable trigger function shared by all tables.

---

## Exposed API

### Package: `internal/capture`

```go
// Installer manages trigger installation on user tables
type Installer struct {
    pool *db.Pool
}

// New creates an Installer
func New(pool *db.Pool) *Installer

// InstallTrigger creates the beacon trigger on a table (idempotent)
func (i *Installer) InstallTrigger(ctx context.Context, schema, table string) error

// UninstallTrigger removes the beacon trigger from a table
func (i *Installer) UninstallTrigger(ctx context.Context, schema, table string) error

// ListTriggers returns all tables with beacon triggers installed
func (i *Installer) ListTriggers(ctx context.Context) ([]TableRef, error)

// EnsureFunctions creates/updates the beacon SQL functions
func (i *Installer) EnsureFunctions(ctx context.Context) error
```

```go
// TableRef identifies a table
type TableRef struct {
    Schema string
    Name   string
}
```

---

## Internal Implementation

### Trigger Naming Convention

Triggers are named deterministically for idempotency:

```
beacon_capture_<schema>_<table>
```

Example: `beacon_capture_public_users`

### SQL Identifier Safety

All identifiers are quoted using PostgreSQL's `format()` with `%I` specifier:

```go
// ddl.go
func QuoteIdent(s string) string {
    // Use pgx's identifier quoting
    return pgx.Identifier{s}.Sanitize()
}

func TriggerName(schema, table string) string {
    return fmt.Sprintf("beacon_capture_%s_%s", schema, table)
}
```

### InstallTrigger Implementation

```go
func (i *Installer) InstallTrigger(ctx context.Context, schema, table string) error {
    triggerName := TriggerName(schema, table)

    query := fmt.Sprintf(`
        CREATE TRIGGER %s
        AFTER INSERT OR UPDATE OR DELETE ON %s.%s
        FOR EACH ROW
        EXECUTE FUNCTION beacon.capture_changes()
    `,
        QuoteIdent(triggerName),
        QuoteIdent(schema),
        QuoteIdent(table),
    )

    // Use IF NOT EXISTS pattern via DO block
    _, err := i.pool.Exec(ctx, fmt.Sprintf(`
        DO $$
        BEGIN
            IF NOT EXISTS (
                SELECT 1 FROM pg_trigger
                WHERE tgname = %s
            ) THEN
                EXECUTE %s;
            END IF;
        END $$
    `, QuoteLiteral(triggerName), QuoteLiteral(query)))

    return err
}
```

### UninstallTrigger Implementation

```go
func (i *Installer) UninstallTrigger(ctx context.Context, schema, table string) error {
    triggerName := TriggerName(schema, table)

    _, err := i.pool.Exec(ctx, fmt.Sprintf(`
        DROP TRIGGER IF EXISTS %s ON %s.%s
    `,
        QuoteIdent(triggerName),
        QuoteIdent(schema),
        QuoteIdent(table),
    ))

    return err
}
```

---

## SQL Functions

### `beacon.capture_changes()` Trigger Function

This is the core trigger function installed by migrations. It is designed for **row-level execution only** (`FOR EACH ROW`). Statement-level triggers are not supported.

It:

1. Queries `beacon.subscriptions` for matching active subscriptions
2. Applies column filtering for UPDATE operations
3. Builds the payload with selected columns
4. Batch inserts into `beacon.outbox_events`

```sql
CREATE OR REPLACE FUNCTION beacon.capture_changes()
RETURNS TRIGGER AS $$
DECLARE
    sub RECORD;
    should_fire BOOLEAN;
    old_filtered JSONB;
    new_filtered JSONB;
    event_payload JSONB;
    pk_value JSONB;
BEGIN
    -- Early size check (approximate) to fail fast on obviously oversized rows
    IF TG_OP != 'DELETE' AND octet_length(to_jsonb(NEW)::text) > current_setting('beacon.max_payload_bytes')::int THEN
        RAISE EXCEPTION 'row size likely exceeds maximum payload of % bytes',
            current_setting('beacon.max_payload_bytes');
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
```

### Trigger Variables Used

| Variable | Type | Description |
|----------|------|-------------|
| `TG_TABLE_SCHEMA` | `text` | Schema of the table that fired the trigger |
| `TG_TABLE_NAME` | `text` | Name of the table that fired the trigger |
| `TG_OP` | `text` | Operation: `'INSERT'`, `'UPDATE'`, or `'DELETE'` |
| `OLD` | `record` | Pre-change row (UPDATE/DELETE only) |
| `NEW` | `record` | Post-change row (INSERT/UPDATE only) |

---

## Payload Format

Events are delivered with this JSON structure:

```json
{
  "version": 1,
  "trigger": {
    "schema": "public",
    "table": "users",
    "operation": "UPDATE"
  },
  "pk": {
    "id": "550e8400-e29b-41d4-a716-446655440000"
  },
  "old": {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "email": "old@example.com",
    "name": "Old Name"
  },
  "new": {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "email": "new@example.com",
    "name": "New Name"
  }
}
```

**Notes:**
- `version` is always `1` for this payload schema; future changes will increment
- `old` is `null` for INSERT
- `new` is `null` for DELETE
- When `payload_columns` is set, only those columns appear in `old`/`new`

### Payload Versioning

All payloads include a `version` field at the root. Current version is `1`. Future payload format changes will increment this version, allowing receivers to handle multiple formats during migrations.

---

## Column Filtering

### `trigger_columns` (UPDATE only)

Only fire the subscription if at least one of these columns changed:

```yaml
subscriptions:
  - name: users-email-change
    table: public.users
    operation: UPDATE
    trigger_on: [email]  # Only fires when email changes
```

Implementation uses `IS DISTINCT FROM` to handle NULL correctly.

### `payload_columns`

Only include these columns in the payload (for all operations):

```yaml
subscriptions:
  - name: orders-audit
    table: public.orders
    operation: INSERT
    select: [id, user_id, total, created_at]  # Excludes payment_info
```

---

## Performance Note

The `capture_changes()` trigger executes a query against `beacon.subscriptions` for every row modification, even if no subscriptions exist for that table. For high-write tables without Beacon subscriptions, consider not installing the trigger. The control plane automatically manages trigger installation based on active subscriptions—triggers are only installed on tables that have at least one active subscription.

---

## Payload Size Limits

The trigger enforces a maximum payload size to prevent memory exhaustion. Size is checked both before (approximate, on raw row) and after (exact, on final payload) to fail fast on obviously oversized rows:

```sql
-- In beacon.capture_changes()
IF octet_length(event_payload::text) > current_setting('beacon.max_payload_bytes')::int THEN
    RAISE EXCEPTION 'payload exceeds maximum size of % bytes',
        current_setting('beacon.max_payload_bytes');
END IF;
```

**Configuration:** Set via `BEACON_MAX_PAYLOAD_BYTES` environment variable (default: 1MB).

The limit is applied to the final JSON payload, not individual columns. If a payload exceeds the limit, the trigger raises an exception and the user transaction is rolled back.

---

## Error Handling

The trigger function is designed to be resilient:

1. **No matching subscriptions:** Loop simply doesn't execute; no error
2. **Subscription query fails:** Propagates error, blocking the user transaction (fail-safe)
3. **Outbox insert fails:** Propagates error, blocking the user transaction (fail-safe)
4. **Payload too large:** Raises exception, blocking the user transaction

**Design choice:** Blocking user transactions on outbox failure ensures no events are lost. If the outbox is unavailable, the entire system halts rather than silently dropping events.

---

## Dependencies

- `internal/db` - Database connection pool
- PostgreSQL 12+ (for `gen_random_uuid()`, `jsonb_build_object()`)

---

## Testing

### Strategy

Test against **real PostgreSQL via testcontainers** since triggers are database objects. No mocking—verify actual trigger behavior by inserting/updating/deleting rows and checking the outbox.

### Test Helpers

```go
// internal/capture/testhelpers_test.go

package capture_test

// CreateTestTable creates a test table for trigger testing
func CreateTestTable(t *testing.T, pool *db.Pool, schema, table string) {
    t.Helper()
    ctx := context.Background()

    _, err := pool.Exec(ctx, fmt.Sprintf(`
        CREATE TABLE IF NOT EXISTS %s.%s (
            id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
            name TEXT NOT NULL,
            email TEXT,
            status TEXT DEFAULT 'active',
            created_at TIMESTAMPTZ DEFAULT now()
        )
    `, schema, table))
    if err != nil {
        t.Fatalf("failed to create test table: %v", err)
    }
}

// CreateTestSubscription inserts a subscription for testing
func CreateTestSubscription(t *testing.T, pool *db.Pool, opts SubscriptionOpts) uuid.UUID {
    t.Helper()
    ctx := context.Background()

    // First ensure destination exists
    var destID uuid.UUID
    err := pool.QueryRow(ctx, `
        INSERT INTO beacon.destinations (name, url)
        VALUES ($1, $2)
        ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
        RETURNING id
    `, opts.DestName, "https://example.com/webhook").Scan(&destID)
    if err != nil {
        t.Fatalf("failed to create destination: %v", err)
    }

    var subID uuid.UUID
    err = pool.QueryRow(ctx, `
        INSERT INTO beacon.subscriptions
            (name, table_schema, table_name, operation, destination_id, trigger_columns, payload_columns)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
        RETURNING id
    `, opts.Name, opts.Schema, opts.Table, opts.Operation, destID, opts.TriggerColumns, opts.PayloadColumns).Scan(&subID)
    if err != nil {
        t.Fatalf("failed to create subscription: %v", err)
    }

    return subID
}

type SubscriptionOpts struct {
    Name           string
    DestName       string
    Schema         string
    Table          string
    Operation      string
    TriggerColumns []string
    PayloadColumns []string
}
```

### Test Cases

```go
// internal/capture/installer_test.go

func TestInstaller_InstallTrigger(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    installer := capture.New(pool)

    CreateTestTable(t, pool, "public", "test_users")

    // Install trigger
    err := installer.InstallTrigger(ctx, "public", "test_users")
    assert.NoError(t, err)

    // Verify trigger exists
    var exists bool
    pool.QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1 FROM pg_trigger
            WHERE tgname = 'beacon_capture_public_test_users'
        )
    `).Scan(&exists)
    assert.True(t, exists)
}

func TestInstaller_InstallTrigger_Idempotent(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    installer := capture.New(pool)

    CreateTestTable(t, pool, "public", "test_users")

    // Install twice - should not error
    err := installer.InstallTrigger(ctx, "public", "test_users")
    assert.NoError(t, err)

    err = installer.InstallTrigger(ctx, "public", "test_users")
    assert.NoError(t, err)
}

func TestInstaller_UninstallTrigger(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    installer := capture.New(pool)

    CreateTestTable(t, pool, "public", "test_users")
    installer.InstallTrigger(ctx, "public", "test_users")

    // Uninstall
    err := installer.UninstallTrigger(ctx, "public", "test_users")
    assert.NoError(t, err)

    // Verify gone
    var exists bool
    pool.QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1 FROM pg_trigger
            WHERE tgname = 'beacon_capture_public_test_users'
        )
    `).Scan(&exists)
    assert.False(t, exists)
}
```

### Capture Function Tests

```go
// internal/capture/capture_test.go

func TestCapture_Insert(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    installer := capture.New(pool)

    CreateTestTable(t, pool, "public", "test_users")
    subID := CreateTestSubscription(t, pool, SubscriptionOpts{
        Name:      "test-insert",
        DestName:  "test-dest",
        Schema:    "public",
        Table:     "test_users",
        Operation: "INSERT",
    })
    installer.InstallTrigger(ctx, "public", "test_users")

    // Insert a row
    var userID uuid.UUID
    pool.QueryRow(ctx, `
        INSERT INTO public.test_users (name, email)
        VALUES ('Alice', 'alice@example.com')
        RETURNING id
    `).Scan(&userID)

    // Verify outbox event created
    var event struct {
        Operation string
        Payload   json.RawMessage
    }
    err := pool.QueryRow(ctx, `
        SELECT operation, payload FROM beacon.outbox_events
        WHERE subscription_id = $1
    `, subID).Scan(&event.Operation, &event.Payload)

    assert.NoError(t, err)
    assert.Equal(t, "INSERT", event.Operation)

    // Verify payload structure
    var payload map[string]any
    json.Unmarshal(event.Payload, &payload)
    assert.Equal(t, float64(1), payload["version"])
    assert.NotNil(t, payload["new"])
    assert.Nil(t, payload["old"])
}

func TestCapture_Update(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    installer := capture.New(pool)

    CreateTestTable(t, pool, "public", "test_users")
    subID := CreateTestSubscription(t, pool, SubscriptionOpts{
        Name:      "test-update",
        DestName:  "test-dest",
        Schema:    "public",
        Table:     "test_users",
        Operation: "UPDATE",
    })
    installer.InstallTrigger(ctx, "public", "test_users")

    // Insert then update
    var userID uuid.UUID
    pool.QueryRow(ctx, `
        INSERT INTO public.test_users (name, email)
        VALUES ('Alice', 'alice@example.com')
        RETURNING id
    `).Scan(&userID)

    pool.Exec(ctx, `
        UPDATE public.test_users SET name = 'Alice Updated' WHERE id = $1
    `, userID)

    // Verify outbox has UPDATE event
    var payload json.RawMessage
    err := pool.QueryRow(ctx, `
        SELECT payload FROM beacon.outbox_events
        WHERE subscription_id = $1 AND operation = 'UPDATE'
    `, subID).Scan(&payload)

    assert.NoError(t, err)

    var p map[string]any
    json.Unmarshal(payload, &p)
    assert.NotNil(t, p["old"])
    assert.NotNil(t, p["new"])
}

func TestCapture_Delete(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    installer := capture.New(pool)

    CreateTestTable(t, pool, "public", "test_users")
    subID := CreateTestSubscription(t, pool, SubscriptionOpts{
        Name:      "test-delete",
        DestName:  "test-dest",
        Schema:    "public",
        Table:     "test_users",
        Operation: "DELETE",
    })
    installer.InstallTrigger(ctx, "public", "test_users")

    // Insert then delete
    var userID uuid.UUID
    pool.QueryRow(ctx, `
        INSERT INTO public.test_users (name, email)
        VALUES ('Alice', 'alice@example.com')
        RETURNING id
    `).Scan(&userID)

    pool.Exec(ctx, `DELETE FROM public.test_users WHERE id = $1`, userID)

    // Verify DELETE event
    var payload json.RawMessage
    pool.QueryRow(ctx, `
        SELECT payload FROM beacon.outbox_events
        WHERE subscription_id = $1
    `, subID).Scan(&payload)

    var p map[string]any
    json.Unmarshal(payload, &p)
    assert.NotNil(t, p["old"])
    assert.Nil(t, p["new"])
}

func TestCapture_TriggerColumns(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    installer := capture.New(pool)

    CreateTestTable(t, pool, "public", "test_users")
    subID := CreateTestSubscription(t, pool, SubscriptionOpts{
        Name:           "test-trigger-cols",
        DestName:       "test-dest",
        Schema:         "public",
        Table:          "test_users",
        Operation:      "UPDATE",
        TriggerColumns: []string{"email"},  // Only trigger on email changes
    })
    installer.InstallTrigger(ctx, "public", "test_users")

    var userID uuid.UUID
    pool.QueryRow(ctx, `
        INSERT INTO public.test_users (name, email)
        VALUES ('Alice', 'alice@example.com')
        RETURNING id
    `).Scan(&userID)

    // Update name only - should NOT trigger
    pool.Exec(ctx, `UPDATE public.test_users SET name = 'Bob' WHERE id = $1`, userID)

    var count int
    pool.QueryRow(ctx, `
        SELECT COUNT(*) FROM beacon.outbox_events WHERE subscription_id = $1
    `, subID).Scan(&count)
    assert.Equal(t, 0, count, "should not trigger on non-watched column")

    // Update email - should trigger
    pool.Exec(ctx, `UPDATE public.test_users SET email = 'bob@example.com' WHERE id = $1`, userID)

    pool.QueryRow(ctx, `
        SELECT COUNT(*) FROM beacon.outbox_events WHERE subscription_id = $1
    `, subID).Scan(&count)
    assert.Equal(t, 1, count, "should trigger on watched column")
}

func TestCapture_PayloadColumns(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    installer := capture.New(pool)

    CreateTestTable(t, pool, "public", "test_users")
    CreateTestSubscription(t, pool, SubscriptionOpts{
        Name:           "test-payload-cols",
        DestName:       "test-dest",
        Schema:         "public",
        Table:          "test_users",
        Operation:      "INSERT",
        PayloadColumns: []string{"id", "email"},  // Only include id and email
    })
    installer.InstallTrigger(ctx, "public", "test_users")

    pool.Exec(ctx, `
        INSERT INTO public.test_users (name, email)
        VALUES ('Alice', 'alice@example.com')
    `)

    var payload json.RawMessage
    pool.QueryRow(ctx, `
        SELECT payload FROM beacon.outbox_events LIMIT 1
    `).Scan(&payload)

    var p map[string]any
    json.Unmarshal(payload, &p)
    newData := p["new"].(map[string]any)

    assert.Contains(t, newData, "id")
    assert.Contains(t, newData, "email")
    assert.NotContains(t, newData, "name", "name should be excluded")
    assert.NotContains(t, newData, "status", "status should be excluded")
}

func TestCapture_DisabledSubscription(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    installer := capture.New(pool)

    CreateTestTable(t, pool, "public", "test_users")
    subID := CreateTestSubscription(t, pool, SubscriptionOpts{
        Name:      "test-disabled",
        DestName:  "test-dest",
        Schema:    "public",
        Table:     "test_users",
        Operation: "INSERT",
    })
    installer.InstallTrigger(ctx, "public", "test_users")

    // Disable subscription
    pool.Exec(ctx, `UPDATE beacon.subscriptions SET enabled = false WHERE id = $1`, subID)

    // Insert - should not capture
    pool.Exec(ctx, `INSERT INTO public.test_users (name) VALUES ('Alice')`)

    var count int
    pool.QueryRow(ctx, `SELECT COUNT(*) FROM beacon.outbox_events WHERE subscription_id = $1`, subID).Scan(&count)
    assert.Equal(t, 0, count)
}
```

### Running Tests

```bash
# Run capture tests
go test ./internal/capture/... -v

# Run specific test
go test ./internal/capture/... -run TestCapture_TriggerColumns -v
```

---

## Usage Example

```go
installer := capture.New(pool)

// Install trigger on a table
if err := installer.InstallTrigger(ctx, "public", "users"); err != nil {
    return err
}

// Remove trigger when no subscriptions remain
if err := installer.UninstallTrigger(ctx, "public", "users"); err != nil {
    return err
}
```

---

## Trigger Lifecycle

```
┌─────────────────────────────────────────────────────────────┐
│                    Subscription Created                      │
└────────────────────────────┬────────────────────────────────┘
                             │
                             ▼
                ┌────────────────────────┐
                │ Check if trigger exists │
                └────────────┬───────────┘
                             │
            ┌────────────────┴────────────────┐
            │                                 │
            ▼                                 ▼
   ┌─────────────────┐               ┌─────────────────┐
   │ Trigger exists  │               │ No trigger      │
   │ (no-op)         │               │ → Install       │
   └─────────────────┘               └─────────────────┘

┌─────────────────────────────────────────────────────────────┐
│                    Subscription Deleted                      │
└────────────────────────────┬────────────────────────────────┘
                             │
                             ▼
            ┌────────────────────────────────┐
            │ Other subscriptions on table?  │
            └────────────────┬───────────────┘
                             │
            ┌────────────────┴────────────────┐
            │                                 │
            ▼                                 ▼
   ┌─────────────────┐               ┌─────────────────┐
   │ Yes → keep      │               │ No → uninstall  │
   │ trigger         │               │ trigger         │
   └─────────────────┘               └─────────────────┘
```
