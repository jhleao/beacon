# 5. Retry Policy Specification

## Purpose

Defines the exponential backoff retry policy and dead-letter queue (DLQ) handling. The policy is aggressive (fast initial retries) but bounded (capped delay, limited attempts).

---

## Exposed API

### Package: `internal/dispatcher/retry`

```go
// Policy constants
const (
    BaseDelay   = 1 * time.Second   // Initial retry delay
    MaxDelay    = 15 * time.Minute  // Maximum delay cap
    MaxAttempts = 10                // Total attempts before DLQ
    JitterRatio = 0.2               // 20% random jitter
)

// NextDelay calculates the delay before the next retry attempt
// attempt is 1-indexed (first retry is attempt 1)
func NextDelay(attempt int) time.Duration

// ShouldRetry returns true if the event should be retried
func ShouldRetry(attempts int) bool

// IsRetryableError returns true if the error type warrants a retry
func IsRetryableError(err error) bool

// IsRetryableStatus returns true if the HTTP status code warrants a retry
func IsRetryableStatus(statusCode int) bool
```

---

## Internal Implementation

### Exponential Backoff

```go
func NextDelay(attempt int) time.Duration {
    // Exponential: base * 2^(attempt-1), so first retry is base delay
    delay := BaseDelay * time.Duration(1<<(attempt-1))

    // Cap at maximum
    if delay > MaxDelay {
        delay = MaxDelay
    }

    // Add jitter (0 to 20% of delay)
    jitter := time.Duration(rand.Float64() * JitterRatio * float64(delay))

    return delay + jitter
}
```

### Retry Decision

```go
func ShouldRetry(attempts int) bool {
    return attempts < MaxAttempts
}
```

### Retryable Errors

```go
func IsRetryableError(err error) bool {
    // Context cancelled/deadline - not retryable
    if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
        return false
    }

    // Network errors - check if timeout
    var netErr net.Error
    if errors.As(err, &netErr) {
        return netErr.Timeout()
    }

    // DNS errors are retryable (transient resolution failures)
    var dnsErr *net.DNSError
    if errors.As(err, &dnsErr) {
        return dnsErr.IsTemporary
    }

    // TLS certificate errors are not retryable
    var certErr *tls.CertificateVerificationError
    if errors.As(err, &certErr) {
        return false
    }

    // Connection refused, reset, etc. - retryable
    var opErr *net.OpError
    if errors.As(err, &opErr) {
        return true
    }

    // Default: retry on unknown errors
    return true
}
```

### Retryable HTTP Status Codes

```go
func IsRetryableStatus(statusCode int) bool {
    switch statusCode {
    case
        408, // Request Timeout
        425, // Too Early
        429, // Too Many Requests
        500, // Internal Server Error
        502, // Bad Gateway
        503, // Service Unavailable
        504: // Gateway Timeout
        return true
    default:
        return false
    }
}
```

**Non-retryable status codes (immediate DLQ):**
- `400 Bad Request` - Payload issue, retrying won't help
- `401 Unauthorized` - Auth issue, needs config fix
- `403 Forbidden` - Permission issue
- `404 Not Found` - Endpoint doesn't exist
- `405 Method Not Allowed` - Config error
- `410 Gone` - Resource permanently deleted
- `413 Payload Too Large` - Won't change on retry
- `415 Unsupported Media Type` - Config error

---

## Retry Schedule

With default settings (1s base, 15min cap, 10 attempts), using formula `base * 2^(attempt-1)`:

| Attempt | Base Delay | With Max Jitter | Cumulative Time |
|---------|------------|-----------------|-----------------|
| 1 | 1s | ~1.2s | ~1s |
| 2 | 2s | ~2.4s | ~3s |
| 3 | 4s | ~4.8s | ~8s |
| 4 | 8s | ~9.6s | ~17s |
| 5 | 16s | ~19.2s | ~36s |
| 6 | 32s | ~38.4s | ~1m 14s |
| 7 | 64s | ~76.8s | ~2m 30s |
| 8 | 128s | ~153.6s | ~5m |
| 9 | 256s | ~307.2s | ~10m |
| 10 | 512s | ~614.4s | ~19m |

**Total time to DLQ: ~19 minutes**

To achieve longer retry windows, increase `MaxAttempts`:
- `MaxAttempts = 12` → ~1.3 hours to DLQ
- `MaxAttempts = 15` → ~2.5 hours to DLQ

---

## Jitter Purpose

Jitter prevents "thundering herd" when many events fail simultaneously:

```
Without jitter:
  Event A retry at: t+60s
  Event B retry at: t+60s
  Event C retry at: t+60s
  → All hit destination at same time

With 20% jitter:
  Event A retry at: t+60s + rand(0-12s) = t+67s
  Event B retry at: t+60s + rand(0-12s) = t+54s
  Event C retry at: t+60s + rand(0-12s) = t+71s
  → Spread out, reducing load spikes
```

---

## Dead Letter Queue

### When Events Go to DLQ

1. **Max attempts exceeded:** After 10 failed delivery attempts
2. **Non-retryable error:** Immediate DLQ for permanent failures
3. **Non-retryable status:** 4xx errors (except 408, 425, 429)

### DLQ Record Structure

```sql
INSERT INTO beacon.dead_letters (event_id, reason, snapshot)
VALUES (
    $1,
    $2,  -- e.g., "max attempts exceeded: connection refused"
    $3   -- Full event JSON snapshot
);
```

### Snapshot Contents

```json
{
  "subscription_id": "...",
  "occurred_at": "2024-01-15T10:30:00Z",
  "table_schema": "public",
  "table_name": "users",
  "operation": "INSERT",
  "pk": {"id": "..."},
  "old_data": null,
  "new_data": {"id": "...", "email": "..."},
  "payload": {...},
  "attempts": 10,
  "last_error": "connection refused"
}
```

---

## Replay from DLQ

Dead letters can be replayed via the control plane API:

```http
POST /v1/subscriptions/:id/replay
```

This moves events from `dead_letters` back to `outbox_events` with:
- `state = 'pending'`
- `attempts = 0`
- `visible_at = now()`

The `replay_count` is incremented each time an event is replayed:

```sql
UPDATE beacon.dead_letters
SET replay_count = replay_count + 1
WHERE event_id = $1;
```

**Replay limits:** To prevent infinite replay loops, events with `replay_count >= 3` cannot be replayed automatically. Use the force flag to override:

```http
POST /v1/subscriptions/:id/replay?force=true
```

---

## Integration with Dispatcher

```go
// In dispatcher/dispatcher.go

func (d *Dispatcher) handleFailure(ctx context.Context, event outbox.Event, dest outbox.Destination, deliveryErr error) {
    errMsg := formatError(deliveryErr)

    // Check if retryable
    retryable := retry.ShouldRetry(event.Attempts)
    if deliveryErr != nil {
        retryable = retryable && retry.IsRetryableError(deliveryErr)
    }

    if !retryable {
        d.repo.ToDead(ctx, event.ID, errMsg)
        d.metrics.DeadLetter(dest.Name)
        d.logger.Warn("event dead-lettered",
            "event_id", event.ID,
            "attempts", event.Attempts,
            "reason", errMsg,
        )
        return
    }

    // Calculate next visibility
    nextDelay := retry.NextDelay(event.Attempts)
    visibleAt := time.Now().Add(nextDelay)

    d.repo.Reschedule(ctx, event.ID, visibleAt, errMsg)
    d.metrics.DeliveryRetry(dest.Name)
    d.logger.Info("event rescheduled",
        "event_id", event.ID,
        "attempt", event.Attempts,
        "next_delay", nextDelay,
    )
}
```

---

## Configuration

The retry policy uses compile-time constants for simplicity. If runtime configuration is needed:

```go
type RetryConfig struct {
    BaseDelay   time.Duration
    MaxDelay    time.Duration
    MaxAttempts int
    JitterRatio float64
}

func DefaultConfig() RetryConfig {
    return RetryConfig{
        BaseDelay:   1 * time.Second,
        MaxDelay:    15 * time.Minute,
        MaxAttempts: 10,
        JitterRatio: 0.2,
    }
}
```

---

## Dependencies

- Standard library only (`time`, `math/rand`, `errors`, `net`)

---

## Testing

### Strategy

**Pure unit tests only**—no database or HTTP required. The retry module is pure logic (math and error classification). Tests verify backoff calculation, jitter bounds, and error type classification.

### Test Cases

```go
// internal/dispatcher/retry/retry_test.go

package retry_test

import (
    "context"
    "crypto/tls"
    "errors"
    "net"
    "testing"
    "time"

    "beacon/internal/dispatcher/retry"
    "github.com/stretchr/testify/assert"
)

func TestNextDelay_Exponential(t *testing.T) {
    // Verify exponential growth: base * 2^(attempt-1)
    // Delay includes up to 20% jitter
    tests := []struct {
        attempt  int
        minDelay time.Duration
        maxDelay time.Duration
    }{
        {1, 1 * time.Second, 1200 * time.Millisecond},    // 1s + up to 20% jitter
        {2, 2 * time.Second, 2400 * time.Millisecond},    // 2s + jitter
        {3, 4 * time.Second, 4800 * time.Millisecond},    // 4s + jitter
        {4, 8 * time.Second, 9600 * time.Millisecond},    // 8s + jitter
        {5, 16 * time.Second, 19200 * time.Millisecond},  // 16s + jitter
    }

    for _, tt := range tests {
        delay := retry.NextDelay(tt.attempt)
        assert.GreaterOrEqual(t, delay, tt.minDelay, "attempt %d", tt.attempt)
        assert.LessOrEqual(t, delay, tt.maxDelay, "attempt %d", tt.attempt)
    }
}

func TestNextDelay_CappedAtMax(t *testing.T) {
    // High attempts should be capped at MaxDelay (15min)
    for attempt := 10; attempt <= 20; attempt++ {
        delay := retry.NextDelay(attempt)
        // Max is 15 min + 20% jitter = 18 min
        assert.LessOrEqual(t, delay, 18*time.Minute, "attempt %d should be capped", attempt)
    }
}

func TestNextDelay_Jitter(t *testing.T) {
    // Run multiple times to verify jitter varies
    delays := make(map[time.Duration]bool)
    for i := 0; i < 100; i++ {
        delays[retry.NextDelay(3)] = true
    }

    // With 20% jitter on 4s, we should see variation
    assert.Greater(t, len(delays), 1, "jitter should produce different values")
}

func TestShouldRetry(t *testing.T) {
    tests := []struct {
        attempts int
        expected bool
    }{
        {0, true},
        {1, true},
        {5, true},
        {9, true},
        {10, false},  // MaxAttempts = 10
        {11, false},
        {100, false},
    }

    for _, tt := range tests {
        result := retry.ShouldRetry(tt.attempts)
        assert.Equal(t, tt.expected, result, "attempts=%d", tt.attempts)
    }
}

func TestIsRetryableStatus(t *testing.T) {
    retryable := []int{408, 425, 429, 500, 502, 503, 504}
    nonRetryable := []int{200, 201, 400, 401, 403, 404, 405, 410, 413, 415}

    for _, code := range retryable {
        assert.True(t, retry.IsRetryableStatus(code), "status %d should be retryable", code)
    }

    for _, code := range nonRetryable {
        assert.False(t, retry.IsRetryableStatus(code), "status %d should NOT be retryable", code)
    }
}

func TestIsRetryableError(t *testing.T) {
    tests := []struct {
        name     string
        err      error
        expected bool
    }{
        {
            name:     "context cancelled",
            err:      context.Canceled,
            expected: false,
        },
        {
            name:     "context deadline exceeded",
            err:      context.DeadlineExceeded,
            expected: false,
        },
        {
            name:     "timeout error",
            err:      &timeoutError{timeout: true},
            expected: true,
        },
        {
            name:     "temporary DNS error",
            err:      &net.DNSError{IsTemporary: true},
            expected: true,
        },
        {
            name:     "permanent DNS error",
            err:      &net.DNSError{IsTemporary: false},
            expected: false,
        },
        {
            name:     "connection refused",
            err:      &net.OpError{Op: "dial", Err: errors.New("connection refused")},
            expected: true,
        },
        {
            name:     "TLS certificate error",
            err:      &tls.CertificateVerificationError{},
            expected: false,
        },
        {
            name:     "generic error",
            err:      errors.New("something went wrong"),
            expected: true,  // Default to retry on unknown errors
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            result := retry.IsRetryableError(tt.err)
            assert.Equal(t, tt.expected, result)
        })
    }
}

// Helper types for testing
type timeoutError struct {
    timeout bool
}

func (e *timeoutError) Error() string   { return "timeout" }
func (e *timeoutError) Timeout() bool   { return e.timeout }
func (e *timeoutError) Temporary() bool { return e.timeout }
```

### Table-Driven Backoff Verification

```go
func TestBackoffSchedule(t *testing.T) {
    // Verify the documented retry schedule (approximately)
    // Formula: base * 2^(attempt-1), so attempt 1 = 1s, attempt 2 = 2s, etc.

    schedule := []struct {
        attempt           int
        expectedBaseDelay time.Duration
    }{
        {1, 1 * time.Second},
        {2, 2 * time.Second},
        {3, 4 * time.Second},
        {4, 8 * time.Second},
        {5, 16 * time.Second},
        {6, 32 * time.Second},
        {7, 64 * time.Second},
        {8, 128 * time.Second},
        {9, 256 * time.Second},
        {10, 512 * time.Second},
    }

    for _, s := range schedule {
        // Run multiple times and take minimum (closest to base without jitter)
        minDelay := time.Hour
        for i := 0; i < 100; i++ {
            d := retry.NextDelay(s.attempt)
            if d < minDelay {
                minDelay = d
            }
        }

        // Min delay should be close to expected base (within 100ms)
        diff := minDelay - s.expectedBaseDelay
        if diff < 0 {
            diff = -diff
        }
        assert.Less(t, diff, 100*time.Millisecond,
            "attempt %d: expected ~%v, got min %v", s.attempt, s.expectedBaseDelay, minDelay)
    }
}

func TestTotalTimeToDeadLetter(t *testing.T) {
    // Verify total time to DLQ is approximately 17-20 minutes
    // Sum of: 1 + 2 + 4 + 8 + 16 + 32 + 64 + 128 + 256 + 512 = 1023 seconds ≈ 17 min

    totalMin := time.Duration(0)
    totalMax := time.Duration(0)

    for attempt := 1; attempt <= retry.MaxAttempts; attempt++ {
        baseDelay := retry.BaseDelay * time.Duration(1<<(attempt-1))
        if baseDelay > retry.MaxDelay {
            baseDelay = retry.MaxDelay
        }
        totalMin += baseDelay
        totalMax += baseDelay + time.Duration(float64(baseDelay)*retry.JitterRatio)
    }

    t.Logf("Total time to DLQ: %v - %v", totalMin, totalMax)

    // Should be roughly 17-24 minutes
    assert.Greater(t, totalMin, 15*time.Minute)
    assert.Less(t, totalMax, 25*time.Minute)
}
```

### Running Tests

```bash
# Run retry tests (fast - no containers needed)
go test ./internal/dispatcher/retry/... -v

# Run with coverage
go test ./internal/dispatcher/retry/... -cover
```

---

## Usage Example

```go
import "beacon/internal/dispatcher/retry"

// In delivery handler
if err != nil || !isSuccess(statusCode) {
    if !retry.ShouldRetry(event.Attempts) {
        repo.ToDead(ctx, event.ID, err.Error())
    } else {
        delay := retry.NextDelay(event.Attempts)
        repo.Reschedule(ctx, event.ID, time.Now().Add(delay), err.Error())
    }
}
```

---

## Design Rationale

### Why Exponential Backoff?

- **Aggressive start:** Quick recovery from transient failures
- **Capped maximum:** Prevents unbounded delays
- **Exponential growth:** Reduces load on failing destinations

### Why 10 Attempts?

- Balances quick failure detection with resilience
- ~38 minutes total gives destinations time to recover
- Not so long that operator intervention is delayed

### Why No Circuit Breaker?

Per the design decisions, v1 relies on:
1. **Per-destination semaphores:** Limits concurrent requests
2. **Exponential backoff:** Naturally reduces request rate
3. **DLQ:** Prevents infinite retries

Circuit breakers may be added in v2 if needed.
