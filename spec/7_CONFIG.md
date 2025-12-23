# 7. Configuration Specification

## Purpose

Defines the YAML configuration schema for destinations and subscriptions, environment variable handling, and runtime configuration loading. YAML is the source of truth; secrets come from environment variables.

---

## Exposed API

### Package: `internal/config`

#### YAML Types

```go
// BeaconConfig is the root YAML configuration
type BeaconConfig struct {
    Version       int                  `yaml:"version"`
    Destinations  []DestinationConfig  `yaml:"destinations"`
    Subscriptions []SubscriptionConfig `yaml:"subscriptions"`
}

// DestinationConfig defines a webhook endpoint
type DestinationConfig struct {
    Name        string            `yaml:"name"`
    URL         string            `yaml:"url"`
    Method      string            `yaml:"method,omitempty"`      // default: POST
    TimeoutMs   int               `yaml:"timeout_ms,omitempty"`  // default: 5000
    MaxInFlight int               `yaml:"max_in_flight,omitempty"` // default: 50
    Headers     map[string]string `yaml:"headers,omitempty"`
    SSRFPolicy  *SSRFPolicy       `yaml:"ssrf_policy,omitempty"`
    // Note: signing_secret loaded from env BEACON_SECRET_<NAME>
}

// SubscriptionConfig defines what to capture and where to send
type SubscriptionConfig struct {
    Name        string   `yaml:"name"`
    Table       string   `yaml:"table"`        // format: "schema.table" or "table" (assumes public)
    Operation   string   `yaml:"operation"`    // INSERT, UPDATE, DELETE
    Destination string   `yaml:"destination"`  // destination name reference
    TriggerOn   []string `yaml:"trigger_on,omitempty"`  // columns to watch (UPDATE only)
    Select      []string `yaml:"select,omitempty"`      // columns to include in payload
    Enabled     *bool    `yaml:"enabled,omitempty"`     // default: true
}

// SSRFPolicy configures SSRF protection
type SSRFPolicy struct {
    AllowPrivate bool     `yaml:"allow_private,omitempty"`
    AllowedHosts []string `yaml:"allowed_hosts,omitempty"`
}
```

#### Parsing Functions

```go
// Parse parses YAML config bytes into BeaconConfig
func Parse(data []byte) (*BeaconConfig, error)

// Validate checks the config for errors
func (c *BeaconConfig) Validate() error

// ParseTable splits "schema.table" into components
func ParseTable(table string) (schema, name string)
```

#### Environment Loading

```go
// EnvConfig holds runtime environment configuration
type EnvConfig struct {
    DatabaseURL   string
    HTTPAddr      string
    PollInterval  time.Duration
    BatchSize     int
    WorkerCount   int
}

// LoadEnv loads configuration from environment variables
func LoadEnv() (*EnvConfig, error)

// LoadSecret loads a destination's signing secret from environment
// Looks for BEACON_SECRET_<DESTINATION_NAME>
func LoadSecret(destName string) ([]byte, error)
```

---

## YAML Schema

### Full Example

```yaml
version: 1

destinations:
  - name: analytics-webhook
    url: https://analytics.example.com/events
    method: POST
    timeout_ms: 5000
    max_in_flight: 50
    headers:
      Content-Type: application/json
      X-Source: beacon
    # Signing uses global BEACON_HMAC_SECRET for all destinations

  - name: audit-service
    url: https://audit.internal:8443/ingest
    timeout_ms: 10000
    max_in_flight: 20
    ssrf_policy:
      allow_private: true

  - name: slack-notifications
    url: https://hooks.slack.com/services/T00/B00/xxx
    timeout_ms: 3000
    max_in_flight: 10

subscriptions:
  - name: users-insert-analytics
    table: public.users
    operation: INSERT
    destination: analytics-webhook

  - name: users-update-analytics
    table: public.users
    operation: UPDATE
    destination: analytics-webhook
    trigger_on: [email, name, plan_id]    # only fire if these change
    select: [id, email, name, updated_at]  # only include these columns

  - name: orders-insert-audit
    table: orders                          # assumes public schema
    operation: INSERT
    destination: audit-service
    select: [id, user_id, total, created_at]

  - name: orders-status-audit
    table: public.orders
    operation: UPDATE
    destination: audit-service
    trigger_on: [status]
    select: [id, status, updated_at]

  - name: orders-delete-audit
    table: public.orders
    operation: DELETE
    destination: audit-service
    enabled: false                         # temporarily disabled
```

---

## Validation Rules

### Destination Validation

| Field | Required | Validation |
|-------|----------|------------|
| `name` | Yes | Non-empty, unique, valid identifier (a-z, 0-9, -, _) |
| `url` | Yes | Valid URL, http or https scheme |
| `method` | No | One of: GET, POST, PUT, PATCH, DELETE (default: POST) |
| `timeout_ms` | No | Positive integer, max 60000 (default: 5000) |
| `max_in_flight` | No | Positive integer, max 1000 (default: 50) |
| `headers` | No | Valid header names and values |

### Subscription Validation

| Field | Required | Validation |
|-------|----------|------------|
| `name` | Yes | Non-empty, unique, valid identifier |
| `table` | Yes | Format: `schema.table` or `table` (assumes public) |
| `operation` | Yes | One of: INSERT, UPDATE, DELETE |
| `destination` | Yes | Must reference existing destination name |
| `trigger_on` | No | Non-empty column names (only valid for UPDATE) |
| `select` | No | Non-empty column names |
| `enabled` | No | Boolean (default: true) |

### Cross-Validation

```go
func (c *BeaconConfig) Validate() error {
    destNames := make(map[string]bool)
    subNames := make(map[string]bool)

    // Validate destinations
    for i, dest := range c.Destinations {
        if dest.Name == "" {
            return fmt.Errorf("destinations[%d]: name is required", i)
        }
        if destNames[dest.Name] {
            return fmt.Errorf("destinations[%d]: duplicate name %q", i, dest.Name)
        }
        destNames[dest.Name] = true

        if _, err := url.Parse(dest.URL); err != nil {
            return fmt.Errorf("destinations[%d]: invalid URL: %w", i, err)
        }
    }

    // Validate subscriptions
    for i, sub := range c.Subscriptions {
        if sub.Name == "" {
            return fmt.Errorf("subscriptions[%d]: name is required", i)
        }
        if subNames[sub.Name] {
            return fmt.Errorf("subscriptions[%d]: duplicate name %q", i, sub.Name)
        }
        subNames[sub.Name] = true

        if !destNames[sub.Destination] {
            return fmt.Errorf("subscriptions[%d]: unknown destination %q", i, sub.Destination)
        }

        if len(sub.TriggerOn) > 0 && sub.Operation != "UPDATE" {
            return fmt.Errorf("subscriptions[%d]: trigger_on only valid for UPDATE", i)
        }
    }

    return nil
}
```

---

## Environment Variables

### Required

| Variable | Description |
|----------|-------------|
| `DATABASE_URL` | PostgreSQL connection string |

### Optional (with defaults)

| Variable | Default | Description |
|----------|---------|-------------|
| `BEACON_HTTP_ADDR` | `:8080` | Control plane listen address |
| `BEACON_POLL_INTERVAL` | `100ms` | Outbox poll interval |
| `BEACON_BATCH_SIZE` | `100` | Events claimed per poll |
| `BEACON_WORKER_COUNT` | `10` | Concurrent delivery workers |
| `BEACON_LOG_LEVEL` | `info` | Log level (debug, info, warn, error) |
| `BEACON_LOG_FORMAT` | `json` | Log format (json, text) |
| `BEACON_MAX_PAYLOAD_BYTES` | `1048576` | Maximum payload size (1MB) |

### Secret Variables

| Variable | Required | Description |
|----------|----------|-------------|
| `BEACON_HMAC_SECRET` | No | Global HMAC signing secret for all webhook requests |
| `BEACON_CONTROLPLANE_SECRET` | Yes | Bearer token for control plane API authentication |

```go
func LoadHMACSecret() []byte {
    secret := os.Getenv("BEACON_HMAC_SECRET")
    if secret == "" {
        return nil  // No secret configured (signing disabled)
    }
    return []byte(secret)
}

func LoadControlPlaneSecret() (string, error) {
    secret := os.Getenv("BEACON_CONTROLPLANE_SECRET")
    if secret == "" {
        return "", errors.New("BEACON_CONTROLPLANE_SECRET is required")
    }
    return secret, nil
}
```

---

## Table Name Parsing

```go
func ParseTable(table string) (schema, name string) {
    parts := strings.SplitN(table, ".", 2)
    if len(parts) == 2 {
        return parts[0], parts[1]
    }
    return "public", parts[0]
}
```

| Input | Schema | Name |
|-------|--------|------|
| `public.users` | `public` | `users` |
| `users` | `public` | `users` |
| `sales.orders` | `sales` | `orders` |
| `my_schema.my_table` | `my_schema` | `my_table` |

---

## Loading Implementation

```go
func LoadEnv() (*EnvConfig, error) {
    cfg := &EnvConfig{
        DatabaseURL:  os.Getenv("DATABASE_URL"),
        HTTPAddr:     getEnvOr("BEACON_HTTP_ADDR", ":8080"),
        PollInterval: parseDurationOr("BEACON_POLL_INTERVAL", 100*time.Millisecond),
        BatchSize:    parseIntOr("BEACON_BATCH_SIZE", 100),
        WorkerCount:  parseIntOr("BEACON_WORKER_COUNT", 10),
    }

    if cfg.DatabaseURL == "" {
        return nil, errors.New("DATABASE_URL is required")
    }

    return cfg, nil
}

func getEnvOr(key, defaultValue string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return defaultValue
}
```

---

## Dependencies

- `gopkg.in/yaml.v3` - YAML parsing
- Standard library (`os`, `strings`, `time`, `net/url`)

---

## Testing

### Strategy

**Pure unit tests only**—no database or HTTP required. Tests cover YAML parsing, validation rules, and environment variable loading.

### Parse and Validate Tests

```go
// internal/config/parse_test.go

package config_test

import (
    "testing"

    "beacon/internal/config"
    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestParse_ValidConfig(t *testing.T) {
    yaml := `
version: 1
destinations:
  - name: webhook
    url: https://example.com/hook
    method: POST
    timeout_ms: 5000
    max_in_flight: 50
subscriptions:
  - name: users-insert
    table: users
    operation: INSERT
    destination: webhook
`
    cfg, err := config.Parse([]byte(yaml))
    require.NoError(t, err)
    require.NotNil(t, cfg)

    assert.Equal(t, 1, cfg.Version)
    assert.Len(t, cfg.Destinations, 1)
    assert.Len(t, cfg.Subscriptions, 1)

    assert.Equal(t, "webhook", cfg.Destinations[0].Name)
    assert.Equal(t, "https://example.com/hook", cfg.Destinations[0].URL)
    assert.Equal(t, 5000, cfg.Destinations[0].TimeoutMs)
    assert.Equal(t, 50, cfg.Destinations[0].MaxInFlight)

    assert.Equal(t, "users-insert", cfg.Subscriptions[0].Name)
    assert.Equal(t, "users", cfg.Subscriptions[0].Table)
    assert.Equal(t, "INSERT", cfg.Subscriptions[0].Operation)
}

func TestParse_Defaults(t *testing.T) {
    yaml := `
version: 1
destinations:
  - name: webhook
    url: https://example.com/hook
subscriptions:
  - name: test
    table: users
    operation: INSERT
    destination: webhook
`
    cfg, err := config.Parse([]byte(yaml))
    require.NoError(t, err)

    // Check defaults applied
    assert.Equal(t, "POST", cfg.Destinations[0].Method)
    assert.Equal(t, 5000, cfg.Destinations[0].TimeoutMs)
    assert.Equal(t, 50, cfg.Destinations[0].MaxInFlight)
}

func TestParse_InvalidYAML(t *testing.T) {
    yaml := `
version: 1
destinations:
  - name: invalid
    url: not: valid: yaml
`
    _, err := config.Parse([]byte(yaml))
    assert.Error(t, err)
}

func TestParse_EmptyConfig(t *testing.T) {
    _, err := config.Parse([]byte(""))
    assert.Error(t, err)
}

func TestParse_UnsupportedVersion(t *testing.T) {
    yaml := `version: 999`
    _, err := config.Parse([]byte(yaml))
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "version")
}
```

### Validation Tests

```go
// internal/config/validate_test.go

func TestValidate_MissingDestinationName(t *testing.T) {
    cfg := &config.BeaconConfig{
        Version: 1,
        Destinations: []config.DestinationConfig{
            {URL: "https://example.com"},
        },
    }

    err := cfg.Validate()
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "name")
}

func TestValidate_InvalidURL(t *testing.T) {
    cfg := &config.BeaconConfig{
        Version: 1,
        Destinations: []config.DestinationConfig{
            {Name: "test", URL: "not-a-url"},
        },
    }

    err := cfg.Validate()
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "url")
}

func TestValidate_HTTPURLAllowed(t *testing.T) {
    cfg := &config.BeaconConfig{
        Version: 1,
        Destinations: []config.DestinationConfig{
            {Name: "test", URL: "http://internal.local/hook"},
        },
    }

    err := cfg.Validate()
    assert.NoError(t, err)
}

func TestValidate_DuplicateDestinationNames(t *testing.T) {
    cfg := &config.BeaconConfig{
        Version: 1,
        Destinations: []config.DestinationConfig{
            {Name: "dupe", URL: "https://a.com"},
            {Name: "dupe", URL: "https://b.com"},
        },
    }

    err := cfg.Validate()
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "duplicate")
}

func TestValidate_InvalidOperation(t *testing.T) {
    cfg := &config.BeaconConfig{
        Version: 1,
        Destinations: []config.DestinationConfig{
            {Name: "dest", URL: "https://example.com"},
        },
        Subscriptions: []config.SubscriptionConfig{
            {Name: "sub", Table: "users", Operation: "INVALID", Destination: "dest"},
        },
    }

    err := cfg.Validate()
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "operation")
}

func TestValidate_UnknownDestination(t *testing.T) {
    cfg := &config.BeaconConfig{
        Version: 1,
        Destinations: []config.DestinationConfig{
            {Name: "real", URL: "https://example.com"},
        },
        Subscriptions: []config.SubscriptionConfig{
            {Name: "sub", Table: "users", Operation: "INSERT", Destination: "fake"},
        },
    }

    err := cfg.Validate()
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "destination")
}

func TestValidate_TableWithSchema(t *testing.T) {
    cfg := &config.BeaconConfig{
        Version: 1,
        Destinations: []config.DestinationConfig{
            {Name: "dest", URL: "https://example.com"},
        },
        Subscriptions: []config.SubscriptionConfig{
            {Name: "sub", Table: "myschema.users", Operation: "INSERT", Destination: "dest"},
        },
    }

    err := cfg.Validate()
    assert.NoError(t, err)
}

func TestValidate_TriggerOn(t *testing.T) {
    cfg := &config.BeaconConfig{
        Version: 1,
        Destinations: []config.DestinationConfig{
            {Name: "dest", URL: "https://example.com"},
        },
        Subscriptions: []config.SubscriptionConfig{
            {
                Name:        "sub",
                Table:       "users",
                Operation:   "UPDATE",
                Destination: "dest",
                TriggerOn:   []string{"email", "status"},
            },
        },
    }

    err := cfg.Validate()
    assert.NoError(t, err)
}

func TestValidate_TriggerOnInsert(t *testing.T) {
    // trigger_on only makes sense for UPDATE
    cfg := &config.BeaconConfig{
        Version: 1,
        Destinations: []config.DestinationConfig{
            {Name: "dest", URL: "https://example.com"},
        },
        Subscriptions: []config.SubscriptionConfig{
            {
                Name:        "sub",
                Table:       "users",
                Operation:   "INSERT",
                Destination: "dest",
                TriggerOn:   []string{"email"},
            },
        },
    }

    err := cfg.Validate()
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "trigger_on")
}
```

### Environment Variable Tests

```go
// internal/config/load_test.go

func TestLoadFromEnv(t *testing.T) {
    t.Setenv("DATABASE_URL", "postgres://localhost/beacon")
    t.Setenv("BEACON_HTTP_ADDR", ":9090")
    t.Setenv("BEACON_POLL_INTERVAL", "200ms")
    t.Setenv("BEACON_BATCH_SIZE", "50")
    t.Setenv("BEACON_WORKER_COUNT", "20")
    t.Setenv("BEACON_CONTROLPLANE_SECRET", "test-secret")

    cfg, err := config.LoadFromEnv()
    require.NoError(t, err)

    assert.Equal(t, "postgres://localhost/beacon", cfg.DatabaseURL)
    assert.Equal(t, ":9090", cfg.HTTPAddr)
    assert.Equal(t, 200*time.Millisecond, cfg.PollInterval)
    assert.Equal(t, 50, cfg.BatchSize)
    assert.Equal(t, 20, cfg.WorkerCount)
}

func TestLoadFromEnv_Defaults(t *testing.T) {
    t.Setenv("DATABASE_URL", "postgres://localhost/beacon")
    t.Setenv("BEACON_CONTROLPLANE_SECRET", "test-secret")
    // Don't set optional vars

    cfg, err := config.LoadFromEnv()
    require.NoError(t, err)

    // Check defaults
    assert.Equal(t, ":8080", cfg.HTTPAddr)
    assert.Equal(t, 100*time.Millisecond, cfg.PollInterval)
    assert.Equal(t, 100, cfg.BatchSize)
    assert.Equal(t, 10, cfg.WorkerCount)
}

func TestLoadFromEnv_MissingRequired(t *testing.T) {
    // Clear required vars
    t.Setenv("DATABASE_URL", "")
    t.Setenv("BEACON_CONTROLPLANE_SECRET", "")

    _, err := config.LoadFromEnv()
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "DATABASE_URL")
}

func TestLoadFromEnv_InvalidDuration(t *testing.T) {
    t.Setenv("DATABASE_URL", "postgres://localhost/beacon")
    t.Setenv("BEACON_CONTROLPLANE_SECRET", "test-secret")
    t.Setenv("BEACON_POLL_INTERVAL", "not-a-duration")

    _, err := config.LoadFromEnv()
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "BEACON_POLL_INTERVAL")
}

func TestLoadHMACSecret(t *testing.T) {
    t.Setenv("BEACON_HMAC_SECRET", "my-signing-key")

    secret := config.LoadHMACSecret()
    assert.Equal(t, []byte("my-signing-key"), secret)
}

func TestLoadHMACSecret_Empty(t *testing.T) {
    t.Setenv("BEACON_HMAC_SECRET", "")

    secret := config.LoadHMACSecret()
    assert.Nil(t, secret)
}
```

### Running Tests

```bash
# Run config tests (fast - pure unit tests)
go test ./internal/config/... -v

# Run with coverage
go test ./internal/config/... -cover
```

---

## Usage Example

### Parsing YAML

```go
yamlData := []byte(`
version: 1
destinations:
  - name: webhook
    url: https://example.com/hook
subscriptions:
  - name: users-insert
    table: users
    operation: INSERT
    destination: webhook
`)

cfg, err := config.Parse(yamlData)
if err != nil {
    log.Fatal("parse error:", err)
}

if err := cfg.Validate(); err != nil {
    log.Fatal("validation error:", err)
}
```

### Loading Environment

```go
envCfg, err := config.LoadEnv()
if err != nil {
    log.Fatal(err)
}

pool, err := db.New(ctx, envCfg.DatabaseURL)
dispatcher := dispatcher.New(pool, repo, client, dispatcher.Config{
    PollInterval: envCfg.PollInterval,
    BatchSize:    envCfg.BatchSize,
    WorkerCount:  envCfg.WorkerCount,
})
```

### Loading Secrets

```go
for _, dest := range cfg.Destinations {
    secret, err := config.LoadSecret(dest.Name)
    if err != nil {
        log.Fatal(err)
    }
    // secret is nil if BEACON_SECRET_<NAME> not set
    // secret is []byte if set
}
```

---

## Design Rationale

### Why YAML via API, not disk?

1. **Atomic updates:** API can validate and apply in a transaction
2. **No file sync issues:** Multiple replicas don't need shared filesystem
3. **Audit trail:** API can log who changed what
4. **Validation:** Reject invalid config before it's stored

### Why a global signing secret?

1. **Simplicity:** One secret to manage, rotate, and distribute
2. **Security:** Secret never appears in config files or logs
3. **12-factor:** Standard approach for containerized apps
4. **Secret managers:** Easy to inject from Vault, AWS Secrets Manager, etc.
5. **Verification:** Consumers only need one secret to verify any webhook

### Why name-based references?

Subscriptions reference destinations by `name` (not UUID) because:
1. **Human-readable:** YAML is easier to write and review
2. **Stable:** UUIDs change on recreate; names can stay the same
3. **Validation:** Can validate references at parse time
