# 8. Control Plane Specification

## Purpose

The control plane provides an HTTP API for configuration management, health checks, and operational endpoints. It implements YAML-based declarative config with diff/apply semantics and handles soft delete with async drain for subscriptions.

---

## Exposed API

### Package: `internal/controlplane/api`

```go
// Server is the control plane HTTP server
type Server struct {
    // contains unexported fields
}

// NewServer creates a control plane server
func NewServer(
    pool *db.Pool,
    applySvc *service.ApplyService,
    addr string,
) *Server

// Start runs the HTTP server (blocks until context cancelled)
func (s *Server) Start(ctx context.Context) error
```

### Package: `internal/controlplane/service`

```go
// ApplyService handles YAML config application
type ApplyService struct {
    // contains unexported fields
}

// NewApplyService creates an ApplyService
func NewApplyService(
    pool *db.Pool,
    installer *capture.Installer,
) *ApplyService

// Apply applies a configuration, returning changes made
func (s *ApplyService) Apply(ctx context.Context, cfg *config.BeaconConfig) (*ApplyResult, error)

// DryRun computes what Apply would do without making changes
func (s *ApplyService) DryRun(ctx context.Context, cfg *config.BeaconConfig) (*ApplyResult, error)

// Export returns the current configuration as BeaconConfig
func (s *ApplyService) Export(ctx context.Context) (*config.BeaconConfig, error)
```

```go
// ApplyResult describes changes made by Apply
type ApplyResult struct {
    Destinations  ChangeSet `json:"destinations"`
    Subscriptions ChangeSet `json:"subscriptions"`
    Triggers      ChangeSet `json:"triggers"`
}

// ChangeSet tracks what was created, updated, deleted
type ChangeSet struct {
    Created []string `json:"created,omitempty"`
    Updated []string `json:"updated,omitempty"`
    Deleted []string `json:"deleted,omitempty"`
}
```

```go
// DrainService handles async subscription draining
type DrainService struct {
    // contains unexported fields
}

// NewDrainService creates a DrainService
func NewDrainService(pool *db.Pool) *DrainService

// StartDrain marks a subscription as draining
func (s *DrainService) StartDrain(ctx context.Context, subID uuid.UUID) error

// CheckDrainComplete checks if a draining subscription can be deleted
func (s *DrainService) CheckDrainComplete(ctx context.Context, subID uuid.UUID) (bool, error)

// RunDrainLoop periodically checks and finalizes draining subscriptions
func (s *DrainService) RunDrainLoop(ctx context.Context) error
```

---

## HTTP Endpoints

### Configuration Endpoints

#### `POST /v1/apply`

Apply YAML configuration. Creates, updates, and soft-deletes resources to match.

**Request:**
```http
POST /v1/apply HTTP/1.1
Content-Type: application/x-yaml
Authorization: Bearer your-secret-token

version: 1
destinations:
  - name: webhook
    url: https://example.com/hook
subscriptions:
  - name: users-insert
    table: users
    operation: INSERT
    destination: webhook
```

**Query Parameters:**
- `dry_run=true` - Preview changes without applying

**Response (200 OK):**
```json
{
  "destinations": {
    "created": ["webhook"],
    "updated": [],
    "deleted": []
  },
  "subscriptions": {
    "created": ["users-insert"],
    "updated": [],
    "deleted": []
  },
  "triggers": {
    "created": ["public.users"],
    "updated": [],
    "deleted": []
  }
}
```

**Errors:**
- `400 Bad Request` - Invalid YAML or validation error
- `409 Conflict` - Concurrent modification detected

#### `GET /v1/config`

Export current configuration as YAML.

**Response (200 OK):**
```yaml
version: 1
destinations:
  - name: webhook
    url: https://example.com/hook
    method: POST
    timeout_ms: 5000
    max_in_flight: 50
subscriptions:
  - name: users-insert
    table: public.users
    operation: INSERT
    destination: webhook
```

#### `POST /v1/validate`

Validate YAML without applying.

**Request:** Same as `/v1/apply`

**Response (200 OK):**
```json
{
  "valid": true
}
```

**Response (400 Bad Request):**
```json
{
  "valid": false,
  "errors": [
    "subscriptions[0]: unknown destination \"missing\""
  ]
}
```

### Read-Only Endpoints

#### `GET /v1/destinations`

List all destinations.

**Response:**
```json
{
  "destinations": [
    {
      "id": "550e8400-...",
      "name": "webhook",
      "url": "https://example.com/hook",
      "method": "POST",
      "timeout_ms": 5000,
      "max_in_flight": 50,
      "created_at": "2024-01-15T10:00:00Z"
    }
  ]
}
```

#### `GET /v1/destinations/:id`

Get destination details.

#### `GET /v1/subscriptions`

List all subscriptions (excludes soft-deleted).

**Query Parameters:**
- `include_draining=true` - Include subscriptions being drained

**Response:**
```json
{
  "subscriptions": [
    {
      "id": "550e8400-...",
      "name": "users-insert",
      "enabled": true,
      "draining": false,
      "table_schema": "public",
      "table_name": "users",
      "operation": "INSERT",
      "destination_id": "...",
      "destination_name": "webhook",
      "trigger_columns": null,
      "payload_columns": null,
      "created_at": "2024-01-15T10:00:00Z"
    }
  ]
}
```

#### `GET /v1/subscriptions/:id`

Get subscription details including event stats.

### Operations Endpoints

#### `POST /v1/subscriptions/:id/replay`

Replay dead-lettered events for a subscription.

**Request:**
```json
{
  "limit": 100
}
```

**Query Parameters:**
- `force=true` - Replay events even if `replay_count >= 3`

**Response:**
```json
{
  "replayed": 42,
  "skipped": 3,
  "skipped_reason": "replay_count >= 3 (use force=true to override)"
}
```

**Replay Logic:**

```go
func (s *ApplyService) ReplayDeadLetters(ctx context.Context, subID uuid.UUID, limit int, force bool) (*ReplayResult, error) {
    return s.pool.WithTx(ctx, func(tx pgx.Tx) error {
        // Find eligible dead letters
        query := `
            SELECT dl.event_id, dl.snapshot, dl.replay_count
            FROM beacon.dead_letters dl
            JOIN beacon.outbox_events e ON e.id = dl.event_id
            WHERE e.subscription_id = $1
        `
        if !force {
            query += " AND dl.replay_count < 3"
        }
        query += " LIMIT $2"

        // For each event:
        // 1. Increment replay_count
        // 2. Reset outbox_events state to 'pending'
        // 3. Clear lock fields, set attempts = 0
    })
}
```

#### `GET /v1/health`

Health check.

**Response (200 OK):**
```json
{
  "status": "healthy",
  "database": "connected",
  "workers": 10
}
```

**Response (503 Service Unavailable):**
```json
{
  "status": "unhealthy",
  "database": "disconnected",
  "error": "connection refused"
}
```

#### `GET /v1/metrics`

Prometheus metrics (see [9_OBSERVABILITY.md](9_OBSERVABILITY.md)).

---

## Apply Logic

### Diff Algorithm

```go
func (s *ApplyService) Apply(ctx context.Context, cfg *config.BeaconConfig) (*ApplyResult, error) {
    return s.pool.WithTx(ctx, func(tx pgx.Tx) error {
        // 1. Load current state
        currentDests := s.loadDestinations(ctx, tx)
        currentSubs := s.loadSubscriptions(ctx, tx)

        // 2. Compute destination changes
        destChanges := s.diffDestinations(currentDests, cfg.Destinations)

        // 3. Apply destination changes
        destIDMap := s.applyDestinations(ctx, tx, destChanges)

        // 4. Compute subscription changes
        subChanges := s.diffSubscriptions(currentSubs, cfg.Subscriptions, destIDMap)

        // 5. Apply subscription changes (soft delete triggers drain)
        s.applySubscriptions(ctx, tx, subChanges)

        // 6. Reconcile triggers
        triggerChanges := s.reconcileTriggers(ctx, tx)

        return &ApplyResult{
            Destinations:  destChanges.ToChangeSet(),
            Subscriptions: subChanges.ToChangeSet(),
            Triggers:      triggerChanges.ToChangeSet(),
        }
    })
}
```

### Change Detection

Resources are matched by `name`. For each resource type:

| Current State | Config State | Action |
|--------------|--------------|--------|
| Not exists | In config | Create |
| Exists | In config (same) | No-op |
| Exists | In config (different) | Update |
| Exists | Not in config | Delete (soft) |

### Destination Diff

```go
type DestinationDiff struct {
    Create []config.DestinationConfig
    Update []DestinationUpdate
    Delete []uuid.UUID
}

type DestinationUpdate struct {
    ID     uuid.UUID
    Old    Destination
    New    config.DestinationConfig
    Fields []string  // Changed fields
}
```

### Subscription Diff

```go
type SubscriptionDiff struct {
    Create []config.SubscriptionConfig
    Update []SubscriptionUpdate
    Delete []uuid.UUID  // Triggers drain, not immediate delete
}
```

---

## Soft Delete & Drain

### Drain Flow

```
┌──────────────────────────────────────────────────────────────────┐
│              Subscription Removal from Config                     │
└───────────────────────────────┬──────────────────────────────────┘
                                │
                                ▼
                    ┌───────────────────────┐
                    │ Set draining = true   │
                    │ (stops new captures)  │
                    └───────────┬───────────┘
                                │
                                ▼
                    ┌───────────────────────┐
                    │ Return success to     │
                    │ API immediately       │
                    └───────────┬───────────┘
                                │
                                │ (async)
                                ▼
                    ┌───────────────────────┐
                    │ Drain loop checks     │
                    │ pending event count   │
                    └───────────┬───────────┘
                                │
               ┌────────────────┴────────────────┐
               │                                 │
               ▼                                 ▼
    ┌─────────────────────┐           ┌─────────────────────┐
    │ Events pending?     │           │ No events pending   │
    │ → Wait, check again │           │ → Set deleted_at    │
    └─────────────────────┘           └─────────────────────┘
                                                │
                                                ▼
                                   ┌───────────────────────┐
                                   │ Check if trigger      │
                                   │ still needed          │
                                   └───────────┬───────────┘
                                               │
                              ┌────────────────┴────────────────┐
                              │                                 │
                              ▼                                 ▼
                   ┌──────────────────┐              ┌──────────────────┐
                   │ Other subs exist │              │ No other subs    │
                   │ → Keep trigger   │              │ → Drop trigger   │
                   └──────────────────┘              └──────────────────┘
```

### Drain Loop Implementation

```go
const DrainCheckInterval = 10 * time.Second

func (s *DrainService) RunDrainLoop(ctx context.Context) error {
    ticker := time.NewTicker(DrainCheckInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-ticker.C:
            s.checkDrainingSubscriptions(ctx)
        }
    }
}

func (s *DrainService) checkDrainingSubscriptions(ctx context.Context) {
    // Find draining subscriptions
    rows, _ := s.pool.Query(ctx, `
        SELECT id FROM beacon.subscriptions
        WHERE draining = true AND deleted_at IS NULL
    `)

    for rows.Next() {
        var subID uuid.UUID
        rows.Scan(&subID)

        // Check if all events processed
        var pending int64
        s.pool.QueryRow(ctx, `
            SELECT COUNT(*) FROM beacon.outbox_events
            WHERE subscription_id = $1
              AND state IN ('pending', 'delivering')
        `, subID).Scan(&pending)

        if pending == 0 {
            // Finalize deletion
            s.pool.Exec(ctx, `
                UPDATE beacon.subscriptions
                SET deleted_at = now()
                WHERE id = $1
            `, subID)

            s.logger.Info("subscription drain complete", "id", subID)
        }
    }
}
```

---

## Trigger Reconciliation

After applying subscription changes, reconcile triggers:

```go
func (s *ApplyService) reconcileTriggers(ctx context.Context, tx pgx.Tx) TriggerChanges {
    // Tables that need triggers (have active subscriptions)
    tablesNeeded := make(map[TableRef]bool)
    rows, _ := tx.Query(ctx, `
        SELECT DISTINCT table_schema, table_name
        FROM beacon.subscriptions
        WHERE deleted_at IS NULL AND enabled = true
    `)
    for rows.Next() {
        var schema, name string
        rows.Scan(&schema, &name)
        tablesNeeded[TableRef{schema, name}] = true
    }

    // Tables that have triggers
    tablesWithTriggers := s.installer.ListTriggers(ctx)

    var changes TriggerChanges

    // Install missing
    for table := range tablesNeeded {
        if !tablesWithTriggers[table] {
            s.installer.InstallTrigger(ctx, table.Schema, table.Name)
            changes.Created = append(changes.Created, table.String())
        }
    }

    // Remove orphans
    for table := range tablesWithTriggers {
        if !tablesNeeded[table] {
            s.installer.UninstallTrigger(ctx, table.Schema, table.Name)
            changes.Deleted = append(changes.Deleted, table.String())
        }
    }

    return changes
}
```

---

## Error Handling

### Validation Errors

```go
type ValidationError struct {
    Field   string `json:"field"`
    Message string `json:"message"`
}

type ValidationErrors struct {
    Errors []ValidationError `json:"errors"`
}

func (e ValidationErrors) Error() string {
    // Format as multi-line for logging
}
```

### Conflict Detection

Apply uses `SELECT ... FOR UPDATE` to prevent concurrent modifications:

```go
// At start of Apply transaction
_, err := tx.Exec(ctx, `
    SELECT 1 FROM beacon.destinations FOR UPDATE
`)
```

If another apply is in progress, the second will block and then see the updated state.

---

## Dependencies

- `internal/db` - Database connection
- `internal/config` - YAML parsing
- `internal/capture` - Trigger installation
- `net/http` - HTTP server (stdlib)

---

## Usage Example

```go
pool, _ := db.New(ctx, databaseURL)
installer := capture.New(pool)
applySvc := service.NewApplyService(pool, installer)
drainSvc := service.NewDrainService(pool)

server := api.NewServer(pool, applySvc, ":8080")

// Run drain loop in background
go drainSvc.RunDrainLoop(ctx)

// Start HTTP server
server.Start(ctx)
```

---

## Authentication

All control plane endpoints (except `/v1/health` and `/v1/metrics`) require authentication via Bearer token.

### Configuration

Set the `BEACON_CONTROLPLANE_SECRET` environment variable:

```bash
export BEACON_CONTROLPLANE_SECRET="your-secure-random-token"
```

### Request Format

```http
GET /v1/config HTTP/1.1
Authorization: Bearer your-secure-random-token
```

### Middleware Implementation

```go
func AuthMiddleware(secret string) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            // Skip auth for health and metrics
            if r.URL.Path == "/v1/health" || r.URL.Path == "/v1/metrics" {
                next.ServeHTTP(w, r)
                return
            }

            auth := r.Header.Get("Authorization")
            if auth == "" {
                http.Error(w, `{"error": "missing authorization header"}`, http.StatusUnauthorized)
                return
            }

            const prefix = "Bearer "
            if !strings.HasPrefix(auth, prefix) {
                http.Error(w, `{"error": "invalid authorization format"}`, http.StatusUnauthorized)
                return
            }

            token := strings.TrimPrefix(auth, prefix)
            if subtle.ConstantTimeCompare([]byte(token), []byte(secret)) != 1 {
                http.Error(w, `{"error": "invalid token"}`, http.StatusUnauthorized)
                return
            }

            next.ServeHTTP(w, r)
        })
    }
}
```

### Error Responses

| Status | Condition |
|--------|-----------|
| `401 Unauthorized` | Missing or invalid `Authorization` header |
| `401 Unauthorized` | Token does not match `BEACON_CONTROLPLANE_SECRET` |

---

## Security Considerations

1. **Authentication:** Bearer token required for all mutating endpoints
2. **Input validation:** All YAML validated before apply
3. **SQL injection:** All queries parameterized
4. **Constant-time comparison:** Token comparison uses `subtle.ConstantTimeCompare` to prevent timing attacks
5. **Rate limiting:** Consider adding for production

---

## Testing

### Strategy

Use **testcontainers for PostgreSQL** and **httptest** for the HTTP server. Tests verify the full API flow: parsing YAML, diffing configuration, applying changes to the database, and returning correct responses.

### Test Helpers

```go
// internal/controlplane/testhelpers_test.go

package controlplane_test

import (
    "context"
    "net/http/httptest"
    "testing"

    "beacon/internal/capture"
    "beacon/internal/controlplane/api"
    "beacon/internal/controlplane/service"
    "beacon/internal/db"
)

// SetupTestServer creates a test control plane server backed by real PostgreSQL
func SetupTestServer(t *testing.T) (*httptest.Server, *db.Pool, func()) {
    t.Helper()

    pool, dbCleanup := db.SetupTestDB(t)

    installer := capture.New(pool)
    applySvc := service.NewApplyService(pool, installer)
    server := api.NewServer(pool, applySvc, ":0")

    testServer := httptest.NewServer(server.Handler())

    cleanup := func() {
        testServer.Close()
        dbCleanup()
    }

    return testServer, pool, cleanup
}

// AuthHeader returns the authorization header for tests
func AuthHeader() map[string]string {
    return map[string]string{
        "Authorization": "Bearer test-secret",
        "Content-Type":  "application/x-yaml",
    }
}
```

### API Tests

```go
// internal/controlplane/api/api_test.go

func TestApply_CreateDestination(t *testing.T) {
    server, pool, cleanup := SetupTestServer(t)
    defer cleanup()

    yaml := `
version: 1
destinations:
  - name: webhook
    url: https://example.com/hook
subscriptions: []
`
    req, _ := http.NewRequest("POST", server.URL+"/v1/apply", strings.NewReader(yaml))
    for k, v := range AuthHeader() {
        req.Header.Set(k, v)
    }

    resp, err := http.DefaultClient.Do(req)
    require.NoError(t, err)
    defer resp.Body.Close()

    assert.Equal(t, 200, resp.StatusCode)

    var result map[string]any
    json.NewDecoder(resp.Body).Decode(&result)

    dests := result["destinations"].(map[string]any)
    created := dests["created"].([]any)
    assert.Len(t, created, 1)
    assert.Equal(t, "webhook", created[0])

    // Verify in database
    var count int
    pool.QueryRow(context.Background(),
        `SELECT COUNT(*) FROM beacon.destinations WHERE name = 'webhook'`).Scan(&count)
    assert.Equal(t, 1, count)
}

func TestApply_UpdateDestination(t *testing.T) {
    server, pool, cleanup := SetupTestServer(t)
    defer cleanup()

    ctx := context.Background()

    // Create initial
    pool.Exec(ctx, `
        INSERT INTO beacon.destinations (name, url, timeout_ms)
        VALUES ('webhook', 'https://old.example.com', 5000)
    `)

    yaml := `
version: 1
destinations:
  - name: webhook
    url: https://new.example.com
    timeout_ms: 10000
subscriptions: []
`
    req, _ := http.NewRequest("POST", server.URL+"/v1/apply", strings.NewReader(yaml))
    for k, v := range AuthHeader() {
        req.Header.Set(k, v)
    }

    resp, _ := http.DefaultClient.Do(req)
    assert.Equal(t, 200, resp.StatusCode)

    // Verify updated
    var url string
    var timeout int
    pool.QueryRow(ctx, `SELECT url, timeout_ms FROM beacon.destinations WHERE name = 'webhook'`).
        Scan(&url, &timeout)
    assert.Equal(t, "https://new.example.com", url)
    assert.Equal(t, 10000, timeout)
}

func TestApply_DeleteDestination(t *testing.T) {
    server, pool, cleanup := SetupTestServer(t)
    defer cleanup()

    ctx := context.Background()

    // Create initial
    pool.Exec(ctx, `
        INSERT INTO beacon.destinations (name, url)
        VALUES ('to-delete', 'https://example.com')
    `)

    // Apply empty config
    yaml := `
version: 1
destinations: []
subscriptions: []
`
    req, _ := http.NewRequest("POST", server.URL+"/v1/apply", strings.NewReader(yaml))
    for k, v := range AuthHeader() {
        req.Header.Set(k, v)
    }

    resp, _ := http.DefaultClient.Do(req)
    assert.Equal(t, 200, resp.StatusCode)

    // Verify deleted
    var count int
    pool.QueryRow(ctx, `SELECT COUNT(*) FROM beacon.destinations WHERE name = 'to-delete'`).Scan(&count)
    assert.Equal(t, 0, count)
}

func TestApply_DryRun(t *testing.T) {
    server, pool, cleanup := SetupTestServer(t)
    defer cleanup()

    yaml := `
version: 1
destinations:
  - name: webhook
    url: https://example.com
subscriptions: []
`
    req, _ := http.NewRequest("POST", server.URL+"/v1/apply?dry_run=true", strings.NewReader(yaml))
    for k, v := range AuthHeader() {
        req.Header.Set(k, v)
    }

    resp, _ := http.DefaultClient.Do(req)
    assert.Equal(t, 200, resp.StatusCode)

    // Verify NOT in database (dry run)
    var count int
    pool.QueryRow(context.Background(),
        `SELECT COUNT(*) FROM beacon.destinations WHERE name = 'webhook'`).Scan(&count)
    assert.Equal(t, 0, count, "dry_run should not modify database")
}

func TestApply_ValidationError(t *testing.T) {
    server, _, cleanup := SetupTestServer(t)
    defer cleanup()

    yaml := `
version: 1
destinations:
  - name: invalid
    url: not-a-url
subscriptions: []
`
    req, _ := http.NewRequest("POST", server.URL+"/v1/apply", strings.NewReader(yaml))
    for k, v := range AuthHeader() {
        req.Header.Set(k, v)
    }

    resp, _ := http.DefaultClient.Do(req)
    assert.Equal(t, 400, resp.StatusCode)
}

func TestApply_Unauthorized(t *testing.T) {
    server, _, cleanup := SetupTestServer(t)
    defer cleanup()

    yaml := `version: 1`
    req, _ := http.NewRequest("POST", server.URL+"/v1/apply", strings.NewReader(yaml))
    req.Header.Set("Content-Type", "application/x-yaml")
    // No Authorization header

    resp, _ := http.DefaultClient.Do(req)
    assert.Equal(t, 401, resp.StatusCode)
}

func TestApply_InvalidToken(t *testing.T) {
    server, _, cleanup := SetupTestServer(t)
    defer cleanup()

    yaml := `version: 1`
    req, _ := http.NewRequest("POST", server.URL+"/v1/apply", strings.NewReader(yaml))
    req.Header.Set("Content-Type", "application/x-yaml")
    req.Header.Set("Authorization", "Bearer wrong-token")

    resp, _ := http.DefaultClient.Do(req)
    assert.Equal(t, 401, resp.StatusCode)
}
```

### Config Export Tests

```go
func TestGetConfig(t *testing.T) {
    server, pool, cleanup := SetupTestServer(t)
    defer cleanup()

    ctx := context.Background()

    // Setup test data
    var destID uuid.UUID
    pool.QueryRow(ctx, `
        INSERT INTO beacon.destinations (name, url, timeout_ms)
        VALUES ('webhook', 'https://example.com', 5000)
        RETURNING id
    `).Scan(&destID)

    pool.Exec(ctx, `
        INSERT INTO beacon.subscriptions (name, table_schema, table_name, operation, destination_id)
        VALUES ('users-insert', 'public', 'users', 'INSERT', $1)
    `, destID)

    req, _ := http.NewRequest("GET", server.URL+"/v1/config", nil)
    req.Header.Set("Authorization", "Bearer test-secret")

    resp, _ := http.DefaultClient.Do(req)
    assert.Equal(t, 200, resp.StatusCode)

    body, _ := io.ReadAll(resp.Body)
    assert.Contains(t, string(body), "webhook")
    assert.Contains(t, string(body), "users-insert")
}
```

### Health Check Tests

```go
func TestHealth_Healthy(t *testing.T) {
    server, _, cleanup := SetupTestServer(t)
    defer cleanup()

    resp, _ := http.Get(server.URL + "/v1/health")
    assert.Equal(t, 200, resp.StatusCode)

    var health map[string]any
    json.NewDecoder(resp.Body).Decode(&health)

    assert.Equal(t, "healthy", health["status"])
    assert.Equal(t, "connected", health["database"])
}

func TestHealth_NoAuth(t *testing.T) {
    server, _, cleanup := SetupTestServer(t)
    defer cleanup()

    // Health endpoint should work without auth
    resp, _ := http.Get(server.URL + "/v1/health")
    assert.Equal(t, 200, resp.StatusCode)
}
```

### Drain Tests

```go
func TestDrain_SoftDelete(t *testing.T) {
    server, pool, cleanup := SetupTestServer(t)
    defer cleanup()

    ctx := context.Background()

    // Create destination and subscription
    var destID, subID uuid.UUID
    pool.QueryRow(ctx, `
        INSERT INTO beacon.destinations (name, url)
        VALUES ('webhook', 'https://example.com')
        RETURNING id
    `).Scan(&destID)

    pool.QueryRow(ctx, `
        INSERT INTO beacon.subscriptions (name, table_schema, table_name, operation, destination_id)
        VALUES ('to-drain', 'public', 'users', 'INSERT', $1)
        RETURNING id
    `, destID).Scan(&subID)

    // Insert a pending event for this subscription
    pool.Exec(ctx, `
        INSERT INTO beacon.outbox_events (subscription_id, table_schema, table_name, operation, pk, payload, state)
        VALUES ($1, 'public', 'users', 'INSERT', '{"id":1}', '{}', 'pending')
    `, subID)

    // Apply config without the subscription (triggers drain)
    yaml := `
version: 1
destinations:
  - name: webhook
    url: https://example.com
subscriptions: []
`
    req, _ := http.NewRequest("POST", server.URL+"/v1/apply", strings.NewReader(yaml))
    for k, v := range AuthHeader() {
        req.Header.Set(k, v)
    }

    resp, _ := http.DefaultClient.Do(req)
    assert.Equal(t, 200, resp.StatusCode)

    // Verify subscription is draining (not deleted yet due to pending events)
    var draining bool
    var deletedAt *time.Time
    pool.QueryRow(ctx, `
        SELECT draining, deleted_at FROM beacon.subscriptions WHERE id = $1
    `, subID).Scan(&draining, &deletedAt)

    assert.True(t, draining, "subscription should be draining")
    assert.Nil(t, deletedAt, "subscription should not be deleted yet")
}
```

### Running Tests

```bash
# Run control plane tests
go test ./internal/controlplane/... -v

# Run with race detector
go test ./internal/controlplane/... -race
```
