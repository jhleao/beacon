package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// EnvConfig holds runtime environment configuration.
type EnvConfig struct {
	DatabaseURL          string
	HTTPAddr             string
	PollInterval         time.Duration
	BatchSize            int
	WorkerCount          int
	LogLevel             string
	LogFormat            string
	MaxPayloadBytes      int
	ControlPlaneSecret   string

	// Janitor settings
	RetentionHours    int
	JanitorInterval   time.Duration
	JanitorBatchSize  int

	// Seed config for automatic bootstrapping
	SeedConfigPath string
}

// LoadEnv loads configuration from environment variables.
func LoadEnv() (*EnvConfig, error) {
	cfg := &EnvConfig{
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		HTTPAddr:           getEnvOr("BEACON_HTTP_ADDR", ":8080"),
		PollInterval:       parseDurationOr("BEACON_POLL_INTERVAL", 100*time.Millisecond),
		BatchSize:          parseIntOr("BEACON_BATCH_SIZE", 100),
		WorkerCount:        parseIntOr("BEACON_WORKER_COUNT", 10),
		LogLevel:           getEnvOr("BEACON_LOG_LEVEL", "info"),
		LogFormat:          getEnvOr("BEACON_LOG_FORMAT", "json"),
		MaxPayloadBytes:    parseIntOr("BEACON_MAX_PAYLOAD_BYTES", 1048576),
		ControlPlaneSecret: os.Getenv("BEACON_CONTROLPLANE_SECRET"),

		// Janitor defaults: 7 days retention, run hourly, 1000 events per batch
		RetentionHours:   parseIntOr("BEACON_RETENTION_HOURS", 168),
		JanitorInterval:  parseDurationOr("BEACON_JANITOR_INTERVAL", 1*time.Hour),
		JanitorBatchSize: parseIntOr("BEACON_JANITOR_BATCH_SIZE", 1000),

		// Seed config path (optional, for auto-bootstrapping)
		SeedConfigPath: os.Getenv("BEACON_SEED_CONFIG_PATH"),
	}

	// Validate required fields
	if cfg.DatabaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	if cfg.ControlPlaneSecret == "" {
		return nil, errors.New("BEACON_CONTROLPLANE_SECRET is required")
	}

	// Validate poll interval
	if v := os.Getenv("BEACON_POLL_INTERVAL"); v != "" {
		if _, err := time.ParseDuration(v); err != nil {
			return nil, fmt.Errorf("invalid BEACON_POLL_INTERVAL: %w", err)
		}
	}

	return cfg, nil
}

// LoadHMACSecret loads the global HMAC signing secret from environment.
// Returns nil if not configured (signing disabled).
func LoadHMACSecret() []byte {
	secret := os.Getenv("BEACON_HMAC_SECRET")
	if secret == "" {
		return nil
	}
	return []byte(secret)
}

func getEnvOr(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
}

func parseDurationOr(key string, defaultValue time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return defaultValue
	}
	return d
}

func parseIntOr(key string, defaultValue int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultValue
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return defaultValue
	}
	return i
}
