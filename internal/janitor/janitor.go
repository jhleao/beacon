// Package janitor provides automatic cleanup of old delivered events.
package janitor

import (
	"context"
	"log/slog"
	"time"

	"beacon/internal/observability"
	"beacon/internal/outbox"
)

// Config holds janitor configuration.
type Config struct {
	RetentionDuration time.Duration
	Interval          time.Duration
	BatchSize         int
}

// Janitor periodically cleans up old delivered events.
type Janitor struct {
	repo    *outbox.Repository
	config  Config
	logger  *slog.Logger
	metrics *observability.Metrics
}

// New creates a Janitor.
func New(repo *outbox.Repository, config Config, logger *slog.Logger, metrics *observability.Metrics) *Janitor {
	return &Janitor{
		repo:    repo,
		config:  config,
		logger:  logger.With("component", "janitor"),
		metrics: metrics,
	}
}

// Run starts the janitor loop. Blocks until context is cancelled.
func (j *Janitor) Run(ctx context.Context) {
	j.logger.Info("janitor started",
		"retention", j.config.RetentionDuration,
		"interval", j.config.Interval,
		"batch_size", j.config.BatchSize,
	)

	// Run once at startup
	j.cleanup(ctx)

	ticker := time.NewTicker(j.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			j.logger.Info("janitor stopped")
			return
		case <-ticker.C:
			j.cleanup(ctx)
		}
	}
}

// cleanup performs a single cleanup cycle.
func (j *Janitor) cleanup(ctx context.Context) {
	cleaned, err := j.repo.CleanupDelivered(ctx, j.config.RetentionDuration, j.config.BatchSize)
	if err != nil {
		j.logger.Error("cleanup failed", "error", err)
		return
	}

	if cleaned > 0 {
		j.logger.Info("cleaned old events", "count", cleaned)
		j.metrics.EventsCleaned(cleaned)
	}
}
