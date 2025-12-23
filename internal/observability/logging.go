package observability

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

// NewLogger creates a logger with the specified level and format.
func NewLogger(level, format string) (*slog.Logger, error) {
	return NewLoggerWithOutput(level, format, os.Stdout)
}

// NewLoggerWithOutput creates a logger with custom output.
func NewLoggerWithOutput(level, format string, w io.Writer) (*slog.Logger, error) {
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
		handler = slog.NewJSONHandler(w, opts)
	case "text":
		handler = slog.NewTextHandler(w, opts)
	default:
		return nil, fmt.Errorf("unknown log format: %s", format)
	}

	return slog.New(handler), nil
}
