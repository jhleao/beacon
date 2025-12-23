package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	validNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	validMethods   = map[string]bool{
		"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
	}
	validOperations = map[string]bool{
		"INSERT": true, "UPDATE": true, "DELETE": true,
	}
)

// Parse parses YAML config bytes into BeaconConfig.
func Parse(data []byte) (*BeaconConfig, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty configuration")
	}

	var cfg BeaconConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}

	// Check version
	if cfg.Version != 1 {
		return nil, fmt.Errorf("unsupported config version: %d (expected 1)", cfg.Version)
	}

	// Apply defaults
	applyDefaults(&cfg)

	return &cfg, nil
}

func applyDefaults(cfg *BeaconConfig) {
	for i := range cfg.Destinations {
		dest := &cfg.Destinations[i]
		if dest.Method == "" {
			dest.Method = "POST"
		}
		if dest.TimeoutMs == 0 {
			dest.TimeoutMs = 5000
		}
		if dest.MaxInFlight == 0 {
			dest.MaxInFlight = 50
		}
	}
}

// Validate checks the config for errors.
func (c *BeaconConfig) Validate() error {
	destNames := make(map[string]bool)
	subNames := make(map[string]bool)

	// Validate destinations
	for i, dest := range c.Destinations {
		if dest.Name == "" {
			return fmt.Errorf("destinations[%d]: name is required", i)
		}
		if !validNameRegex.MatchString(dest.Name) {
			return fmt.Errorf("destinations[%d]: invalid name %q (must be lowercase alphanumeric with hyphens/underscores)", i, dest.Name)
		}
		if destNames[dest.Name] {
			return fmt.Errorf("destinations[%d]: duplicate name %q", i, dest.Name)
		}
		destNames[dest.Name] = true

		if dest.URL == "" {
			return fmt.Errorf("destinations[%d]: url is required", i)
		}
		parsedURL, err := url.Parse(dest.URL)
		if err != nil {
			return fmt.Errorf("destinations[%d]: invalid url: %w", i, err)
		}
		if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
			return fmt.Errorf("destinations[%d]: url scheme must be http or https", i)
		}

		if !validMethods[dest.Method] {
			return fmt.Errorf("destinations[%d]: invalid method %q", i, dest.Method)
		}

		if dest.TimeoutMs <= 0 || dest.TimeoutMs > 60000 {
			return fmt.Errorf("destinations[%d]: timeout_ms must be between 1 and 60000", i)
		}

		if dest.MaxInFlight <= 0 || dest.MaxInFlight > 1000 {
			return fmt.Errorf("destinations[%d]: max_in_flight must be between 1 and 1000", i)
		}
	}

	// Validate subscriptions
	for i, sub := range c.Subscriptions {
		if sub.Name == "" {
			return fmt.Errorf("subscriptions[%d]: name is required", i)
		}
		if !validNameRegex.MatchString(sub.Name) {
			return fmt.Errorf("subscriptions[%d]: invalid name %q", i, sub.Name)
		}
		if subNames[sub.Name] {
			return fmt.Errorf("subscriptions[%d]: duplicate name %q", i, sub.Name)
		}
		subNames[sub.Name] = true

		if sub.Table == "" {
			return fmt.Errorf("subscriptions[%d]: table is required", i)
		}

		if !validOperations[sub.Operation] {
			return fmt.Errorf("subscriptions[%d]: invalid operation %q (must be INSERT, UPDATE, or DELETE)", i, sub.Operation)
		}

		if !destNames[sub.Destination] {
			return fmt.Errorf("subscriptions[%d]: unknown destination %q", i, sub.Destination)
		}

		if len(sub.TriggerOn) > 0 && sub.Operation != "UPDATE" {
			return fmt.Errorf("subscriptions[%d]: trigger_on is only valid for UPDATE operations", i)
		}
	}

	return nil
}

// ParseTable splits "schema.table" into components.
func ParseTable(table string) (schema, name string) {
	parts := strings.SplitN(table, ".", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "public", parts[0]
}
