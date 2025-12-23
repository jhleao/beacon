# 1. Database Specification

## Purpose

Defines the PostgreSQL schema, migrations, and connection pool for Beacon. All Beacon tables live in the `beacon` schema to avoid polluting user schemas.

---

## Exposed API

### Package: `internal/db`

```go
// Pool wraps pgxpool.Pool with Beacon-specific helpers
type Pool struct {
    *pgxpool.Pool
}

// New creates a connection pool from DATABASE_URL
func New(ctx context.Context, databaseURL string) (*Pool, error)

// Close gracefully shuts down the pool
func (p *Pool) Close()

// WithTx executes fn in a transaction, rolling back on error
func (p *Pool) WithTx(ctx context.Context, fn func(pgx.Tx) error) error

// Migrate runs all pending migrations
func (p *Pool) Migrate(ctx context.Context) error
```

---

## Internal Implementation

### Connection Pool Configuration

```go
config, _ := pgxpool.ParseConfig(databaseURL)
config.MaxConns = 25
config.MinConns = 5
config.MaxConnLifetime = 1 * time.Hour
config.MaxConnIdleTime = 30 * time.Minute
config.HealthCheckPeriod = 1 * time.Minute
```

### Transaction Helper

```go
func (p *Pool) WithTx(ctx context.Context, fn func(pgx.Tx) error) error {
    tx, err := p.Begin(ctx)
    if err != nil {
        return err
    }
    defer tx.Rollback(ctx)

    if err := fn(tx); err != nil {
        return err
    }
    return tx.Commit(ctx)
}
```

---

## Schema

### Table: `beacon.destinations`

Webhook endpoints that receive events.

| Column | Type | Constraints | Description |
|--------|------|-------------|-------------|
| `id` | `UUID` | PK, default `gen_random_uuid()` | Unique identifier |
| `name` | `TEXT` | UNIQUE, NOT NULL | Human-readable name for YAML reference |
| `url` | `TEXT` | NOT NULL | Webhook URL |
| `method` | `TEXT` | NOT NULL, default `'POST'` | HTTP method |
| `headers` | `JSONB` | NOT NULL, default `'{}'` | Custom headers as `{"Header": "Value"}` |
| `timeout_ms` | `INT` | NOT NULL, default `5000` | Request timeout in milliseconds |
| `max_in_flight` | `INT` | NOT NULL, default `50` | Max concurrent deliveries |
| `ssrf_policy` | `JSONB` | NOT NULL, default `'{}'` | SSRF bypass rules |
| `created_at` | `TIMESTAMPTZ` | NOT NULL, default `now()` | Creation time |
| `updated_at` | `TIMESTAMPTZ` | NOT NULL, default `now()` | Last update time |

### Table: `beacon.subscriptions`

Links tables/operations to destinations.

| Column | Type | Constraints | Description |
|--------|------|-------------|-------------|
| `id` | `UUID` | PK, default `gen_random_uuid()` | Unique identifier |
| `name` | `TEXT` | UNIQUE, NOT NULL | Human-readable name for YAML reference |
| `enabled` | `BOOLEAN` | NOT NULL, default `true` | Whether subscription is active |
| `deleted_at` | `TIMESTAMPTZ` | nullable | Soft delete timestamp |
| `draining` | `BOOLEAN` | NOT NULL, default `false` | True when draining before delete |
| `table_schema` | `TEXT` | NOT NULL | Schema of watched table |
| `table_name` | `TEXT` | NOT NULL | Name of watched table |
| `operation` | `TEXT` | NOT NULL, CHECK | One of: `'INSERT'`, `'UPDATE'`, `'DELETE'` |
| `destination_id` | `UUID` | FK → destinations | Target destination |
| `filter` | `JSONB` | nullable | Reserved for future row-level filtering |
| `trigger_columns` | `TEXT[]` | nullable | UPDATE only fires if these change; NULL = all |
| `payload_columns` | `TEXT[]` | nullable | Columns to include in payload; NULL = all |
| `created_at` | `TIMESTAMPTZ` | NOT NULL, default `now()` | Creation time |
| `updated_at` | `TIMESTAMPTZ` | NOT NULL, default `now()` | Last update time |

**Unique Constraint:** `(table_schema, table_name, operation, destination_id) WHERE deleted_at IS NULL`

### Table: `beacon.outbox_events`

Transactional outbox for pending deliveries.

| Column | Type | Constraints | Description |
|--------|------|-------------|-------------|
| `id` | `UUID` | PK, default `gen_random_uuid()` | Event ID (sent in headers) |
| `subscription_id` | `UUID` | FK → subscriptions, NOT NULL | Originating subscription |
| `occurred_at` | `TIMESTAMPTZ` | NOT NULL, default `now()` | When the DB change happened |
| `table_schema` | `TEXT` | NOT NULL | Source table schema |
| `table_name` | `TEXT` | NOT NULL | Source table name |
| `operation` | `TEXT` | NOT NULL | `'INSERT'`, `'UPDATE'`, or `'DELETE'` |
| `pk` | `JSONB` | NOT NULL | Primary key values as `{"col": value}` |
| `old_data` | `JSONB` | nullable | Pre-change row (UPDATE/DELETE) |
| `new_data` | `JSONB` | nullable | Post-change row (INSERT/UPDATE) |
| `payload` | `JSONB` | NOT NULL | Actual webhook payload |
| `state` | `TEXT` | NOT NULL, default `'pending'` | One of: `pending`, `delivering`, `delivered`, `dead` |
| `visible_at` | `TIMESTAMPTZ` | NOT NULL, default `now()` | When event becomes claimable |
| `locked_by` | `TEXT` | nullable | Worker ID holding lock |
| `locked_at` | `TIMESTAMPTZ` | nullable | When lock was acquired |
| `attempts` | `INT` | NOT NULL, default `0` | Delivery attempt count |
| `last_error` | `TEXT` | nullable | Most recent error message |
| `created_at` | `TIMESTAMPTZ` | NOT NULL, default `now()` | Insert time |

### Table: `beacon.worker_heartbeats`

Tracks active workers for crash recovery.

| Column | Type | Constraints | Description |
|--------|------|-------------|-------------|
| `worker_id` | `TEXT` | PK | Unique worker identifier |
| `last_heartbeat` | `TIMESTAMPTZ` | NOT NULL, default `now()` | Last heartbeat time |
| `started_at` | `TIMESTAMPTZ` | NOT NULL, default `now()` | Worker start time |

### Table: `beacon.delivery_attempts`

Audit log of all delivery attempts.

| Column | Type | Constraints | Description |
|--------|------|-------------|-------------|
| `id` | `BIGSERIAL` | PK | Auto-increment ID |
| `event_id` | `UUID` | FK → outbox_events, NOT NULL | Event being delivered |
| `destination_id` | `UUID` | FK → destinations, NOT NULL | Target destination |
| `attempt` | `INT` | NOT NULL | Attempt number (1-indexed) |
| `started_at` | `TIMESTAMPTZ` | NOT NULL | Request start time |
| `finished_at` | `TIMESTAMPTZ` | NOT NULL | Request end time |
| `status_code` | `INT` | nullable | HTTP status code (null if connection failed) |
| `error` | `TEXT` | nullable | Error message if failed |
| `response_headers` | `JSONB` | nullable | Response headers for debugging |

### Table: `beacon.dead_letters`

Events that exhausted all retries.

| Column | Type | Constraints | Description |
|--------|------|-------------|-------------|
| `event_id` | `UUID` | PK | Dead event ID (no FK, snapshot is self-contained) |
| `dead_at` | `TIMESTAMPTZ` | NOT NULL, default `now()` | When moved to DLQ |
| `reason` | `TEXT` | NOT NULL | Why it died (last error) |
| `snapshot` | `JSONB` | NOT NULL | Full event data at time of death |
| `replay_count` | `INT` | NOT NULL, default `0` | Number of times replayed from DLQ |

> **Note:** Dead letters are intentionally not FK-constrained to `outbox_events` or `subscriptions`. The `snapshot` column contains all data needed to understand and replay the event, allowing subscriptions to be deleted without orphaning issues. Snapshot includes: `subscription_id`, `subscription_name`, `destination_id`, `destination_name`, `destination_url`, `occurred_at`, table info, `pk`, `old_data`, `new_data`, `payload`, `attempts`, `last_error`.

---

## Indexes

```sql
-- Fast polling query
CREATE INDEX idx_outbox_poll
  ON beacon.outbox_events (state, visible_at)
  WHERE state = 'pending';

-- Subscription-based queries
CREATE INDEX idx_outbox_subscription
  ON beacon.outbox_events (subscription_id, created_at);

-- Reaper query for stale locks
CREATE INDEX idx_outbox_delivering
  ON beacon.outbox_events (locked_at)
  WHERE state = 'delivering';
```

---

## SQL Functions

### `beacon.extract_pk`

Extracts primary key columns from a table row.

```sql
beacon.extract_pk(
  p_schema TEXT,    -- Table schema
  p_table TEXT,     -- Table name
  p_new JSONB,      -- NEW row as JSONB (or NULL)
  p_old JSONB       -- OLD row as JSONB (or NULL)
) RETURNS JSONB
```

**Returns:** `{"col1": value1, "col2": value2, ...}` for composite PKs

**Behavior:**
- Queries `information_schema` for PK columns
- Returns error if no PK found (tables must have a primary key)
- Uses `p_new` preferentially, falls back to `p_old`

### `beacon.capture_changes`

Trigger function that captures row changes. See [2_CAPTURE.md](2_CAPTURE.md).

---

## Migration Strategy

Migrations are embedded SQL files in `internal/db/migrations/`.

```
migrations/
├── 001_core.sql      # Initial schema (all tables above)
├── 002_*.sql         # Future migrations
```

**Execution:** Migrations run on startup via `Pool.Migrate()`. Uses a simple version table:

```sql
CREATE TABLE IF NOT EXISTS beacon.schema_version (
  version INT PRIMARY KEY,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

**Migration Policy:**
- Migrations are forward-only; no rollback support
- Always test migrations in staging before production
- Breaking changes require a new major version

---

## Data Retention

### `beacon.delivery_attempts`

This table grows with every delivery attempt. Implement retention to prevent unbounded growth:

```sql
-- Run daily via pg_cron or external scheduler
DELETE FROM beacon.delivery_attempts
WHERE started_at < now() - INTERVAL '30 days';
```

**Recommended retention:** 30 days (configurable via `BEACON_RETENTION_DAYS`)

### `beacon.outbox_events` (delivered)

Successfully delivered events can be archived or deleted:

```sql
-- Archive to cold storage or delete
DELETE FROM beacon.outbox_events
WHERE state = 'delivered'
  AND created_at < now() - INTERVAL '7 days';
```

### `beacon.dead_letters`

Keep indefinitely for debugging, or archive to external storage after review.

---

## Dependencies

- `github.com/jackc/pgx/v5/pgxpool` - Connection pooling
- `github.com/jackc/pgx/v5` - PostgreSQL driver

---

## Testing

### Strategy

Use **real PostgreSQL via testcontainers** for all database tests. This module has no complex logic to mock—its purpose is database interaction, so testing against a real database provides meaningful coverage.

### Test Helpers

```go
// internal/db/testhelpers_test.go

package db_test

import (
    "context"
    "testing"

    "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// SetupTestDB creates a fresh PostgreSQL container for testing.
// Returns the pool and a cleanup function.
func SetupTestDB(t *testing.T) (*db.Pool, func()) {
    t.Helper()
    ctx := context.Background()

    container, err := postgres.Run(ctx,
        "postgres:16-alpine",
        postgres.WithDatabase("beacon_test"),
        postgres.WithUsername("test"),
        postgres.WithPassword("test"),
        postgres.BasicWaitStrategies(),
    )
    if err != nil {
        t.Fatalf("failed to start postgres: %v", err)
    }

    connStr, err := container.ConnectionString(ctx, "sslmode=disable")
    if err != nil {
        t.Fatalf("failed to get connection string: %v", err)
    }

    pool, err := db.New(ctx, connStr)
    if err != nil {
        t.Fatalf("failed to create pool: %v", err)
    }

    // Run migrations
    if err := pool.Migrate(ctx); err != nil {
        t.Fatalf("failed to migrate: %v", err)
    }

    cleanup := func() {
        pool.Close()
        container.Terminate(ctx)
    }

    return pool, cleanup
}
```

### Test Cases

```go
// internal/db/pool_test.go

func TestPool_New(t *testing.T) {
    pool, cleanup := SetupTestDB(t)
    defer cleanup()

    // Verify connection works
    var result int
    err := pool.QueryRow(context.Background(), "SELECT 1").Scan(&result)
    assert.NoError(t, err)
    assert.Equal(t, 1, result)
}

func TestPool_Migrate_Idempotent(t *testing.T) {
    pool, cleanup := SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()

    // Run migrations again - should be no-op
    err := pool.Migrate(ctx)
    assert.NoError(t, err)

    // Verify schema exists
    var exists bool
    err = pool.QueryRow(ctx, `
        SELECT EXISTS (
            SELECT 1 FROM information_schema.schemata
            WHERE schema_name = 'beacon'
        )
    `).Scan(&exists)
    assert.NoError(t, err)
    assert.True(t, exists)
}

func TestPool_WithTx_Commit(t *testing.T) {
    pool, cleanup := SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()

    // Insert via transaction
    err := pool.WithTx(ctx, func(tx pgx.Tx) error {
        _, err := tx.Exec(ctx, `
            INSERT INTO beacon.destinations (name, url)
            VALUES ('test', 'https://example.com')
        `)
        return err
    })
    assert.NoError(t, err)

    // Verify committed
    var count int
    pool.QueryRow(ctx, "SELECT COUNT(*) FROM beacon.destinations").Scan(&count)
    assert.Equal(t, 1, count)
}

func TestPool_WithTx_Rollback(t *testing.T) {
    pool, cleanup := SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()

    // Insert but return error
    err := pool.WithTx(ctx, func(tx pgx.Tx) error {
        tx.Exec(ctx, `
            INSERT INTO beacon.destinations (name, url)
            VALUES ('test', 'https://example.com')
        `)
        return errors.New("rollback")
    })
    assert.Error(t, err)

    // Verify rolled back
    var count int
    pool.QueryRow(ctx, "SELECT COUNT(*) FROM beacon.destinations").Scan(&count)
    assert.Equal(t, 0, count)
}
```

### Schema Verification Tests

```go
func TestSchema_Tables(t *testing.T) {
    pool, cleanup := SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()

    expectedTables := []string{
        "destinations",
        "subscriptions",
        "outbox_events",
        "worker_heartbeats",
        "delivery_attempts",
        "dead_letters",
        "schema_version",
    }

    for _, table := range expectedTables {
        var exists bool
        err := pool.QueryRow(ctx, `
            SELECT EXISTS (
                SELECT 1 FROM information_schema.tables
                WHERE table_schema = 'beacon' AND table_name = $1
            )
        `, table).Scan(&exists)

        assert.NoError(t, err, "checking table %s", table)
        assert.True(t, exists, "table beacon.%s should exist", table)
    }
}

func TestSchema_Indexes(t *testing.T) {
    pool, cleanup := SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()

    expectedIndexes := []string{
        "idx_outbox_poll",
        "idx_outbox_subscription",
        "idx_outbox_delivering",
    }

    for _, index := range expectedIndexes {
        var exists bool
        err := pool.QueryRow(ctx, `
            SELECT EXISTS (
                SELECT 1 FROM pg_indexes
                WHERE schemaname = 'beacon' AND indexname = $1
            )
        `, index).Scan(&exists)

        assert.NoError(t, err, "checking index %s", index)
        assert.True(t, exists, "index %s should exist", index)
    }
}
```

### Running Tests

```bash
# Run database tests (requires Docker)
go test ./internal/db/... -v

# Run with race detector
go test ./internal/db/... -race

# Skip container tests in CI without Docker
go test ./internal/db/... -short
```

---

## Usage Example

```go
pool, err := db.New(ctx, os.Getenv("DATABASE_URL"))
if err != nil {
    log.Fatal(err)
}
defer pool.Close()

if err := pool.Migrate(ctx); err != nil {
    log.Fatal(err)
}

// Use pool in other packages
dispatcher := dispatcher.New(pool, ...)
```
