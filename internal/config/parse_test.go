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
}

func TestValidate_HTTPURLAllowed(t *testing.T) {
	cfg := &config.BeaconConfig{
		Version: 1,
		Destinations: []config.DestinationConfig{
			{Name: "test", URL: "http://internal.local/hook", Method: "POST", TimeoutMs: 5000, MaxInFlight: 50},
		},
	}

	err := cfg.Validate()
	assert.NoError(t, err)
}

func TestValidate_DuplicateDestinationNames(t *testing.T) {
	cfg := &config.BeaconConfig{
		Version: 1,
		Destinations: []config.DestinationConfig{
			{Name: "dupe", URL: "https://a.com", Method: "POST", TimeoutMs: 5000, MaxInFlight: 50},
			{Name: "dupe", URL: "https://b.com", Method: "POST", TimeoutMs: 5000, MaxInFlight: 50},
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
			{Name: "dest", URL: "https://example.com", Method: "POST", TimeoutMs: 5000, MaxInFlight: 50},
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
			{Name: "real", URL: "https://example.com", Method: "POST", TimeoutMs: 5000, MaxInFlight: 50},
		},
		Subscriptions: []config.SubscriptionConfig{
			{Name: "sub", Table: "users", Operation: "INSERT", Destination: "fake"},
		},
	}

	err := cfg.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "destination")
}

func TestValidate_TriggerOnOnlyForUpdate(t *testing.T) {
	cfg := &config.BeaconConfig{
		Version: 1,
		Destinations: []config.DestinationConfig{
			{Name: "dest", URL: "https://example.com", Method: "POST", TimeoutMs: 5000, MaxInFlight: 50},
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

func TestParseTable(t *testing.T) {
	tests := []struct {
		input          string
		expectedSchema string
		expectedName   string
	}{
		{"public.users", "public", "users"},
		{"users", "public", "users"},
		{"sales.orders", "sales", "orders"},
		{"my_schema.my_table", "my_schema", "my_table"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			schema, name := config.ParseTable(tt.input)
			assert.Equal(t, tt.expectedSchema, schema)
			assert.Equal(t, tt.expectedName, name)
		})
	}
}
