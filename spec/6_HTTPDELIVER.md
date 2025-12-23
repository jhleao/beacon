# 6. HTTP Delivery Specification

## Purpose

The HTTP delivery module provides a hardened HTTP client for webhook delivery. It includes SSRF protection, request signing, and configurable timeouts.

---

## Exposed API

### Package: `internal/httpdeliver`

```go
// Client delivers webhooks with security and reliability features
type Client struct {
    // contains unexported fields
}

// NewClient creates a Client with the global HMAC signing secret
// Pass nil to disable request signing
func NewClient(hmacSecret []byte) *Client

// Deliver sends an event to a destination
// Returns: status code (nil if connection failed), response headers, error
func (c *Client) Deliver(
    ctx context.Context,
    dest outbox.Destination,
    event outbox.Event,
) (*int, map[string]string, error)
```

```go
// SSRFGuard validates URLs against SSRF attacks
type SSRFGuard struct {
    // contains unexported fields
}

// NewSSRFGuard creates a guard with default blocked ranges
func NewSSRFGuard() *SSRFGuard

// CheckURL validates a URL is safe to request
// Returns error if URL is blocked
func (g *SSRFGuard) CheckURL(ctx context.Context, rawURL string) (string, error)

// WithPolicy returns a guard with custom policy applied
func (g *SSRFGuard) WithPolicy(policy SSRFPolicy) *SSRFGuard
```

```go
// SSRFPolicy configures SSRF protection overrides
type SSRFPolicy struct {
    AllowPrivate bool     `json:"allow_private"`  // Allow private IP ranges
    AllowedHosts []string `json:"allowed_hosts"`  // Specific hosts to allow
}
```

```go
// Signer creates HMAC signatures for request authentication
type Signer struct{}

// Sign generates an HMAC-SHA256 signature
func (s *Signer) Sign(secret []byte, timestamp time.Time, body []byte) string

// Headers returns the signing headers to add to a request
func (s *Signer) Headers(secret []byte, body []byte) map[string]string
```

---

## Internal Implementation

### HTTP Client Configuration

```go
func NewClient() *Client {
    transport := &http.Transport{
        DialContext: (&net.Dialer{
            Timeout:   5 * time.Second,   // Connect timeout
            KeepAlive: 30 * time.Second,
        }).DialContext,
        TLSHandshakeTimeout:   5 * time.Second,
        ResponseHeaderTimeout: 30 * time.Second,
        IdleConnTimeout:       90 * time.Second,
        MaxIdleConns:          100,
        MaxIdleConnsPerHost:   10,
    }

    return &Client{
        httpClient: &http.Client{
            Transport: transport,
            // Disable automatic redirects
            CheckRedirect: func(req *http.Request, via []*http.Request) error {
                return http.ErrUseLastResponse
            },
        },
        ssrfGuard: NewSSRFGuard(),
        signer:    &Signer{},
    }
}
```

### Delivery Implementation

```go
func (c *Client) Deliver(
    ctx context.Context,
    dest outbox.Destination,
    event outbox.Event,
) (*int, map[string]string, error) {
    // 1. SSRF check
    guard := c.ssrfGuard
    if dest.SSRFPolicy != nil {
        guard = guard.WithPolicy(*dest.SSRFPolicy)
    }
    safeURL, err := guard.CheckURL(ctx, dest.URL)
    if err != nil {
        return nil, nil, fmt.Errorf("SSRF blocked: %w", err)
    }

    // 2. Marshal payload
    body, err := json.Marshal(event.Payload)
    if err != nil {
        return nil, nil, fmt.Errorf("marshal payload: %w", err)
    }

    // 3. Create request
    req, err := http.NewRequestWithContext(ctx, dest.Method, safeURL, bytes.NewReader(body))
    if err != nil {
        return nil, nil, fmt.Errorf("create request: %w", err)
    }

    // 4. Set headers
    req.Header.Set("Content-Type", "application/json")
    req.Header.Set("User-Agent", "Beacon/1.0")
    req.Header.Set("Beacon-Event-Id", event.ID.String())
    req.Header.Set("Beacon-Attempt", strconv.Itoa(event.Attempts))
    req.Header.Set("Beacon-Subscription-Id", event.SubscriptionID.String())

    // Custom headers from destination
    for k, v := range dest.Headers {
        req.Header.Set(k, v)
    }

    // 5. Add signature if global secret configured
    if len(c.hmacSecret) > 0 {
        sigHeaders := c.signer.Headers(c.hmacSecret, body)
        for k, v := range sigHeaders {
            req.Header.Set(k, v)
        }
    }

    // 6. Apply timeout
    timeout := time.Duration(dest.TimeoutMs) * time.Millisecond
    ctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()
    req = req.WithContext(ctx)

    // 7. Execute request
    resp, err := c.httpClient.Do(req)
    if err != nil {
        return nil, nil, err
    }
    defer resp.Body.Close()

    // 8. Read response (limited)
    io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))

    // 9. Extract response headers
    respHeaders := make(map[string]string)
    for k := range resp.Header {
        respHeaders[k] = resp.Header.Get(k)
    }

    return &resp.StatusCode, respHeaders, nil
}
```

---

## SSRF Protection

### Blocked IP Ranges (Default)

| Range | Description |
|-------|-------------|
| `127.0.0.0/8` | Loopback |
| `10.0.0.0/8` | Private Class A |
| `172.16.0.0/12` | Private Class B |
| `192.168.0.0/16` | Private Class C |
| `169.254.0.0/16` | Link-local |
| `::1/128` | IPv6 loopback |
| `fc00::/7` | IPv6 unique local |
| `fe80::/10` | IPv6 link-local |

### DNS Rebinding Protection

The guard resolves DNS before connecting and validates the resolved IP. Results are cached to reduce DNS overhead for high-throughput destinations:

```go
type SSRFGuard struct {
    blockedRanges []*net.IPNet
    cache         *dnsCache
    cacheTTL      time.Duration  // default: 15s (short for security)
}

type dnsCache struct {
    mu      sync.RWMutex
    entries map[string]*dnsCacheEntry
}

type dnsCacheEntry struct {
    ips       []net.IP
    expiresAt time.Time
    blocked   bool
}

func (g *SSRFGuard) CheckURL(ctx context.Context, rawURL string) (string, error) {
    parsed, err := url.Parse(rawURL)
    if err != nil {
        return "", fmt.Errorf("invalid URL: %w", err)
    }

    // Validate scheme
    if parsed.Scheme != "https" && parsed.Scheme != "http" {
        return "", fmt.Errorf("scheme must be http or https")
    }

    host := parsed.Hostname()

    // Check cache first
    if entry := g.cache.get(host); entry != nil {
        if entry.blocked {
            return "", fmt.Errorf("blocked IP: %s (cached)", host)
        }
        return rawURL, nil
    }

    // Resolve hostname
    ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
    if err != nil {
        return "", fmt.Errorf("DNS lookup failed: %w", err)
    }

    // Check each resolved IP
    for _, ip := range ips {
        if g.isBlocked(ip) {
            g.cache.set(host, ips, true, g.cacheTTL)
            return "", fmt.Errorf("blocked IP: %s resolves to %s", host, ip)
        }
    }

    g.cache.set(host, ips, false, g.cacheTTL)
    return rawURL, nil
}

func (g *SSRFGuard) isBlocked(ip net.IP) bool {
    for _, block := range g.blockedRanges {
        if block.Contains(ip) {
            return true
        }
    }
    return false
}
```

**Cache behavior:**
- TTL: 15 seconds (short for security; balances overhead with timely detection of DNS changes)
- Blocked results are cached to prevent repeated DNS lookups
- Cache is cleared on SSRF policy changes

### Policy Overrides

Destinations can override SSRF protection:

```yaml
destinations:
  - name: internal-service
    url: http://10.0.1.50:8080/webhook
    ssrf_policy:
      allow_private: true       # Allow all private ranges
      # OR
      allowed_hosts:            # Allow specific hosts
        - "10.0.1.50"
        - "internal.company.com"
```

---

## Request Signing

### Signature Format

HMAC-SHA256 signature over `{timestamp}.{body}`:

```go
func (s *Signer) Sign(secret []byte, timestamp time.Time, body []byte) string {
    mac := hmac.New(sha256.New, secret)
    mac.Write([]byte(fmt.Sprintf("%d.", timestamp.Unix())))
    mac.Write(body)
    return hex.EncodeToString(mac.Sum(nil))
}
```

### Headers Added

```go
func (s *Signer) Headers(secret []byte, body []byte) map[string]string {
    timestamp := time.Now()
    signature := s.Sign(secret, timestamp, body)

    return map[string]string{
        "Beacon-Timestamp": strconv.FormatInt(timestamp.Unix(), 10),
        "Beacon-Signature": signature,
    }
}
```

### Verification (Receiver Side)

```go
// Example verification code for webhook receivers
func VerifySignature(secret []byte, timestamp, signature string, body []byte) bool {
    ts, err := strconv.ParseInt(timestamp, 10, 64)
    if err != nil {
        return false
    }

    // Reject old signatures (replay protection)
    if time.Since(time.Unix(ts, 0)) > 5*time.Minute {
        return false
    }

    expected := Sign(secret, time.Unix(ts, 0), body)
    return hmac.Equal([]byte(signature), []byte(expected))
}
```

---

## Request Headers

All requests include these headers:

| Header | Value | Description |
|--------|-------|-------------|
| `Content-Type` | `application/json` | Payload format |
| `User-Agent` | `Beacon/1.0` | Client identifier |
| `Beacon-Event-Id` | UUID | Unique event ID (for idempotency) |
| `Beacon-Attempt` | Integer | Delivery attempt number (1-indexed) |
| `Beacon-Subscription-Id` | UUID | Subscription that generated this event |
| `Beacon-Timestamp` | Unix timestamp | When signature was created (if signing) |
| `Beacon-Signature` | Hex string | HMAC-SHA256 signature (if signing) |

Plus any custom headers from `destinations.headers`.

---

## Timeout Configuration

Per-destination timeout via `timeout_ms`:

```yaml
destinations:
  - name: fast-service
    url: https://fast.example.com/webhook
    timeout_ms: 2000   # 2 seconds

  - name: slow-service
    url: https://slow.example.com/webhook
    timeout_ms: 30000  # 30 seconds
```

Default: 5000ms (5 seconds)

### Timeout Hierarchy

Multiple timeouts apply at different stages:

| Stage | Timeout | Configurable | Description |
|-------|---------|--------------|-------------|
| DNS resolution | (system) | No | Covered by connect timeout |
| TCP connect | 5s | **No** | Hardcoded to prevent slow connects from blocking workers |
| TLS handshake | 5s | **No** | Hardcoded for security |
| Total request | `timeout_ms` | Yes | Per-destination, includes response wait |

> **Note:** If `timeout_ms` exceeds 10s, the effective timeout for connection establishment is still 5s; only the response wait extends. This ensures workers aren't blocked by destinations with aggressive timeouts on unreachable hosts.

---

## Redirect Handling

Redirects are **disabled** to prevent:
1. SSRF via redirect to internal IP
2. Infinite redirect loops
3. Unexpected behavior changes

If a destination returns 3xx, it's treated as a delivery failure and retried.

---

## Error Types

```go
var (
    ErrSSRFBlocked    = errors.New("SSRF protection blocked request")
    ErrDNSLookup      = errors.New("DNS lookup failed")
    ErrConnectionFailed = errors.New("connection failed")
    ErrTimeout        = errors.New("request timeout")
)
```

---

## Dependencies

- Standard library (`net/http`, `crypto/hmac`, `crypto/sha256`, `net`)
- `internal/outbox` - Event and Destination types

---

## Testing

### Strategy

Use **httptest** for mock HTTP servers and **unit tests** for SSRF guard and signing logic. No database required—this module is HTTP-focused.

### SSRF Guard Tests

```go
// internal/httpdeliver/ssrf_test.go

package httpdeliver_test

import (
    "context"
    "testing"

    "beacon/internal/httpdeliver"
    "github.com/stretchr/testify/assert"
)

func TestSSRFGuard_BlocksPrivateIPs(t *testing.T) {
    guard := httpdeliver.NewSSRFGuard()
    ctx := context.Background()

    blockedURLs := []string{
        "http://127.0.0.1/webhook",
        "http://localhost/webhook",
        "http://10.0.0.1/webhook",
        "http://10.255.255.255/webhook",
        "http://172.16.0.1/webhook",
        "http://172.31.255.255/webhook",
        "http://192.168.1.1/webhook",
        "http://169.254.169.254/latest/meta-data/",  // AWS metadata
        "http://[::1]/webhook",                       // IPv6 loopback
    }

    for _, url := range blockedURLs {
        _, err := guard.CheckURL(ctx, url)
        assert.Error(t, err, "should block %s", url)
        assert.Contains(t, err.Error(), "blocked", "error for %s", url)
    }
}

func TestSSRFGuard_AllowsPublicIPs(t *testing.T) {
    guard := httpdeliver.NewSSRFGuard()
    ctx := context.Background()

    allowedURLs := []string{
        "https://api.example.com/webhook",
        "https://8.8.8.8/webhook",
        "https://1.1.1.1/webhook",
    }

    for _, url := range allowedURLs {
        safe, err := guard.CheckURL(ctx, url)
        assert.NoError(t, err, "should allow %s", url)
        assert.Equal(t, url, safe)
    }
}

func TestSSRFGuard_RejectsInvalidSchemes(t *testing.T) {
    guard := httpdeliver.NewSSRFGuard()
    ctx := context.Background()

    invalidURLs := []string{
        "ftp://example.com/file",
        "file:///etc/passwd",
        "gopher://localhost/",
    }

    for _, url := range invalidURLs {
        _, err := guard.CheckURL(ctx, url)
        assert.Error(t, err, "should reject %s", url)
    }
}

func TestSSRFGuard_WithPolicy_AllowPrivate(t *testing.T) {
    guard := httpdeliver.NewSSRFGuard()
    ctx := context.Background()

    policy := httpdeliver.SSRFPolicy{AllowPrivate: true}
    guardWithPolicy := guard.WithPolicy(policy)

    // Now private IPs should be allowed
    safe, err := guardWithPolicy.CheckURL(ctx, "http://10.0.1.50/webhook")
    assert.NoError(t, err)
    assert.Equal(t, "http://10.0.1.50/webhook", safe)
}

func TestSSRFGuard_WithPolicy_AllowedHosts(t *testing.T) {
    guard := httpdeliver.NewSSRFGuard()
    ctx := context.Background()

    policy := httpdeliver.SSRFPolicy{
        AllowedHosts: []string{"internal.company.com", "10.0.1.50"},
    }
    guardWithPolicy := guard.WithPolicy(policy)

    // Specific allowed host should work
    safe, err := guardWithPolicy.CheckURL(ctx, "http://10.0.1.50/webhook")
    assert.NoError(t, err)
    assert.Equal(t, "http://10.0.1.50/webhook", safe)

    // Other private IPs still blocked
    _, err = guardWithPolicy.CheckURL(ctx, "http://10.0.1.51/webhook")
    assert.Error(t, err)
}
```

### Signer Tests

```go
// internal/httpdeliver/signer_test.go

package httpdeliver_test

import (
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "testing"
    "time"

    "beacon/internal/httpdeliver"
    "github.com/stretchr/testify/assert"
)

func TestSigner_Sign(t *testing.T) {
    signer := &httpdeliver.Signer{}
    secret := []byte("test-secret")
    timestamp := time.Unix(1700000000, 0)
    body := []byte(`{"event":"test"}`)

    signature := signer.Sign(secret, timestamp, body)

    // Verify manually
    expected := computeSignature(secret, timestamp, body)
    assert.Equal(t, expected, signature)
}

func TestSigner_Headers(t *testing.T) {
    signer := &httpdeliver.Signer{}
    secret := []byte("test-secret")
    body := []byte(`{"event":"test"}`)

    headers := signer.Headers(secret, body)

    assert.Contains(t, headers, "Beacon-Timestamp")
    assert.Contains(t, headers, "Beacon-Signature")

    // Timestamp should be recent
    ts := headers["Beacon-Timestamp"]
    assert.NotEmpty(t, ts)

    // Signature should be hex-encoded
    sig := headers["Beacon-Signature"]
    _, err := hex.DecodeString(sig)
    assert.NoError(t, err, "signature should be valid hex")
}

func TestSigner_Deterministic(t *testing.T) {
    signer := &httpdeliver.Signer{}
    secret := []byte("test-secret")
    timestamp := time.Unix(1700000000, 0)
    body := []byte(`{"event":"test"}`)

    sig1 := signer.Sign(secret, timestamp, body)
    sig2 := signer.Sign(secret, timestamp, body)

    assert.Equal(t, sig1, sig2, "same inputs should produce same signature")
}

func TestSigner_DifferentSecretsProduceDifferentSignatures(t *testing.T) {
    signer := &httpdeliver.Signer{}
    timestamp := time.Unix(1700000000, 0)
    body := []byte(`{"event":"test"}`)

    sig1 := signer.Sign([]byte("secret-1"), timestamp, body)
    sig2 := signer.Sign([]byte("secret-2"), timestamp, body)

    assert.NotEqual(t, sig1, sig2)
}

// Helper to compute expected signature
func computeSignature(secret []byte, timestamp time.Time, body []byte) string {
    mac := hmac.New(sha256.New, secret)
    mac.Write([]byte(fmt.Sprintf("%d.", timestamp.Unix())))
    mac.Write(body)
    return hex.EncodeToString(mac.Sum(nil))
}
```

### HTTP Delivery Tests

```go
// internal/httpdeliver/client_test.go

package httpdeliver_test

import (
    "context"
    "io"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "beacon/internal/httpdeliver"
    "beacon/internal/outbox"
    "github.com/google/uuid"
    "github.com/stretchr/testify/assert"
)

func TestClient_Deliver_Success(t *testing.T) {
    var receivedBody []byte
    var receivedHeaders http.Header

    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        receivedHeaders = r.Header
        receivedBody, _ = io.ReadAll(r.Body)
        w.WriteHeader(200)
        w.Write([]byte(`{"ok":true}`))
    }))
    defer server.Close()

    client := httpdeliver.NewClient(nil)
    dest := outbox.Destination{
        ID:        uuid.New(),
        URL:       server.URL,
        Method:    "POST",
        TimeoutMs: 5000,
        Headers:   map[string]string{"X-Custom": "test-value"},
    }
    event := outbox.Event{
        ID:             uuid.New(),
        SubscriptionID: uuid.New(),
        Payload:        []byte(`{"data":"test"}`),
        Attempts:       1,
    }

    statusCode, _, err := client.Deliver(context.Background(), dest, event)

    assert.NoError(t, err)
    assert.NotNil(t, statusCode)
    assert.Equal(t, 200, *statusCode)

    // Verify headers sent
    assert.Equal(t, "application/json", receivedHeaders.Get("Content-Type"))
    assert.Equal(t, "Beacon/1.0", receivedHeaders.Get("User-Agent"))
    assert.Equal(t, event.ID.String(), receivedHeaders.Get("Beacon-Event-Id"))
    assert.Equal(t, "1", receivedHeaders.Get("Beacon-Attempt"))
    assert.Equal(t, "test-value", receivedHeaders.Get("X-Custom"))

    // Verify body
    assert.Equal(t, `{"data":"test"}`, string(receivedBody))
}

func TestClient_Deliver_WithSigning(t *testing.T) {
    var receivedHeaders http.Header

    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        receivedHeaders = r.Header
        w.WriteHeader(200)
    }))
    defer server.Close()

    secret := []byte("test-hmac-secret")
    client := httpdeliver.NewClient(secret)

    dest := outbox.Destination{
        ID:        uuid.New(),
        URL:       server.URL,
        Method:    "POST",
        TimeoutMs: 5000,
    }
    event := outbox.Event{
        ID:      uuid.New(),
        Payload: []byte(`{"data":"test"}`),
    }

    client.Deliver(context.Background(), dest, event)

    // Verify signing headers present
    assert.NotEmpty(t, receivedHeaders.Get("Beacon-Timestamp"))
    assert.NotEmpty(t, receivedHeaders.Get("Beacon-Signature"))
}

func TestClient_Deliver_ServerError(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(503)
        w.Write([]byte("Service Unavailable"))
    }))
    defer server.Close()

    client := httpdeliver.NewClient(nil)
    dest := outbox.Destination{URL: server.URL, Method: "POST", TimeoutMs: 5000}
    event := outbox.Event{ID: uuid.New(), Payload: []byte(`{}`)}

    statusCode, _, err := client.Deliver(context.Background(), dest, event)

    assert.NoError(t, err)  // No error, just non-2xx status
    assert.Equal(t, 503, *statusCode)
}

func TestClient_Deliver_Timeout(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        time.Sleep(2 * time.Second)  // Longer than timeout
        w.WriteHeader(200)
    }))
    defer server.Close()

    client := httpdeliver.NewClient(nil)
    dest := outbox.Destination{URL: server.URL, Method: "POST", TimeoutMs: 100}
    event := outbox.Event{ID: uuid.New(), Payload: []byte(`{}`)}

    statusCode, _, err := client.Deliver(context.Background(), dest, event)

    assert.Error(t, err)
    assert.Nil(t, statusCode)
    assert.Contains(t, err.Error(), "deadline exceeded")
}

func TestClient_Deliver_ConnectionRefused(t *testing.T) {
    client := httpdeliver.NewClient(nil)
    dest := outbox.Destination{
        URL:       "http://127.0.0.1:59999",  // Nothing listening
        Method:    "POST",
        TimeoutMs: 1000,
    }
    event := outbox.Event{ID: uuid.New(), Payload: []byte(`{}`)}

    statusCode, _, err := client.Deliver(context.Background(), dest, event)

    assert.Error(t, err)
    assert.Nil(t, statusCode)
}

func TestClient_Deliver_NoRedirectFollow(t *testing.T) {
    redirectCount := 0
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        redirectCount++
        if redirectCount == 1 {
            http.Redirect(w, r, "/redirected", http.StatusFound)
            return
        }
        w.WriteHeader(200)
    }))
    defer server.Close()

    client := httpdeliver.NewClient(nil)
    dest := outbox.Destination{URL: server.URL, Method: "POST", TimeoutMs: 5000}
    event := outbox.Event{ID: uuid.New(), Payload: []byte(`{}`)}

    statusCode, _, err := client.Deliver(context.Background(), dest, event)

    // Should get 302, not follow redirect
    assert.NoError(t, err)
    assert.Equal(t, 302, *statusCode)
    assert.Equal(t, 1, redirectCount, "should not follow redirects")
}
```

### Running Tests

```bash
# Run httpdeliver tests (fast - uses httptest)
go test ./internal/httpdeliver/... -v

# Run with race detector
go test ./internal/httpdeliver/... -race
```

---

## Usage Example

```go
// Load global signing secret from environment
hmacSecret := config.LoadHMACSecret()  // from BEACON_HMAC_SECRET
client := httpdeliver.NewClient(hmacSecret)

dest := outbox.Destination{
    URL:       "https://webhook.example.com/events",
    Method:    "POST",
    TimeoutMs: 5000,
    Headers:   map[string]string{"X-Custom": "value"},
}

event := outbox.Event{
    ID:      uuid.New(),
    Payload: json.RawMessage(`{"type":"user.created"}`),
    // ...
}

statusCode, headers, err := client.Deliver(ctx, dest, event)
if err != nil {
    log.Error("delivery failed", "error", err)
} else if *statusCode >= 200 && *statusCode < 300 {
    log.Info("delivery succeeded", "status", *statusCode)
} else {
    log.Warn("delivery rejected", "status", *statusCode)
}
```

---

## Security Considerations

1. **SSRF:** Validated before every request, even for same destination
2. **DNS Rebinding:** IP checked at resolution time
3. **TLS:** HTTPS strongly recommended; HTTP allowed for internal use
4. **Secrets:** Never logged; loaded from environment only
5. **Response Body:** Limited read to prevent memory exhaustion
6. **Redirects:** Disabled to prevent SSRF bypass
