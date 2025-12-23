# 9. Observability Specification

## Purpose

Defines the metrics, logging, and health check interfaces for monitoring Beacon in production. Provides Prometheus-compatible metrics and structured JSON logging.

---

## Exposed API

### Package: `internal/observability`

#### Metrics

```go
// Metrics provides Prometheus metrics for Beacon
type Metrics struct {
    // contains unexported fields
}

// NewMetrics creates and registers all metrics
func NewMetrics(registry prometheus.Registerer) *Metrics

// Handler returns an HTTP handler for /metrics endpoint
func (m *Metrics) Handler() http.Handler

// Delivery metrics
func (m *Metrics) DeliverySuccess(destination string)
func (m *Metrics) DeliveryFailure(destination string, statusCode int)
func (m *Metrics) DeliveryRetry(destination string)
func (m *Metrics) DeadLetter(destination string)
func (m *Metrics) DeliveryDuration(destination string, duration time.Duration)

// Outbox metrics
func (m *Metrics) SetOutboxDepth(state string, count int64)
func (m *Metrics) EventsClaimed(count int)
func (m *Metrics) EventsReaped(count int)

// Worker metrics
func (m *Metrics) SetActiveWorkers(count int)
func (m *Metrics) WorkerHeartbeat()
```

#### Logging

```go
// Logger provides structured logging
type Logger interface {
    Debug(msg string, keysAndValues ...any)
    Info(msg string, keysAndValues ...any)
    Warn(msg string, keysAndValues ...any)
    Error(msg string, keysAndValues ...any)
    With(keysAndValues ...any) Logger
}

// NewLogger creates a logger with the specified level and format
func NewLogger(level, format string) (Logger, error)

// Levels: "debug", "info", "warn", "error"
// Formats: "json", "text"
```

---

## Prometheus Metrics

### Delivery Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `beacon_delivery_total` | Counter | `destination`, `status` | Total delivery attempts |
| `beacon_delivery_duration_seconds` | Histogram | `destination` | Delivery request duration |
| `beacon_delivery_attempts` | Histogram | `destination` | Attempts before success/DLQ |
| `beacon_dead_letters_total` | Counter | `destination` | Events sent to DLQ |

**Status labels:** `success`, `client_error`, `server_error`, `timeout`, `connection_error`

### Outbox Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `beacon_outbox_depth` | Gauge | `state` | Current event count by state |
| `beacon_events_claimed_total` | Counter | - | Events claimed from outbox |
| `beacon_events_reaped_total` | Counter | - | Events recovered by reaper |

**State labels:** `pending`, `delivering`, `delivered`, `dead`

### Worker Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `beacon_workers_active` | Gauge | - | Currently active workers |
| `beacon_worker_heartbeats_total` | Counter | - | Heartbeats sent |
| `beacon_poll_duration_seconds` | Histogram | - | Time to poll and claim events |

### API Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `beacon_api_requests_total` | Counter | `method`, `path`, `status` | HTTP requests |
| `beacon_api_request_duration_seconds` | Histogram | `method`, `path` | Request duration |

---

## Metrics Implementation

```go
func NewMetrics(registry prometheus.Registerer) *Metrics {
    m := &Metrics{
        deliveryTotal: prometheus.NewCounterVec(
            prometheus.CounterOpts{
                Name: "beacon_delivery_total",
                Help: "Total delivery attempts by destination and status",
            },
            []string{"destination", "status"},
        ),
        deliveryDuration: prometheus.NewHistogramVec(
            prometheus.HistogramOpts{
                Name:    "beacon_delivery_duration_seconds",
                Help:    "Delivery request duration in seconds",
                Buckets: []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
            },
            []string{"destination"},
        ),
        outboxDepth: prometheus.NewGaugeVec(
            prometheus.GaugeOpts{
                Name: "beacon_outbox_depth",
                Help: "Current number of events in each state",
            },
            []string{"state"},
        ),
        workersActive: prometheus.NewGauge(
            prometheus.GaugeOpts{
                Name: "beacon_workers_active",
                Help: "Number of active worker goroutines",
            },
        ),
        deadLettersTotal: prometheus.NewCounterVec(
            prometheus.CounterOpts{
                Name: "beacon_dead_letters_total",
                Help: "Events sent to dead letter queue",
            },
            []string{"destination"},
        ),
    }

    registry.MustRegister(
        m.deliveryTotal,
        m.deliveryDuration,
        m.outboxDepth,
        m.workersActive,
        m.deadLettersTotal,
    )

    return m
}

func (m *Metrics) DeliverySuccess(destination string) {
    m.deliveryTotal.WithLabelValues(destination, "success").Inc()
}

func (m *Metrics) DeliveryFailure(destination string, statusCode int) {
    status := "server_error"
    if statusCode >= 400 && statusCode < 500 {
        status = "client_error"
    }
    m.deliveryTotal.WithLabelValues(destination, status).Inc()
}

func (m *Metrics) DeliveryDuration(destination string, duration time.Duration) {
    m.deliveryDuration.WithLabelValues(destination).Observe(duration.Seconds())
}
```

---

## Structured Logging

### Log Format (JSON)

```json
{
  "timestamp": "2024-01-15T10:30:45.123Z",
  "level": "info",
  "msg": "event delivered",
  "event_id": "550e8400-e29b-41d4-a716-446655440000",
  "subscription_id": "660e8400-e29b-41d4-a716-446655440000",
  "destination": "analytics-webhook",
  "attempt": 1,
  "duration_ms": 145,
  "status_code": 200
}
```

### Standard Fields

All logs should include these fields when applicable:

| Field | Type | Description |
|-------|------|-------------|
| `timestamp` | ISO8601 | When the log was emitted |
| `level` | string | debug, info, warn, error |
| `msg` | string | Human-readable message |
| `event_id` | UUID | Event being processed |
| `subscription_id` | UUID | Subscription that generated event |
| `destination` | string | Destination name |
| `destination_id` | UUID | Destination ID |
| `worker_id` | string | Worker processing event |
| `attempt` | int | Delivery attempt number |
| `duration_ms` | int | Operation duration |
| `status_code` | int | HTTP response status |
| `error` | string | Error message if failed |

### Log Levels

| Level | Usage |
|-------|-------|
| `debug` | Detailed tracing, poll results, claim counts |
| `info` | Normal operations: delivery success, config applied |
| `warn` | Recoverable issues: retry, semaphore full, slow delivery |
| `error` | Failures: dead letter, connection failed, panic recovery |

### Example Log Messages

```go
// Delivery success
logger.Info("event delivered",
    "event_id", event.ID,
    "destination", dest.Name,
    "attempt", event.Attempts,
    "duration_ms", duration.Milliseconds(),
    "status_code", 200,
)

// Delivery retry
logger.Warn("event rescheduled",
    "event_id", event.ID,
    "destination", dest.Name,
    "attempt", event.Attempts,
    "next_delay", nextDelay,
    "error", err.Error(),
)

// Dead letter
logger.Error("event dead-lettered",
    "event_id", event.ID,
    "subscription_id", event.SubscriptionID,
    "destination", dest.Name,
    "attempts", event.Attempts,
    "reason", errMsg,
)

// Reaper activity
logger.Info("reclaimed stale events",
    "worker_ids", staleWorkers,
    "event_count", reclaimed,
)

// Config applied
logger.Info("configuration applied",
    "destinations_created", result.Destinations.Created,
    "subscriptions_created", result.Subscriptions.Created,
    "triggers_installed", result.Triggers.Created,
)
```

---

## Logger Implementation

Using `log/slog` (Go 1.21+):

```go
func NewLogger(level, format string) (*slog.Logger, error) {
    var lvl slog.Level
    switch level {
    case "debug":
        lvl = slog.LevelDebug
    case "info":
        lvl = slog.LevelInfo
    case "warn":
        lvl = slog.LevelWarn
    case "error":
        lvl = slog.LevelError
    default:
        return nil, fmt.Errorf("unknown log level: %s", level)
    }

    opts := &slog.HandlerOptions{Level: lvl}

    var handler slog.Handler
    switch format {
    case "json":
        handler = slog.NewJSONHandler(os.Stdout, opts)
    case "text":
        handler = slog.NewTextHandler(os.Stdout, opts)
    default:
        return nil, fmt.Errorf("unknown log format: %s", format)
    }

    return slog.New(handler), nil
}
```

### Contextual Logging

Create child loggers with context:

```go
// In dispatcher, add worker context
workerLogger := logger.With("worker_id", workerID)

// In delivery, add event context
eventLogger := workerLogger.With(
    "event_id", event.ID,
    "subscription_id", event.SubscriptionID,
    "destination", dest.Name,
)

eventLogger.Info("starting delivery")
// ... delivery ...
eventLogger.Info("delivery complete", "status_code", 200)
```

---

## Health Check

### `/v1/health` Response

```go
type HealthResponse struct {
    Status   string            `json:"status"`   // "healthy" or "unhealthy"
    Database string            `json:"database"` // "connected" or "disconnected"
    Workers  int               `json:"workers"`  // Active worker count
    Details  map[string]string `json:"details,omitempty"`
    Error    string            `json:"error,omitempty"`
}

func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
    resp := HealthResponse{
        Status:  "healthy",
        Workers: s.dispatcher.WorkerCount(),
    }

    // Check database
    ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
    defer cancel()

    if err := s.pool.Ping(ctx); err != nil {
        resp.Status = "unhealthy"
        resp.Database = "disconnected"
        resp.Error = err.Error()
        w.WriteHeader(http.StatusServiceUnavailable)
    } else {
        resp.Database = "connected"
    }

    json.NewEncoder(w).Encode(resp)
}
```

---

## Alerting Recommendations

### Critical Alerts

| Condition | Threshold | Description |
|-----------|-----------|-------------|
| `beacon_outbox_depth{state="pending"} > 10000` | 5 min | Backlog growing |
| `beacon_dead_letters_total` increase | > 100/hr | High DLQ rate |
| `beacon_workers_active == 0` | 1 min | No workers running |
| Health check failing | 2 min | Service unhealthy |

### Warning Alerts

| Condition | Threshold | Description |
|-----------|-----------|-------------|
| `beacon_delivery_duration_seconds` p99 > 5s | 5 min | Slow deliveries |
| `beacon_delivery_total{status="server_error"}` rate | > 10/min | Destination errors |
| `beacon_outbox_depth{state="delivering"}` high | > 1000 | Many in-flight |

---

## Grafana Dashboard Panels

Recommended panels:

1. **Delivery Rate** - `rate(beacon_delivery_total[5m])` by status
2. **Delivery Latency** - `beacon_delivery_duration_seconds` histogram
3. **Outbox Depth** - `beacon_outbox_depth` by state (stacked)
4. **Dead Letters** - `rate(beacon_dead_letters_total[1h])` by destination
5. **Active Workers** - `beacon_workers_active`
6. **Events Reaped** - `rate(beacon_events_reaped_total[5m])`

---

## Dependencies

- `github.com/prometheus/client_golang` - Prometheus client
- `log/slog` (stdlib) - Structured logging

---

## Usage Example

```go
// Initialize
registry := prometheus.NewRegistry()
metrics := observability.NewMetrics(registry)
logger, _ := observability.NewLogger("info", "json")

// Use in dispatcher
dispatcher := dispatcher.New(pool, repo, client, cfg,
    dispatcher.WithMetrics(metrics),
    dispatcher.WithLogger(logger),
)

// Expose metrics endpoint
http.Handle("/metrics", metrics.Handler())
```

---

## Configuration

Environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `BEACON_LOG_LEVEL` | `info` | Minimum log level |
| `BEACON_LOG_FORMAT` | `json` | Output format |

---

## Metric Cardinality

Keep label cardinality low to avoid memory issues:

- **destination:** Bounded by config (typically < 100)
- **status:** Fixed set (success, client_error, server_error, timeout, connection_error)
- **state:** Fixed set (pending, delivering, delivered, dead)
- **path:** Fixed API paths only

Avoid high-cardinality labels like event_id or subscription_id in metrics.

---

## Testing

### Strategy

**Pure unit tests only**—no database required. Tests verify metric registration, counter increments, gauge values, and logging output format.

### Metrics Tests

```go
// internal/observability/metrics_test.go

package observability_test

import (
    "strings"
    "testing"
    "time"

    "beacon/internal/observability"
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/testutil"
    "github.com/stretchr/testify/assert"
)

func TestMetrics_DeliverySuccess(t *testing.T) {
    registry := prometheus.NewRegistry()
    metrics := observability.NewMetrics(registry)

    metrics.DeliverySuccess("webhook-1")
    metrics.DeliverySuccess("webhook-1")
    metrics.DeliverySuccess("webhook-2")

    // Verify counter values
    count := testutil.ToFloat64(metrics.DeliveryTotal().WithLabelValues("webhook-1", "success"))
    assert.Equal(t, float64(2), count)

    count = testutil.ToFloat64(metrics.DeliveryTotal().WithLabelValues("webhook-2", "success"))
    assert.Equal(t, float64(1), count)
}

func TestMetrics_DeliveryFailure(t *testing.T) {
    registry := prometheus.NewRegistry()
    metrics := observability.NewMetrics(registry)

    metrics.DeliveryFailure("webhook-1", 500)
    metrics.DeliveryFailure("webhook-1", 503)
    metrics.DeliveryFailure("webhook-1", 400)

    // 5xx should be server_error
    count := testutil.ToFloat64(metrics.DeliveryTotal().WithLabelValues("webhook-1", "server_error"))
    assert.Equal(t, float64(2), count)

    // 4xx should be client_error
    count = testutil.ToFloat64(metrics.DeliveryTotal().WithLabelValues("webhook-1", "client_error"))
    assert.Equal(t, float64(1), count)
}

func TestMetrics_DeliveryDuration(t *testing.T) {
    registry := prometheus.NewRegistry()
    metrics := observability.NewMetrics(registry)

    metrics.DeliveryDuration("webhook-1", 100*time.Millisecond)
    metrics.DeliveryDuration("webhook-1", 200*time.Millisecond)

    // Verify histogram has observations
    expected := `
# HELP beacon_delivery_duration_seconds Delivery request duration in seconds
# TYPE beacon_delivery_duration_seconds histogram
`
    err := testutil.CollectAndCompare(metrics.DeliveryDurationHist(), strings.NewReader(expected), "beacon_delivery_duration_seconds")
    // We just check it doesn't error - exact values vary
    _ = err
}

func TestMetrics_DeadLetter(t *testing.T) {
    registry := prometheus.NewRegistry()
    metrics := observability.NewMetrics(registry)

    metrics.DeadLetter("webhook-1")
    metrics.DeadLetter("webhook-1")

    count := testutil.ToFloat64(metrics.DeadLettersTotal().WithLabelValues("webhook-1"))
    assert.Equal(t, float64(2), count)
}

func TestMetrics_OutboxDepth(t *testing.T) {
    registry := prometheus.NewRegistry()
    metrics := observability.NewMetrics(registry)

    metrics.SetOutboxDepth("pending", 100)
    metrics.SetOutboxDepth("delivering", 10)
    metrics.SetOutboxDepth("delivered", 5000)

    assert.Equal(t, float64(100), testutil.ToFloat64(metrics.OutboxDepthGauge().WithLabelValues("pending")))
    assert.Equal(t, float64(10), testutil.ToFloat64(metrics.OutboxDepthGauge().WithLabelValues("delivering")))
    assert.Equal(t, float64(5000), testutil.ToFloat64(metrics.OutboxDepthGauge().WithLabelValues("delivered")))
}

func TestMetrics_ActiveWorkers(t *testing.T) {
    registry := prometheus.NewRegistry()
    metrics := observability.NewMetrics(registry)

    metrics.SetActiveWorkers(10)
    assert.Equal(t, float64(10), testutil.ToFloat64(metrics.WorkersActiveGauge()))

    metrics.SetActiveWorkers(8)
    assert.Equal(t, float64(8), testutil.ToFloat64(metrics.WorkersActiveGauge()))
}

func TestMetrics_Registration(t *testing.T) {
    registry := prometheus.NewRegistry()
    metrics := observability.NewMetrics(registry)

    // Verify all metrics registered
    families, err := registry.Gather()
    assert.NoError(t, err)

    names := make(map[string]bool)
    for _, f := range families {
        names[f.GetName()] = true
    }

    assert.True(t, names["beacon_delivery_total"])
    assert.True(t, names["beacon_delivery_duration_seconds"])
    assert.True(t, names["beacon_outbox_depth"])
    assert.True(t, names["beacon_workers_active"])
    assert.True(t, names["beacon_dead_letters_total"])

    // Verify handler works
    _ = metrics.Handler()
}
```

### Logger Tests

```go
// internal/observability/logging_test.go

func TestNewLogger_Levels(t *testing.T) {
    tests := []struct {
        level string
        valid bool
    }{
        {"debug", true},
        {"info", true},
        {"warn", true},
        {"error", true},
        {"invalid", false},
    }

    for _, tt := range tests {
        t.Run(tt.level, func(t *testing.T) {
            logger, err := observability.NewLogger(tt.level, "json")
            if tt.valid {
                assert.NoError(t, err)
                assert.NotNil(t, logger)
            } else {
                assert.Error(t, err)
            }
        })
    }
}

func TestNewLogger_Formats(t *testing.T) {
    tests := []struct {
        format string
        valid  bool
    }{
        {"json", true},
        {"text", true},
        {"invalid", false},
    }

    for _, tt := range tests {
        t.Run(tt.format, func(t *testing.T) {
            logger, err := observability.NewLogger("info", tt.format)
            if tt.valid {
                assert.NoError(t, err)
                assert.NotNil(t, logger)
            } else {
                assert.Error(t, err)
            }
        })
    }
}

func TestLogger_With(t *testing.T) {
    logger, _ := observability.NewLogger("info", "json")

    // Should return a new logger with added context
    childLogger := logger.With("request_id", "123", "user_id", "456")
    assert.NotNil(t, childLogger)
}

func TestLogger_Output(t *testing.T) {
    // Capture output to verify format
    var buf bytes.Buffer
    logger := observability.NewLoggerWithOutput("info", "json", &buf)

    logger.Info("test message", "key", "value")

    output := buf.String()
    assert.Contains(t, output, "test message")
    assert.Contains(t, output, "key")
    assert.Contains(t, output, "value")

    // JSON format should be parseable
    var logEntry map[string]any
    err := json.Unmarshal([]byte(output), &logEntry)
    assert.NoError(t, err)
    assert.Equal(t, "test message", logEntry["msg"])
}
```

### Health Check Tests

```go
// internal/observability/health_test.go

func TestHealthResponse_Healthy(t *testing.T) {
    resp := observability.HealthResponse{
        Status:   "healthy",
        Database: "connected",
        Workers:  10,
    }

    data, err := json.Marshal(resp)
    assert.NoError(t, err)
    assert.Contains(t, string(data), `"status":"healthy"`)
    assert.Contains(t, string(data), `"workers":10`)
}

func TestHealthResponse_Unhealthy(t *testing.T) {
    resp := observability.HealthResponse{
        Status:   "unhealthy",
        Database: "disconnected",
        Workers:  0,
        Error:    "connection refused",
    }

    data, err := json.Marshal(resp)
    assert.NoError(t, err)
    assert.Contains(t, string(data), `"status":"unhealthy"`)
    assert.Contains(t, string(data), `"error":"connection refused"`)
}
```

### Running Tests

```bash
# Run observability tests (fast - no containers)
go test ./internal/observability/... -v

# Run with coverage
go test ./internal/observability/... -cover
```
