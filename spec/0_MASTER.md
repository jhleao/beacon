# Beacon Master Specification

## Purpose

Beacon is a **PostgreSQL-native webhook delivery system** that captures database changes via triggers and reliably delivers them to HTTP endpoints. It implements the transactional outbox pattern with at-least-once delivery guarantees.

## Scope

**In Scope:**
- Capture INSERT/UPDATE/DELETE events from any PostgreSQL table
- Reliable webhook delivery with retries and dead-letter queue
- Per-destination concurrency control (semaphores)
- HMAC request signing for webhook security
- YAML-based configuration management
- Soft delete with async drain for subscriptions

**Out of Scope (v1):**
- Ordering guarantees (best-effort only)
- Circuit breakers (rely on semaphores + backoff)
- Reading config from disk (YAML via API only)

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              PostgreSQL                                      │
│  ┌─────────────┐    ┌─────────────────────────────────────────────────────┐ │
│  │ User Tables │───▶│ beacon.capture_changes() TRIGGER                    │ │
│  │  (public.*)  │    │   - Checks subscriptions                            │ │
│  └─────────────┘    │   - Filters columns                                  │ │
│                     │   - Inserts into outbox                              │ │
│                     └─────────────────┬───────────────────────────────────┘ │
│                                       │                                      │
│                                       ▼                                      │
│  ┌─────────────────────────────────────────────────────────────────────────┐ │
│  │                     beacon.outbox_events                                │ │
│  │  pending → delivering → delivered                                       │ │
│  │                    ↓                                                    │ │
│  │                  dead (after max retries)                               │ │
│  └─────────────────────────────────────────────────────────────────────────┘ │
└───────────────────────────────────┬─────────────────────────────────────────┘
                                    │
                                    │ Poll (FOR UPDATE SKIP LOCKED)
                                    ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│                              Beacon Process                                  │
│                                                                              │
│  ┌──────────────┐     ┌──────────────┐     ┌──────────────────────────────┐ │
│  │  Dispatcher  │────▶│ Worker Pool  │────▶│     HTTP Delivery Client     │ │
│  │  (polling)   │     │ (goroutines) │     │  - SSRF guard                │ │
│  └──────────────┘     └──────────────┘     │  - HMAC signing              │ │
│         │                    │              │  - Timeout/retry             │ │
│         │                    │              └──────────────────────────────┘ │
│         │                    │                           │                   │
│         ▼                    ▼                           ▼                   │
│  ┌──────────────┐     ┌──────────────┐           ┌─────────────┐            │
│  │   Reaper     │     │ Heartbeats   │           │ Destination │            │
│  │ (stale work) │     │  (5s tick)   │           │  Endpoints  │            │
│  └──────────────┘     └──────────────┘           └─────────────┘            │
│                                                                              │
│  ┌──────────────────────────────────────────────────────────────────────┐   │
│  │                     Control Plane API                                 │   │
│  │  POST /v1/apply     - Apply YAML config                              │   │
│  │  GET  /v1/config    - Export current config                          │   │
│  │  GET  /v1/health    - Health check                                   │   │
│  │  GET  /v1/metrics   - Prometheus metrics                             │   │
│  └──────────────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

## Data Flow

```
1. User INSERT/UPDATE/DELETE on watched table
              │
              ▼
2. Trigger fires → checks beacon.subscriptions
              │
              ▼
3. For each matching subscription:
   - Filter by trigger_columns (UPDATE only)
   - Build payload with payload_columns
   - INSERT into beacon.outbox_events
              │
              ▼
4. Dispatcher polls outbox (FOR UPDATE SKIP LOCKED)
              │
              ▼
5. Worker claims event → checks destination semaphore
              │
              ├─── Semaphore full? → reschedule +100ms
              │
              ▼
6. HTTP delivery with signing
              │
              ├─── 2xx? → mark delivered
              ├─── 4xx/5xx/timeout? → reschedule with backoff
              └─── Max attempts? → move to dead_letters
```

---

## Project Structure

```
beacon/
├── cmd/
│   └── beacon/
│       └── main.go                 # Single binary entry point
│
├── internal/
│   ├── config/                     # [Spec: 7_CONFIG.md]
│   │   ├── load.go                 # Environment variable loading
│   │   ├── schema.go               # YAML config Go types
│   │   └── parse.go                # YAML parsing & validation
│   │
│   ├── db/                         # [Spec: 1_DATABASE.md]
│   │   ├── pool.go                 # pgxpool wrapper
│   │   ├── migrations/             # SQL migration files
│   │   │   └── 001_core.sql
│   │   └── queries/                # sqlc-generated (optional)
│   │
│   ├── capture/                    # [Spec: 2_CAPTURE.md]
│   │   ├── installer.go            # Trigger install/uninstall
│   │   └── ddl.go                  # Safe SQL identifier handling
│   │
│   ├── outbox/                     # [Spec: 3_OUTBOX.md]
│   │   ├── model.go                # Event types
│   │   └── repository.go           # Claim, ack, reschedule, DLQ
│   │
│   ├── dispatcher/                 # [Spec: 4_DISPATCHER.md]
│   │   ├── dispatcher.go           # Main polling loop
│   │   ├── worker.go               # Worker pool + heartbeats
│   │   ├── semaphore.go            # Per-destination concurrency
│   │   ├── retry.go                # Backoff calculation [Spec: 5_RETRY.md]
│   │   └── reaper.go               # Stale lock recovery
│   │
│   ├── httpdeliver/                # [Spec: 6_HTTPDELIVER.md]
│   │   ├── client.go               # Hardened HTTP client
│   │   ├── ssrf.go                 # SSRF protection
│   │   └── signer.go               # HMAC signing
│   │
│   ├── controlplane/               # [Spec: 8_CONTROLPLANE.md]
│   │   ├── api/
│   │   │   ├── server.go           # HTTP server setup
│   │   │   └── routes.go           # Route handlers
│   │   └── service/
│   │       ├── apply.go            # YAML diff/apply logic
│   │       └── drain.go            # Async drain logic
│   │
│   └── observability/              # [Spec: 9_OBSERVABILITY.md]
│       ├── metrics.go              # Prometheus metrics
│       └── logging.go              # Structured logging
│
├── migrations/                     # SQL migrations (embedded)
│   └── 001_core.sql
│
├── scripts/                        # [Spec: 10_DEVELOPMENT.md]
│   ├── seed.sh                     # Seed script
│   ├── webhook-receiver.go         # Test webhook server
│   └── prometheus.yml              # Prometheus config
│
├── testdata/                       # Test fixtures
│   └── config.yaml                 # Sample config
│
├── spec/                           # This directory
│   ├── 0_MASTER.md                 # You are here
│   ├── 1_DATABASE.md
│   ├── 2_CAPTURE.md
│   ├── 3_OUTBOX.md
│   ├── 4_DISPATCHER.md
│   ├── 5_RETRY.md
│   ├── 6_HTTPDELIVER.md
│   ├── 7_CONFIG.md
│   ├── 8_CONTROLPLANE.md
│   ├── 9_OBSERVABILITY.md
│   └── 10_DEVELOPMENT.md
│
├── docker-compose.yaml             # Local development stack
├── Makefile                        # Dev commands
├── .env.example                    # Environment template
└── go.mod
```

---

## Module Dependency Graph

```
                    ┌─────────────┐
                    │    main     │
                    └──────┬──────┘
                           │
           ┌───────────────┼───────────────┐
           │               │               │
           ▼               ▼               ▼
    ┌─────────────┐ ┌─────────────┐ ┌─────────────┐
    │ dispatcher  │ │controlplane │ │observability│
    └──────┬──────┘ └──────┬──────┘ └─────────────┘
           │               │
     ┌─────┴─────┐   ┌─────┴─────┐
     │           │   │           │
     ▼           ▼   ▼           │
┌─────────┐ ┌─────────┐ ┌───────┐│
│ outbox  │ │httpdeliv│ │capture││
└────┬────┘ └────┬────┘ └───┬───┘│
     │           │          │    │
     └─────┬─────┴──────────┤    │
           │                │    │
           ▼                ▼    ▼
        ┌─────┐         ┌─────────┐
        │ db  │◀────────│ config  │
        └─────┘         └─────────┘
```

---

## Spec Index

| Spec | Module | Purpose |
|------|--------|---------|
| [1_DATABASE.md](1_DATABASE.md) | `internal/db` | Schema, migrations, connection pool |
| [2_CAPTURE.md](2_CAPTURE.md) | `internal/capture` | Trigger installation and management |
| [3_OUTBOX.md](3_OUTBOX.md) | `internal/outbox` | Event model and repository operations |
| [4_DISPATCHER.md](4_DISPATCHER.md) | `internal/dispatcher` | Worker pool, heartbeats, reaper |
| [5_RETRY.md](5_RETRY.md) | `internal/dispatcher` | Backoff policy and DLQ handling |
| [6_HTTPDELIVER.md](6_HTTPDELIVER.md) | `internal/httpdeliver` | HTTP client, SSRF guard, signing |
| [7_CONFIG.md](7_CONFIG.md) | `internal/config` | YAML schema and environment loading |
| [8_CONTROLPLANE.md](8_CONTROLPLANE.md) | `internal/controlplane` | API endpoints and apply logic |
| [9_OBSERVABILITY.md](9_OBSERVABILITY.md) | `internal/observability` | Metrics and logging |
| [10_DEVELOPMENT.md](10_DEVELOPMENT.md) | - | Local dev setup, tooling, DX |

---

## Key Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Crash Recovery | Heartbeat-based | Workers send heartbeats; reaper reclaims on timeout |
| Event ID | UUID (`gen_random_uuid()`) | Simple, no coordination needed |
| Trigger Design | One per table, batch insert | Efficient for multiple subscriptions |
| Fairness | Per-destination semaphores | Simple, prevents one destination from starving others |
| Retry Policy | 1s base, 15min cap, 10 retries | ~38min to DLQ; aggressive but bounded |
| Config Source | YAML via API | Not disk; API is read-only, YAML is truth |
| Secrets | Env vars (`BEACON_HMAC_SECRET`, `BEACON_CONTROLPLANE_SECRET`) | Never in YAML |
| Ordering | Best-effort only | Simplifies implementation significantly |

---

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DATABASE_URL` | Yes | - | PostgreSQL connection string |
| `BEACON_HTTP_ADDR` | No | `:8080` | Control plane listen address |
| `BEACON_POLL_INTERVAL` | No | `100ms` | Outbox poll interval |
| `BEACON_BATCH_SIZE` | No | `100` | Events claimed per poll |
| `BEACON_WORKER_COUNT` | No | `10` | Worker goroutines |
| `BEACON_HMAC_SECRET` | No | - | Global HMAC signing secret for webhook requests |
| `BEACON_CONTROLPLANE_SECRET` | Yes | - | Bearer token for control plane API authentication |
| `BEACON_MAX_PAYLOAD_BYTES` | No | `1048576` | Maximum payload size (1MB default) |

---

## Security Considerations

| Concern | Mitigation |
|---------|------------|
| **API authentication** | Control plane requires `Authorization: Bearer <BEACON_CONTROLPLANE_SECRET>` header |
| **SSRF attacks** | URLs validated against blocked IP ranges; DNS resolution checked before connect |
| **Secret exposure** | Secrets via environment variables only; never in config or logs |
| **SQL injection** | All queries use parameterized statements |
| **Payload size** | Configurable limit prevents memory exhaustion |

---

## Testing Strategy

### Philosophy

Beacon uses **real dependencies over mocks** whenever practical. This approach catches integration issues early and keeps test code simple.

| Dependency | Testing Approach |
|------------|------------------|
| PostgreSQL | Testcontainers (real database per test) |
| HTTP endpoints | `net/http/httptest` mock servers |
| Time/randomness | Inject values where needed; avoid mocking stdlib |

### Test Dependencies

```go
// go.mod test dependencies
require (
    github.com/stretchr/testify v1.9.0
    github.com/testcontainers/testcontainers-go v0.34.0
    github.com/testcontainers/testcontainers-go/modules/postgres v0.34.0
)
```

### Test Categories

| Category | Command | Docker Required | Purpose |
|----------|---------|-----------------|---------|
| Unit | `go test -short ./...` | No | Pure logic: retry calculation, config parsing, SSRF validation |
| Integration | `go test ./...` | Yes | Database operations, full delivery flows, API endpoints |

All tests should check `testing.Short()` and skip container-based tests:

```go
func TestClaimEvents(t *testing.T) {
    if testing.Short() {
        t.Skip("skipping integration test")
    }
    // ... testcontainers setup
}
```

### Shared Test Helpers

All database-touching tests share a common helper defined in `internal/testutil/db.go`:

```go
package testutil

import (
    "context"
    "testing"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/testcontainers/testcontainers-go"
    "github.com/testcontainers/testcontainers-go/modules/postgres"
    "github.com/testcontainers/testcontainers-go/wait"
)

// SetupTestDB creates a real PostgreSQL container for testing.
// Returns a connection pool and cleanup function.
func SetupTestDB(t *testing.T) (*pgxpool.Pool, func()) {
    t.Helper()
    ctx := context.Background()

    container, err := postgres.Run(ctx,
        "postgres:16-alpine",
        postgres.WithDatabase("beacon_test"),
        postgres.WithUsername("beacon"),
        postgres.WithPassword("beacon"),
        testcontainers.WithWaitStrategy(
            wait.ForLog("database system is ready to accept connections").
                WithOccurrence(2).
                WithStartupTimeout(30*time.Second),
        ),
    )
    if err != nil {
        t.Fatalf("failed to start postgres container: %v", err)
    }

    connStr, err := container.ConnectionString(ctx, "sslmode=disable")
    if err != nil {
        container.Terminate(ctx)
        t.Fatalf("failed to get connection string: %v", err)
    }

    pool, err := pgxpool.New(ctx, connStr)
    if err != nil {
        container.Terminate(ctx)
        t.Fatalf("failed to create pool: %v", err)
    }

    // Run migrations
    if err := db.RunMigrations(ctx, pool); err != nil {
        pool.Close()
        container.Terminate(ctx)
        t.Fatalf("failed to run migrations: %v", err)
    }

    cleanup := func() {
        pool.Close()
        container.Terminate(ctx)
    }

    return pool, cleanup
}
```

### Running Tests

```bash
# Unit tests only (fast, no Docker)
go test -short -v ./...

# All tests including integration (requires Docker)
go test -v ./...

# Single package
go test -v ./internal/outbox/...

# With race detection
go test -race ./...

# With coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

### Test Organization by Module

| Module | Test Type | Key Test Scenarios |
|--------|-----------|-------------------|
| `db` | Integration | Pool connections, migrations idempotent, transactions |
| `capture` | Integration | Trigger install, INSERT/UPDATE/DELETE capture, column filtering |
| `outbox` | Integration | Claim, Ack, Reschedule, ToDead, concurrent claims |
| `dispatcher` | Integration | Full delivery flow, retry on failure, DLQ after max attempts |
| `retry` | Unit | Exponential backoff, jitter bounds, status code classification |
| `httpdeliver` | Unit + Mock | SSRF guard, signing, HTTP delivery with httptest servers |
| `config` | Unit | YAML parsing, validation rules, env var loading |
| `controlplane` | Integration | CRUD via API, dry run, auth, export, drain |
| `observability` | Unit | Metric values, logger output, health serialization |

See each module's spec file for detailed test scenarios and example code.

### CI/CD Integration

```yaml
# Example GitHub Actions workflow
name: Test
on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'

      # Unit tests (fast feedback)
      - name: Unit Tests
        run: go test -short -v ./...

      # Integration tests (full validation)
      - name: Integration Tests
        run: go test -v -race -coverprofile=coverage.out ./...

      - name: Upload Coverage
        uses: codecov/codecov-action@v4
        with:
          files: coverage.out
```

### Chaos Testing

Manual chaos testing scenarios for pre-release validation:

| Scenario | How to Test | Expected Behavior |
|----------|-------------|-------------------|
| Worker crash | Kill beacon process mid-delivery | Reaper reclaims events after heartbeat timeout |
| Slow destination | Add artificial delay to webhook receiver | Semaphore limits concurrent attempts |
| Database disconnect | Stop postgres container | Graceful degradation, reconnect when available |
| Full semaphore | Send burst of events to single destination | Events reschedule with +100ms delay |
