# 4. Dispatcher Specification

## Purpose

The dispatcher is the core event processing engine. It polls the outbox, manages a worker pool for concurrent delivery, sends heartbeats for crash recovery, and runs the reaper to reclaim stale work.

---

## Exposed API

### Package: `internal/dispatcher`

```go
// Config holds dispatcher configuration
type Config struct {
    PollInterval time.Duration  // How often to poll outbox (default: 100ms)
    BatchSize    int            // Events per poll (default: 100)
    WorkerCount  int            // Concurrent workers (default: 10)
}

// Dispatcher orchestrates event processing
type Dispatcher struct {
    // contains unexported fields
}

// New creates a Dispatcher
func New(
    pool *db.Pool,
    repo *outbox.Repository,
    client *httpdeliver.Client,
    cfg Config,
) *Dispatcher

// Start begins polling and processing (blocks until context cancelled)
func (d *Dispatcher) Start(ctx context.Context) error

// WorkerID returns this dispatcher's unique worker ID
func (d *Dispatcher) WorkerID() string
```

```go
// Semaphores manages per-destination concurrency limits
type Semaphores struct {
    // contains unexported fields
}

// NewSemaphores creates a Semaphores manager with initial limits from config
func NewSemaphores(destinations []Destination) *Semaphores

// Acquire attempts to acquire a slot for the destination
// Returns true if acquired, false if at capacity
func (s *Semaphores) Acquire(destID uuid.UUID) bool

// Release releases a slot for the destination
func (s *Semaphores) Release(destID uuid.UUID)
```

> **Limitation:** Changing `max_in_flight` requires a Beacon restart. Hot updates to concurrency limits are not supported to avoid race conditions with in-flight deliveries. Semaphores are initialized at startup from the current destination configuration and remain fixed for the lifetime of the process.

---

## Internal Implementation

### Worker ID Generation

Each dispatcher generates a unique worker ID on startup:

```go
func generateWorkerID() string {
    hostname, _ := os.Hostname()
    return fmt.Sprintf("%s-%d-%s",
        hostname,
        os.Getpid(),
        uuid.New().String()[:8],
    )
}
```

Example: `web-server-1-12345-a1b2c3d4`

### Main Loop

```go
func (d *Dispatcher) Start(ctx context.Context) error {
    // Register worker
    if err := d.registerWorker(ctx); err != nil {
        return err
    }
    defer d.unregisterWorker(context.Background())

    // Start background goroutines
    g, ctx := errgroup.WithContext(ctx)

    // Heartbeat loop
    g.Go(func() error {
        return d.heartbeatLoop(ctx)
    })

    // Reaper loop
    g.Go(func() error {
        return d.reaperLoop(ctx)
    })

    // Poll loop
    g.Go(func() error {
        return d.pollLoop(ctx)
    })

    return g.Wait()
}
```

### Poll Loop

The poll loop uses non-blocking sends to prevent backpressure from blocking heartbeats:

```go
func (d *Dispatcher) pollLoop(ctx context.Context) error {
    ticker := time.NewTicker(d.cfg.PollInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            events, err := d.repo.Claim(ctx, d.workerID, d.cfg.BatchSize)
            if err != nil {
                d.logger.Error("claim failed", "error", err)
                continue
            }

            for _, claimed := range events {
                select {
                case d.workChan <- claimed:
                    // Sent to worker pool
                default:
                    // Channel full, reschedule with short delay
                    d.repo.Reschedule(ctx, claimed.Event.ID, time.Now().Add(50*time.Millisecond), "worker pool full")
                }
            }
        }
    }
}
```

> **Note:** `workChan` is buffered to `WorkerCount * 2`. If the buffer fills (all workers busy), events are rescheduled with a 50ms delay rather than blocking the poll loop. This ensures heartbeats continue even under heavy load.

### Worker Pool

```go
func (d *Dispatcher) startWorkers(ctx context.Context) {
    for i := 0; i < d.cfg.WorkerCount; i++ {
        go d.worker(ctx)
    }
}

func (d *Dispatcher) worker(ctx context.Context) {
    for {
        select {
        case <-ctx.Done():
            return
        case claimed := <-d.workChan:
            d.processEvent(ctx, claimed)
        }
    }
}

func (d *Dispatcher) processEvent(ctx context.Context, claimed outbox.ClaimedEvent) {
    event := claimed.Event
    dest := claimed.Destination

    // Check semaphore
    if !d.semaphores.Acquire(dest.ID) {
        // Destination at capacity, reschedule with short delay
        d.repo.Reschedule(ctx, event.ID, time.Now().Add(100*time.Millisecond), "destination at capacity")
        return
    }
    defer d.semaphores.Release(dest.ID)

    // Attempt delivery
    attempt := DeliveryAttempt{
        EventID:       event.ID,
        DestinationID: dest.ID,
        Attempt:       event.Attempts,
        StartedAt:     time.Now(),
    }

    statusCode, respHeaders, err := d.client.Deliver(ctx, dest, event)
    attempt.FinishedAt = time.Now()
    attempt.StatusCode = statusCode
    attempt.ResponseHeaders = respHeaders

    if err != nil {
        attempt.Error = err.Error()
    }

    // Record attempt for audit
    d.repo.RecordAttempt(ctx, attempt)

    // Handle result
    if err == nil && statusCode != nil && *statusCode >= 200 && *statusCode < 300 {
        d.repo.Ack(ctx, event.ID)
        d.metrics.DeliverySuccess(dest.Name)
    } else {
        d.handleFailure(ctx, event, dest, err)
    }
}
```

### Failure Handling

```go
func (d *Dispatcher) handleFailure(ctx context.Context, event outbox.Event, dest outbox.Destination, err error) {
    errMsg := "unknown error"
    if err != nil {
        errMsg = err.Error()
    }

    if event.Attempts >= retry.MaxAttempts {
        d.repo.ToDead(ctx, event.ID, errMsg)
        d.metrics.DeadLetter(dest.Name)
    } else {
        nextDelay := retry.NextDelay(event.Attempts)
        d.repo.Reschedule(ctx, event.ID, time.Now().Add(nextDelay), errMsg)
        d.metrics.DeliveryRetry(dest.Name)
    }
}
```

---

## Heartbeats

### Purpose

Heartbeats allow the reaper to detect crashed workers and reclaim their in-progress events.

### Implementation

```go
const (
    HeartbeatInterval = 5 * time.Second
    HeartbeatTimeout  = 30 * time.Second  // 6x interval allows for transient failures
)

func (d *Dispatcher) registerWorker(ctx context.Context) error {
    _, err := d.pool.Exec(ctx, `
        INSERT INTO beacon.worker_heartbeats (worker_id, last_heartbeat, started_at)
        VALUES ($1, now(), now())
        ON CONFLICT (worker_id) DO UPDATE SET last_heartbeat = now()
    `, d.workerID)
    return err
}

func (d *Dispatcher) unregisterWorker(ctx context.Context) {
    d.pool.Exec(ctx, `
        DELETE FROM beacon.worker_heartbeats WHERE worker_id = $1
    `, d.workerID)
}

func (d *Dispatcher) heartbeatLoop(ctx context.Context) error {
    ticker := time.NewTicker(HeartbeatInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            _, err := d.pool.Exec(ctx, `
                UPDATE beacon.worker_heartbeats
                SET last_heartbeat = now()
                WHERE worker_id = $1
            `, d.workerID)
            if err != nil {
                d.logger.Error("heartbeat failed", "error", err)
            }
        }
    }
}
```

---

## Reaper

### Purpose

The reaper runs periodically to:
1. Detect workers that have stopped sending heartbeats
2. Reclaim events locked by dead workers

### Implementation

```go
const ReaperInterval = 10 * time.Second

func (d *Dispatcher) reaperLoop(ctx context.Context) error {
    ticker := time.NewTicker(ReaperInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            if err := d.reap(ctx); err != nil {
                d.logger.Error("reap failed", "error", err)
            }
        }
    }
}

func (d *Dispatcher) reap(ctx context.Context) error {
    // Find stale workers
    rows, err := d.pool.Query(ctx, `
        SELECT worker_id
        FROM beacon.worker_heartbeats
        WHERE last_heartbeat < now() - INTERVAL '30 seconds'
    `)
    if err != nil {
        return err
    }
    defer rows.Close()

    var staleWorkers []string
    for rows.Next() {
        var workerID string
        if err := rows.Scan(&workerID); err != nil {
            return err
        }
        staleWorkers = append(staleWorkers, workerID)
    }

    // Reclaim events from stale workers
    if len(staleWorkers) > 0 {
        d.logger.Info("reaping stale workers", "count", len(staleWorkers))

        result, err := d.pool.Exec(ctx, `
            UPDATE beacon.outbox_events
            SET
                state = 'pending',
                locked_by = NULL,
                locked_at = NULL,
                visible_at = now()
            WHERE state = 'delivering'
              AND locked_by = ANY($1)
        `, staleWorkers)
        if err != nil {
            return err
        }

        d.logger.Info("reclaimed events from stale workers", "count", result.RowsAffected())

        // Delete stale worker records
        _, err = d.pool.Exec(ctx, `
            DELETE FROM beacon.worker_heartbeats
            WHERE worker_id = ANY($1)
        `, staleWorkers)
        if err != nil {
            return err
        }
    }

    // Also reclaim zombie locks: events locked by workers that never registered
    // or were removed from heartbeat table before their events were released
    result, err := d.pool.Exec(ctx, `
        UPDATE beacon.outbox_events
        SET
            state = 'pending',
            locked_by = NULL,
            locked_at = NULL,
            visible_at = now()
        WHERE state = 'delivering'
          AND locked_by IS NOT NULL
          AND locked_by NOT IN (SELECT worker_id FROM beacon.worker_heartbeats)
          AND locked_at < now() - INTERVAL '30 seconds'
    `)
    if err != nil {
        return err
    }

    if result.RowsAffected() > 0 {
        d.logger.Info("reclaimed zombie locks", "count", result.RowsAffected())
    }

    return nil
}
```

---

## Semaphores

### Purpose

Per-destination semaphores prevent any single destination from consuming all workers and starving other destinations.

### Implementation

```go
type Semaphores struct {
    mu   sync.RWMutex
    sems map[uuid.UUID]*semaphore
}

type semaphore struct {
    ch    chan struct{}
    limit int
}

// NewSemaphores initializes semaphores from destination config at startup.
// Limits are fixed for the process lifetime.
func NewSemaphores(destinations []Destination) *Semaphores {
    s := &Semaphores{
        sems: make(map[uuid.UUID]*semaphore),
    }
    for _, dest := range destinations {
        s.sems[dest.ID] = &semaphore{
            ch:    make(chan struct{}, dest.MaxInFlight),
            limit: dest.MaxInFlight,
        }
    }
    return s
}

func (s *Semaphores) Acquire(destID uuid.UUID) bool {
    s.mu.RLock()
    sem, exists := s.sems[destID]
    s.mu.RUnlock()

    if !exists {
        // Unknown destination (shouldn't happen), allow
        return true
    }

    select {
    case sem.ch <- struct{}{}:
        return true
    default:
        return false
    }
}

func (s *Semaphores) Release(destID uuid.UUID) {
    s.mu.RLock()
    sem, exists := s.sems[destID]
    s.mu.RUnlock()

    if exists {
        <-sem.ch
    }
}
```

### Semaphore Behavior

| Scenario | Behavior |
|----------|----------|
| Acquire succeeds | Event proceeds to delivery |
| Acquire fails (at limit) | Event rescheduled +100ms |
| Unknown destination | Acquire succeeds (fallback) |

> **Note:** Semaphores are initialized at startup. To change `max_in_flight` limits, restart Beacon with updated configuration.

---

## Timing Parameters

| Parameter | Value | Configurable | Description |
|-----------|-------|--------------|-------------|
| Poll interval | 100ms | Yes (`BEACON_POLL_INTERVAL`) | How often to check for pending events |
| Batch size | 100 | Yes (`BEACON_BATCH_SIZE`) | Max events claimed per poll |
| Worker count | 10 | Yes (`BEACON_WORKER_COUNT`) | Concurrent delivery goroutines |
| Heartbeat interval | 5s | **No** | Fixed for consistency across fleet |
| Heartbeat timeout | 30s | **No** | 6× heartbeat interval, fixed |
| Reaper interval | 10s | **No** | Fixed, checks for stale workers |
| Semaphore retry delay | 100ms | **No** | Delay when destination at capacity |
| Worker pool full delay | 50ms | **No** | Delay when workChan buffer is full |

---

## Graceful Shutdown

```go
func (d *Dispatcher) Start(ctx context.Context) error {
    // ... setup ...

    // Wait for context cancellation
    <-ctx.Done()

    // Stop accepting new work
    close(d.workChan)

    // Wait for in-flight work to complete (with timeout)
    shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    d.wg.Wait()  // Wait for workers to drain

    // Unregister from heartbeat table
    d.unregisterWorker(shutdownCtx)

    return ctx.Err()
}
```

---

## Dependencies

- `internal/db` - Database connection pool
- `internal/outbox` - Event repository
- `internal/httpdeliver` - HTTP delivery client
- `internal/dispatcher/retry` - Backoff calculation (see [5_RETRY.md](5_RETRY.md))

---

## Testing

### Strategy

Use **testcontainers for PostgreSQL** and **httptest for mock HTTP endpoints**. The dispatcher integrates multiple components, so tests verify the full flow from claiming events to updating state based on HTTP responses.

### Test Helpers

```go
// internal/dispatcher/testhelpers_test.go

package dispatcher_test

import (
    "net/http"
    "net/http/httptest"
    "sync/atomic"
)

// MockWebhookServer creates a test HTTP server that records requests
type MockWebhookServer struct {
    *httptest.Server
    Requests     []*http.Request
    ResponseCode int
    ResponseBody string
    requestCount atomic.Int64
}

func NewMockWebhookServer() *MockWebhookServer {
    m := &MockWebhookServer{
        ResponseCode: 200,
        ResponseBody: `{"ok":true}`,
    }
    m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        m.requestCount.Add(1)
        m.Requests = append(m.Requests, r)
        w.WriteHeader(m.ResponseCode)
        w.Write([]byte(m.ResponseBody))
    }))
    return m
}

func (m *MockWebhookServer) RequestCount() int64 {
    return m.requestCount.Load()
}

// SetupDispatcherTest creates all dependencies for dispatcher testing
func SetupDispatcherTest(t *testing.T) (*db.Pool, *MockWebhookServer, func()) {
    t.Helper()

    pool, dbCleanup := db.SetupTestDB(t)
    server := NewMockWebhookServer()

    cleanup := func() {
        server.Close()
        dbCleanup()
    }

    return pool, server, cleanup
}
```

### Integration Tests

```go
// internal/dispatcher/dispatcher_test.go

func TestDispatcher_DeliverEvent(t *testing.T) {
    pool, server, cleanup := SetupDispatcherTest(t)
    defer cleanup()

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    // Setup destination pointing to mock server
    var destID, subID uuid.UUID
    pool.QueryRow(ctx, `
        INSERT INTO beacon.destinations (name, url, timeout_ms)
        VALUES ('test', $1, 5000) RETURNING id
    `, server.URL).Scan(&destID)

    pool.QueryRow(ctx, `
        INSERT INTO beacon.subscriptions (name, table_schema, table_name, operation, destination_id)
        VALUES ('test-sub', 'public', 'test', 'INSERT', $1) RETURNING id
    `, destID).Scan(&subID)

    // Insert event
    pool.Exec(ctx, `
        INSERT INTO beacon.outbox_events
            (subscription_id, table_schema, table_name, operation, pk, payload, state)
        VALUES ($1, 'public', 'test', 'INSERT', '{"id":1}', '{"data":"test"}', 'pending')
    `, subID)

    // Create and start dispatcher
    repo := outbox.New(pool)
    client := httpdeliver.NewClient(nil)
    d := dispatcher.New(pool, repo, client, dispatcher.Config{
        PollInterval: 50 * time.Millisecond,
        BatchSize:    10,
        WorkerCount:  2,
    })

    // Run dispatcher briefly
    go d.Start(ctx)
    time.Sleep(200 * time.Millisecond)
    cancel()

    // Verify event delivered
    assert.Equal(t, int64(1), server.RequestCount())

    var state string
    pool.QueryRow(context.Background(), `
        SELECT state FROM beacon.outbox_events LIMIT 1
    `).Scan(&state)
    assert.Equal(t, "delivered", state)
}

func TestDispatcher_RetryOnFailure(t *testing.T) {
    pool, server, cleanup := SetupDispatcherTest(t)
    defer cleanup()

    // Server returns 503
    server.ResponseCode = 503

    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    var destID, subID uuid.UUID
    pool.QueryRow(ctx, `
        INSERT INTO beacon.destinations (name, url) VALUES ('test', $1) RETURNING id
    `, server.URL).Scan(&destID)

    pool.QueryRow(ctx, `
        INSERT INTO beacon.subscriptions (name, table_schema, table_name, operation, destination_id)
        VALUES ('test-sub', 'public', 'test', 'INSERT', $1) RETURNING id
    `, destID).Scan(&subID)

    pool.Exec(ctx, `
        INSERT INTO beacon.outbox_events
            (subscription_id, table_schema, table_name, operation, pk, payload, state)
        VALUES ($1, 'public', 'test', 'INSERT', '{"id":1}', '{}', 'pending')
    `, subID)

    repo := outbox.New(pool)
    client := httpdeliver.NewClient(nil)
    d := dispatcher.New(pool, repo, client, dispatcher.Config{
        PollInterval: 50 * time.Millisecond,
        BatchSize:    10,
        WorkerCount:  2,
    })

    go d.Start(ctx)
    time.Sleep(200 * time.Millisecond)
    cancel()

    // Verify event rescheduled (not delivered, not dead)
    var state string
    var attempts int
    var lastError *string
    pool.QueryRow(context.Background(), `
        SELECT state, attempts, last_error FROM beacon.outbox_events LIMIT 1
    `).Scan(&state, &attempts, &lastError)

    assert.Equal(t, "pending", state)
    assert.Equal(t, 1, attempts)
    assert.NotNil(t, lastError)
}

func TestDispatcher_MovesToDLQAfterMaxAttempts(t *testing.T) {
    pool, server, cleanup := SetupDispatcherTest(t)
    defer cleanup()

    server.ResponseCode = 500

    ctx := context.Background()

    var destID, subID uuid.UUID
    pool.QueryRow(ctx, `
        INSERT INTO beacon.destinations (name, url) VALUES ('test', $1) RETURNING id
    `, server.URL).Scan(&destID)

    pool.QueryRow(ctx, `
        INSERT INTO beacon.subscriptions (name, table_schema, table_name, operation, destination_id)
        VALUES ('test-sub', 'public', 'test', 'INSERT', $1) RETURNING id
    `, destID).Scan(&subID)

    // Insert event already at max attempts - 1
    pool.Exec(ctx, `
        INSERT INTO beacon.outbox_events
            (subscription_id, table_schema, table_name, operation, pk, payload, state, attempts)
        VALUES ($1, 'public', 'test', 'INSERT', '{"id":1}', '{}', 'pending', 9)
    `, subID)

    repo := outbox.New(pool)
    client := httpdeliver.NewClient(nil)

    runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
    d := dispatcher.New(pool, repo, client, dispatcher.Config{
        PollInterval: 50 * time.Millisecond,
        BatchSize:    10,
        WorkerCount:  2,
    })

    go d.Start(runCtx)
    time.Sleep(200 * time.Millisecond)
    cancel()

    // Verify moved to DLQ
    var state string
    pool.QueryRow(ctx, `SELECT state FROM beacon.outbox_events LIMIT 1`).Scan(&state)
    assert.Equal(t, "dead", state)

    var dlqCount int
    pool.QueryRow(ctx, `SELECT COUNT(*) FROM beacon.dead_letters`).Scan(&dlqCount)
    assert.Equal(t, 1, dlqCount)
}
```

### Semaphore Tests

```go
// internal/dispatcher/semaphore_test.go

func TestSemaphores_Acquire(t *testing.T) {
    dests := []dispatcher.Destination{{ID: uuid.New(), MaxInFlight: 2}}
    sems := dispatcher.NewSemaphores(dests)
    destID := dests[0].ID

    // Acquire 2 should succeed
    assert.True(t, sems.Acquire(destID))
    assert.True(t, sems.Acquire(destID))

    // 3rd should fail
    assert.False(t, sems.Acquire(destID))

    // Release one
    sems.Release(destID)

    // Now can acquire again
    assert.True(t, sems.Acquire(destID))
}

func TestSemaphores_UnknownDestination(t *testing.T) {
    sems := dispatcher.NewSemaphores(nil)
    destID := uuid.New()

    // Unknown destination - should allow (fallback behavior)
    assert.True(t, sems.Acquire(destID))
}

func TestSemaphores_Concurrent(t *testing.T) {
    dests := []dispatcher.Destination{{ID: uuid.New(), MaxInFlight: 10}}
    sems := dispatcher.NewSemaphores(dests)
    destID := dests[0].ID

    var acquired atomic.Int64
    var wg sync.WaitGroup

    // 100 goroutines try to acquire
    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func() {
            defer wg.Done()
            if sems.Acquire(destID) {
                acquired.Add(1)
                time.Sleep(10 * time.Millisecond)
                sems.Release(destID)
            }
        }()
    }

    wg.Wait()

    // All should eventually succeed since we release
    assert.GreaterOrEqual(t, acquired.Load(), int64(10))
}
```

### Heartbeat and Reaper Tests

```go
// internal/dispatcher/reaper_test.go

func TestReaper_ReclaimsStaleEvents(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx := context.Background()

    // Setup fixtures
    var destID, subID uuid.UUID
    pool.QueryRow(ctx, `
        INSERT INTO beacon.destinations (name, url) VALUES ('test', 'https://example.com') RETURNING id
    `).Scan(&destID)

    pool.QueryRow(ctx, `
        INSERT INTO beacon.subscriptions (name, table_schema, table_name, operation, destination_id)
        VALUES ('test-sub', 'public', 'test', 'INSERT', $1) RETURNING id
    `, destID).Scan(&subID)

    // Insert event locked by a "dead" worker
    pool.Exec(ctx, `
        INSERT INTO beacon.outbox_events
            (subscription_id, table_schema, table_name, operation, pk, payload, state, locked_by, locked_at)
        VALUES ($1, 'public', 'test', 'INSERT', '{"id":1}', '{}', 'delivering', 'dead-worker', now() - interval '1 minute')
    `, subID)

    // Insert stale heartbeat
    pool.Exec(ctx, `
        INSERT INTO beacon.worker_heartbeats (worker_id, last_heartbeat)
        VALUES ('dead-worker', now() - interval '1 minute')
    `)

    // Run reaper
    repo := outbox.New(pool)
    client := httpdeliver.NewClient(nil)
    d := dispatcher.New(pool, repo, client, dispatcher.Config{
        PollInterval: 100 * time.Millisecond,
        BatchSize:    10,
        WorkerCount:  2,
    })

    // Manually invoke reaper (in real code this runs in a loop)
    d.Reap(ctx)

    // Verify event reclaimed
    var state string
    var lockedBy *string
    pool.QueryRow(ctx, `SELECT state, locked_by FROM beacon.outbox_events LIMIT 1`).Scan(&state, &lockedBy)

    assert.Equal(t, "pending", state)
    assert.Nil(t, lockedBy)
}

func TestHeartbeat_RegistersWorker(t *testing.T) {
    pool, cleanup := db.SetupTestDB(t)
    defer cleanup()

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()

    repo := outbox.New(pool)
    client := httpdeliver.NewClient(nil)
    d := dispatcher.New(pool, repo, client, dispatcher.Config{
        PollInterval: 100 * time.Millisecond,
        BatchSize:    10,
        WorkerCount:  2,
    })

    // Start briefly to register
    go d.Start(ctx)
    time.Sleep(100 * time.Millisecond)

    // Verify worker registered
    var count int
    pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM beacon.worker_heartbeats`).Scan(&count)
    assert.Equal(t, 1, count)

    cancel()
    time.Sleep(100 * time.Millisecond)

    // After shutdown, worker should unregister
    pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM beacon.worker_heartbeats`).Scan(&count)
    assert.Equal(t, 0, count)
}
```

### Running Tests

```bash
# Run dispatcher tests
go test ./internal/dispatcher/... -v

# Run with race detector
go test ./internal/dispatcher/... -race

# Run specific test
go test ./internal/dispatcher/... -run TestDispatcher_RetryOnFailure -v
```

---

## Goroutine Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         Dispatcher                               │
│                                                                  │
│  ┌──────────────┐                                                │
│  │  Poll Loop   │──── claims events ────▶ workChan               │
│  │  (goroutine) │                            │                   │
│  └──────────────┘                            │                   │
│                                              │                   │
│  ┌──────────────┐                            ▼                   │
│  │  Heartbeat   │              ┌───────────────────────────────┐ │
│  │  (goroutine) │              │         Worker Pool           │ │
│  └──────────────┘              │  ┌────────┐ ┌────────┐        │ │
│                                │  │Worker 1│ │Worker 2│ ...    │ │
│  ┌──────────────┐              │  └────────┘ └────────┘        │ │
│  │    Reaper    │              └───────────────────────────────┘ │
│  │  (goroutine) │                            │                   │
│  └──────────────┘                            │                   │
│                                              ▼                   │
│                                    ┌──────────────────┐          │
│                                    │  HTTP Delivery   │          │
│                                    │  (per worker)    │          │
│                                    └──────────────────┘          │
└─────────────────────────────────────────────────────────────────┘
```

---

## Usage Example

```go
pool, _ := db.New(ctx, os.Getenv("DATABASE_URL"))
repo := outbox.New(pool)
client := httpdeliver.NewClient()

dispatcher := dispatcher.New(pool, repo, client, dispatcher.Config{
    PollInterval: 100 * time.Millisecond,
    BatchSize:    100,
    WorkerCount:  10,
})

// Blocks until context cancelled
if err := dispatcher.Start(ctx); err != nil && err != context.Canceled {
    log.Fatal(err)
}
```
