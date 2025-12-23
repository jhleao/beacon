// Package config handles YAML configuration parsing and environment variable loading.
package config

// BeaconConfig is the root YAML configuration.
type BeaconConfig struct {
	Version       int                  `yaml:"version"`
	Destinations  []DestinationConfig  `yaml:"destinations"`
	Subscriptions []SubscriptionConfig `yaml:"subscriptions"`
}

// DestinationConfig defines a webhook endpoint.
type DestinationConfig struct {
	Name        string            `yaml:"name"`
	URL         string            `yaml:"url"`
	Method      string            `yaml:"method,omitempty"`
	TimeoutMs   int               `yaml:"timeout_ms,omitempty"`
	MaxInFlight int               `yaml:"max_in_flight,omitempty"`
	Headers     map[string]string `yaml:"headers,omitempty"`
	SSRFPolicy  *SSRFPolicy       `yaml:"ssrf_policy,omitempty"`
}

// SubscriptionConfig defines what to capture and where to send.
type SubscriptionConfig struct {
	Name        string   `yaml:"name"`
	Table       string   `yaml:"table"`
	Operation   string   `yaml:"operation"`
	Destination string   `yaml:"destination"`
	TriggerOn   []string `yaml:"trigger_on,omitempty"`
	Select      []string `yaml:"select,omitempty"`
	Enabled     *bool    `yaml:"enabled,omitempty"`
}

// SSRFPolicy configures SSRF protection overrides.
type SSRFPolicy struct {
	AllowPrivate bool     `yaml:"allow_private,omitempty" json:"allow_private,omitempty"`
	AllowedHosts []string `yaml:"allowed_hosts,omitempty" json:"allowed_hosts,omitempty"`
}

// IsEnabled returns whether the subscription is enabled (defaults to true).
func (s *SubscriptionConfig) IsEnabled() bool {
	if s.Enabled == nil {
		return true
	}
	return *s.Enabled
}
