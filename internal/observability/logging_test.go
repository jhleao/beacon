package observability_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"beacon/internal/observability"

	"github.com/stretchr/testify/assert"
)

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
	var buf bytes.Buffer
	logger, _ := observability.NewLoggerWithOutput("info", "json", &buf)

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
