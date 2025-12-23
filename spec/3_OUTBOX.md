# 3. Outbox Specification

## Purpose

The outbox module provides the event model and repository operations for the transactional outbox pattern. It handles claiming events, acknowledging delivery, rescheduling failures, and moving exhausted events to the dead-letter queue.

---

## Exposed API

### Package: `internal/outbox`

#### Types

```go
// Event represents an outbox event
type Event struct {
    ID             uuid.UUID
    SubscriptionID uuid.UUID
    OccurredAt     time.Time
    TableSchema    string
    TableName      string
    Operation      string        // "INSERT", "UPDATE", "DELETE"
    PK             json.RawMessage
    OldData        json.RawMessage  // nil for INSERT
    NewData        json.RawMessage  // nil for DELETE
    Payload        json.RawMessage
    State          State
    VisibleAt      time.Time
    LockedBy       *string
    LockedAt       *time.Time
    Attempts       int
    LastError      *string
    CreatedAt      time.Time
}

// State represents the event lifecycle state
type State string

const (
    StatePending    State = "pending"
    StateDelivering State = "delivering"
    StateDelivered  State = "delivered"
    StateDead       State = "dead"
)

// Destination contains delivery target info (joined from subscriptions)
type Destination struct {
    ID          uuid.UUID
    Name        string
    URL         string
    Method      string
    Headers     map[string]string
    TimeoutMs   int
    MaxInFlight int
}

// ClaimedEvent bundles an event with its destination
type ClaimedEvent struct {
    Event       Event
    Destination Destination
}
```

#### Repository

```go
// Repository handles outbox database operations
type Repository struct {
    pool *db.Pool
}

// New creates a Repository
func New(pool *db.Pool) *Repository

// Claim atomically claims up to `limit` pending events for `workerID`
// Returns events with their destination info
func (r *Repository) Claim(ctx context.Context, workerID string, limit int) ([]ClaimedEvent, error)

// Ack marks an event as successfully delivered
func (r *Repository) Ack(ctx context.Context, eventID uuid.UUID) error

// Reschedule marks an event for retry at `visibleAt` with error message
func (r *Repository) Reschedule(ctx context.Context, eventID uuid.UUID, visibleAt time.Time, errMsg string) error

// ToDead moves an event to the dead-letter queue
func (r *Repository) ToDead(ctx context.Context, eventID uuid.UUID, reason string) error

// RecordAttempt logs a delivery attempt for audit
func (r *Repository) RecordAttempt(ctx context.Context, attempt DeliveryAttempt) error

// CountByState returns event counts grouped by state
func (r *Repository) CountByState(ctx context.Context) (map[State]int64, error)

// CountPendingForSubscription returns pending event count for a subscription
func (r *Repository) CountPendingForSubscription(ctx context.Context, subID uuid.UUID) (int64, error)
```

```go
// DeliveryAttempt records a single delivery attempt
type DeliveryAttempt struct {
    EventID         uuid.UUID
    DestinationID   uuid.UUID
    Attempt         int
    StartedAt       time.Time
    FinishedAt      time.Time
    StatusCode      *int
    Error           *string
    ResponseHeaders map[string]string
}
```

---

## Internal Implementation

### Claim Query

Uses `FOR UPDATE SKIP LOCKED` for high-concurrency claiming without blocking. The query atomically claims events AND fetches destination info in a single statement to avoid race conditions if a destination is deleted between claim and delivery:

```sql
WITH claimed AS (
    SELECT e.id
    FROM beacon.outbox_events e
    WHERE e.state = 'pending'
      AND e.visible_at <= now()
    ORDER BY e.visible_at, e.occurred_at
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
UPDATE beacon.outbox_events e
SET
    state = 'delivering',
    locked_by = $2,
    locked_at = now(),
    attempts = attempts + 1
FROM claimed c
JOIN beacon.subscriptions s ON s.id = e.subscription_id
JOIN beacon.destinations d ON d.id = s.destination_id
WHERE e.id = c.id
  AND s.deleted_at IS NULL
RETURNING
    e.id,
    e.subscription_id,
    e.occurred_at,
    e.table_schema,
    e.table_name,
    e.operation,
    e.pk,
    e.old_data,
    e.new_data,
    e.payload,
    e.state,
    e.visible_at,
    e.locked_by,
    e.locked_at,
    e.attempts,
    e.last_error,
    e.created_at,
    -- Destination fields (atomically fetched)
    d.id AS dest_id,
    d.name AS dest_name,
    d.url AS dest_url,
    d.method AS dest_method,
    d.headers AS dest_headers,
    d.timeout_ms AS dest_timeout_ms,
    d.max_in_flight AS dest_max_in_flight,
    d.ssrf_policy AS dest_ssrf_policy;
```

> **Note:** The JOIN ensures that if a subscription or destination is deleted between polling cycles, the event will not be claimed (the WHERE clause excludes soft-deleted subscriptions). This prevents orphaned claim attempts.

### Ack Query

```sql
UPDATE beacon.outbox_events
SET
    state = 'delivered',
    locked_by = NULL,
    locked_at = NULL
WHERE id = $1;
```

### Reschedule Query

```sql
UPDATE beacon.outbox_events
SET
    state = 'pending',
    locked_by = NULL,
    locked_at = NULL,
    visible_at = $2,
    last_error = $3
WHERE id = $1;
```

### ToDead Query

```sql
-- Begin transaction

-- Insert snapshot into dead_letters (self-contained with destination info)
INSERT INTO beacon.dead_letters (event_id, reason, snapshot)
SELECT
    e.id,
    $2,
    jsonb_build_object(
        'subscription_id', e.subscription_id,
        'subscription_name', s.name,
        'destination_id', d.id,
        'destination_name', d.name,
        'destination_url', d.url,
        'occurred_at', e.occurred_at,
        'table_schema', e.table_schema,
        'table_name', e.table_name,
        'operation', e.operation,
        'pk', e.pk,
        'old_data', e.old_data,
        'new_data', e.new_data,
        'payload', e.payload,
        'attempts', e.attempts,
        'last_error', e.last_error
    )
FROM beacon.outbox_events e
JOIN beacon.subscriptions s ON s.id = e.subscription_id
JOIN beacon.destinations d ON d.id = s.destination_id
WHERE e.id = $1;

-- Update state
UPDATE beacon.outbox_events
SET
    state = 'dead',
    locked_by = NULL,
    locked_at = NULL
WHERE id = $1;

-- Commit
```

> **Note:** The snapshot is intentionally self-contained, including subscription and destination names/URLs. This allows dead letters to remain meaningful even after the original subscription or destination is deleted.

### RecordAttempt Query

```sql
INSERT INTO beacon.delivery_attempts (
    event_id,
    destination_id,
    attempt,
    started_at,
    finished_at,
    status_code,
    error,
    response_headers
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8);
```

---

## Event State Machine

```
                         ┌──────────────────────────────────────┐
                         │                                      │
                         ▼                                      │
┌─────────────┐    ┌─────────────┐    ┌─────────────┐          │
│   pending   │───▶│ delivering  │───▶│  delivered  │          │
└─────────────┘    └─────────────┘    └─────────────┘          │
      ▲                   │                                     │
      │                   │ failure                             │
      │                   ▼                                     │
      │            ┌─────────────┐                              │
      └────────────│ reschedule  │──────────────────────────────┘
                   │ (visible_at)│
                   └─────────────┘
                         │
                         │ max attempts exceeded
                         ▼
                   ┌─────────────┐
                   │    dead     │
                   └─────────────┘
```

### State Transitions

| From | To | Trigger | Side Effects |
|------|----|---------|--------------|
| `pending` | `delivering` | Worker claims event | `locked_by`, `locked_at`, `attempts++` |
| `delivering` | `delivered` | 2xx response | Clear lock fields |
| `delivering` | `pending` | Failure + retries left | Set `visible_at`, clear lock |
| `delivering` | `dead` | Max attempts exceeded | Insert into `dead_letters` |
| `delivering` | `pending` | Worker crash (reaper) | Clear lock fields |

---

## Visibility Delay

The `visible_at` column controls when an event becomes claimable:

- **Initial insert:** `visible_at = now()` (immediately visible)
- **After failure:** `visible_at = now() + backoff_delay` (see [5_RETRY.md](5_RETRY.md))
- **Semaphore full:** `visible_at = now() + 100ms` (short delay)

The claim query only selects events where `visible_at <= now()`.

---

## Locking Semantics

### Optimistic Concurrency

`FOR UPDATE SKIP LOCKED` ensures:
- Multiple workers can poll concurrently
- No blocking on contested rows
- Each event is claimed by exactly one worker

### Lock Fields

| Field | Purpose |
|-------|---------|
| `locked_by` | Worker ID holding the lock |
| `locked_at` | When lock was acquired (for reaper) |

### Crash Recovery

If a worker crashes while delivering, the reaper (see [4_DISPATCHER.md](4_DISPATCHER.md)) detects the stale lock via:
1. Missing heartbeat for worker
2. `locked_at` older than threshold

---

## Dependencies

- `internal/db` - Database connection pool
- `github.com/google/uuid` - UUID handling

---

## Testing

### Strategy

Use **real PostgreSQL via testcontainers** for all outbox tests. The repository is pure database interaction—no HTTP or external dependencies to mock. Tests verify SQL correctness and locking semantics.

### Test Helpers

```go
// internal/outbox/testhelpers_test.go

package outbox_test

// InsertTestEvent creates a test event in the outbox
func InsertTestEvent(t *testing.T, pool *db.Pool, subID, destID uuid.UUID) uuid.UUID {
    t.Helper()
    ctx := context.Background()

    var eventID uuid.UUID
    err := pool.QueryRow(ctx, `
        INSERT INTO beacon.outbox_events
            (subscription_id, table_schema, table_name, operation, pk, payload, state, visible_at)
        VALUES ($1, 'public', 'test', 'INSERT', '{"id":1}', '{"test":true}', 'pending', now())
        RETURNING id
    `, subID).Scan(&eventID)
    if err != nil {
        t.Fatalf("failed to insert test event: %v", err)
    }
    return eventID
}

// SetupTestFixtures creates destination and subscription for testing
func SetupTestFixtures(t *testing.T, pool *db.Pool) (destID, subID uuid.UUID) {
    t.Helper()
    ctx := context.Background()

    err := pool.QueryRow(ctx, `
        INSERT INTO beacon.destinations (name, url, max_in_flight)
        VALUES ('test-dest', 'https://example.com', 50)
        RETURNING id
    `).Scan(&destID)
    if err != nil {
        t.Fatalf("failed to create destination: %v", err)
    }

    err = pool.QueryRow(ctx, `
        INSERT INTO beacon.subscriptions (name, table_schema, table_name, operation, destination_id)
        VALUES ('test-sub', 'public', 'test', 'INSERT', $1)
        RETURNING id
    `, destID).Scan(&subID)
    if err != nil {
        t.Fatalf("failed to create subscription: %v", err)
    }

    return destID, subID
}
```

### Test Cases

```go
// internal/outbox/repository_test.go

func TestRepository_Claim(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    repo := outbox.New(pool)
    destID, subID := SetupTestFixtures(t, pool)

    // Insert pending event
    eventID := InsertTestEvent(t, pool, subID, destID)

    // Claim it
    claimed, err := repo.Claim(ctx, "worker-1", 10)
    assert.NoError(t, err)
    assert.Len(t, claimed, 1)
    assert.Equal(t, eventID, claimed[0].Event.ID)
    assert.Equal(t, "delivering", string(claimed[0].Event.State))
    assert.Equal(t, 1, claimed[0].Event.Attempts)

    // Verify destination info populated
    assert.Equal(t, destID, claimed[0].Destination.ID)
    assert.Equal(t, "https://example.com", claimed[0].Destination.URL)
}

func TestRepository_Claim_RespectsVisibleAt(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    repo := outbox.New(pool)
    destID, subID := SetupTestFixtures(t, pool)

    // Insert event visible in the future
    pool.Exec(ctx, `
        INSERT INTO beacon.outbox_events
            (subscription_id, table_schema, table_name, operation, pk, payload, state, visible_at)
        VALUES ($1, 'public', 'test', 'INSERT', '{"id":1}', '{}', 'pending', now() + interval '1 hour')
    `, subID)

    // Claim should return nothing
    claimed, err := repo.Claim(ctx, "worker-1", 10)
    assert.NoError(t, err)
    assert.Len(t, claimed, 0)
}

func TestRepository_Claim_SkipLocked(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    repo := outbox.New(pool)
    destID, subID := SetupTestFixtures(t, pool)

    // Insert two events
    InsertTestEvent(t, pool, subID, destID)
    InsertTestEvent(t, pool, subID, destID)

    // Worker 1 claims first
    claimed1, err := repo.Claim(ctx, "worker-1", 1)
    assert.NoError(t, err)
    assert.Len(t, claimed1, 1)

    // Worker 2 claims next (should skip locked)
    claimed2, err := repo.Claim(ctx, "worker-2", 1)
    assert.NoError(t, err)
    assert.Len(t, claimed2, 1)

    // Different events
    assert.NotEqual(t, claimed1[0].Event.ID, claimed2[0].Event.ID)
}

func TestRepository_Ack(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    repo := outbox.New(pool)
    destID, subID := SetupTestFixtures(t, pool)
    eventID := InsertTestEvent(t, pool, subID, destID)

    // Claim then ack
    repo.Claim(ctx, "worker-1", 1)
    err := repo.Ack(ctx, eventID)
    assert.NoError(t, err)

    // Verify state
    var state string
    pool.QueryRow(ctx, `SELECT state FROM beacon.outbox_events WHERE id = $1`, eventID).Scan(&state)
    assert.Equal(t, "delivered", state)
}

func TestRepository_Reschedule(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    repo := outbox.New(pool)
    destID, subID := SetupTestFixtures(t, pool)
    eventID := InsertTestEvent(t, pool, subID, destID)

    // Claim then reschedule
    repo.Claim(ctx, "worker-1", 1)
    visibleAt := time.Now().Add(5 * time.Minute)
    err := repo.Reschedule(ctx, eventID, visibleAt, "connection refused")
    assert.NoError(t, err)

    // Verify state and visible_at
    var state, lastError string
    var newVisibleAt time.Time
    pool.QueryRow(ctx, `
        SELECT state, visible_at, last_error
        FROM beacon.outbox_events WHERE id = $1
    `, eventID).Scan(&state, &newVisibleAt, &lastError)

    assert.Equal(t, "pending", state)
    assert.WithinDuration(t, visibleAt, newVisibleAt, time.Second)
    assert.Equal(t, "connection refused", lastError)
}

func TestRepository_ToDead(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    repo := outbox.New(pool)
    destID, subID := SetupTestFixtures(t, pool)
    eventID := InsertTestEvent(t, pool, subID, destID)

    // Claim then move to dead
    repo.Claim(ctx, "worker-1", 1)
    err := repo.ToDead(ctx, eventID, "max attempts exceeded")
    assert.NoError(t, err)

    // Verify outbox state
    var state string
    pool.QueryRow(ctx, `SELECT state FROM beacon.outbox_events WHERE id = $1`, eventID).Scan(&state)
    assert.Equal(t, "dead", state)

    // Verify dead_letters entry
    var reason string
    var snapshot json.RawMessage
    err = pool.QueryRow(ctx, `
        SELECT reason, snapshot FROM beacon.dead_letters WHERE event_id = $1
    `, eventID).Scan(&reason, &snapshot)
    assert.NoError(t, err)
    assert.Equal(t, "max attempts exceeded", reason)
    assert.NotEmpty(t, snapshot)
}

func TestRepository_RecordAttempt(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    repo := outbox.New(pool)
    destID, subID := SetupTestFixtures(t, pool)
    eventID := InsertTestEvent(t, pool, subID, destID)

    statusCode := 503
    attempt := outbox.DeliveryAttempt{
        EventID:       eventID,
        DestinationID: destID,
        Attempt:       1,
        StartedAt:     time.Now().Add(-100 * time.Millisecond),
        FinishedAt:    time.Now(),
        StatusCode:    &statusCode,
        Error:         ptr("service unavailable"),
    }

    err := repo.RecordAttempt(ctx, attempt)
    assert.NoError(t, err)

    // Verify recorded
    var count int
    pool.QueryRow(ctx, `SELECT COUNT(*) FROM beacon.delivery_attempts WHERE event_id = $1`, eventID).Scan(&count)
    assert.Equal(t, 1, count)
}

func TestRepository_CountByState(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    repo := outbox.New(pool)
    destID, subID := SetupTestFixtures(t, pool)

    // Insert events in different states
    InsertTestEvent(t, pool, subID, destID)
    InsertTestEvent(t, pool, subID, destID)
    pool.Exec(ctx, `
        INSERT INTO beacon.outbox_events
            (subscription_id, table_schema, table_name, operation, pk, payload, state)
        VALUES ($1, 'public', 'test', 'INSERT', '{"id":3}', '{}', 'delivered')
    `, subID)

    counts, err := repo.CountByState(ctx)
    assert.NoError(t, err)
    assert.Equal(t, int64(2), counts[outbox.StatePending])
    assert.Equal(t, int64(1), counts[outbox.StateDelivered])
}

func TestRepository_Claim_ExcludesDeletedSubscriptions(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    repo := outbox.New(pool)
    destID, subID := SetupTestFixtures(t, pool)
    InsertTestEvent(t, pool, subID, destID)

    // Soft-delete subscription
    pool.Exec(ctx, `UPDATE beacon.subscriptions SET deleted_at = now() WHERE id = $1`, subID)

    // Claim should return nothing
    claimed, err := repo.Claim(ctx, "worker-1", 10)
    assert.NoError(t, err)
    assert.Len(t, claimed, 0)
}

func ptr[T any](v T) *T { return &v }
```

### Concurrency Tests

```go
func TestRepository_Claim_Concurrent(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()
    repo := outbox.New(pool)
    destID, subID := SetupTestFixtures(t, pool)

    // Insert 100 events
    for i := 0; i < 100; i++ {
        InsertTestEvent(t, pool, subID, destID)
    }

    // 10 workers claim concurrently
    var wg sync.WaitGroup
    claimed := make(chan uuid.UUID, 100)

    for i := 0; i < 10; i++ {
        wg.Add(1)
        go func(workerID int) {
            defer wg.Done()
            events, _ := repo.Claim(ctx, fmt.Sprintf("worker-%d", workerID), 20)
            for _, e := range events {
                claimed <- e.Event.ID
            }
        }(i)
    }

    wg.Wait()
    close(claimed)

    // Collect all claimed event IDs
    ids := make(map[uuid.UUID]bool)
    for id := range claimed {
        assert.False(t, ids[id], "event %s claimed twice", id)
        ids[id] = true
    }

    assert.Len(t, ids, 100, "all events should be claimed exactly once")
}
```

### Running Tests

```bash
# Run outbox tests
go test ./internal/outbox/... -v

# Run with race detector (important for concurrency tests)
go test ./internal/outbox/... -race
```

---

## Usage Example

```go
repo := outbox.New(pool)

// Claim events for processing
events, err := repo.Claim(ctx, "worker-1", 100)
if err != nil {
    return err
}

for _, claimed := range events {
    // Attempt delivery...
    if success {
        repo.Ack(ctx, claimed.Event.ID)
    } else if claimed.Event.Attempts < maxAttempts {
        nextVisible := time.Now().Add(calculateBackoff(claimed.Event.Attempts))
        repo.Reschedule(ctx, claimed.Event.ID, nextVisible, "connection timeout")
    } else {
        repo.ToDead(ctx, claimed.Event.ID, "max attempts exceeded")
    }
}
```

---

## Performance Considerations

1. **Index usage:** The `idx_outbox_poll` partial index ensures fast claims
2. **Batch claiming:** Claim multiple events per query to reduce round trips
3. **SKIP LOCKED:** Prevents convoy effects under high concurrency
4. **Visibility ordering:** `ORDER BY visible_at, occurred_at` ensures fairness
