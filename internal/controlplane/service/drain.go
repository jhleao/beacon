package service

import (
	"context"
	"log/slog"
	"time"

	"beacon/internal/db"

	"github.com/google/uuid"
)

// DrainCheckInterval is how often to check draining subscriptions.
const DrainCheckInterval = 10 * time.Second

// DrainService handles async subscription draining.
type DrainService struct {
	pool   *db.Pool
	logger *slog.Logger
}

// NewDrainService creates a DrainService.
func NewDrainService(pool *db.Pool, logger *slog.Logger) *DrainService {
	return &DrainService{
		pool:   pool,
		logger: logger,
	}
}

// StartDrain marks a subscription as draining.
func (s *DrainService) StartDrain(ctx context.Context, subID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE beacon.subscriptions
		SET draining = true
		WHERE id = $1
	`, subID)
	return err
}

// CheckDrainComplete checks if a draining subscription can be deleted.
func (s *DrainService) CheckDrainComplete(ctx context.Context, subID uuid.UUID) (bool, error) {
	var pending int64
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM beacon.outbox_events
		WHERE subscription_id = $1
		  AND state IN ('pending', 'delivering')
	`, subID).Scan(&pending)
	if err != nil {
		return false, err
	}
	return pending == 0, nil
}

// RunDrainLoop periodically checks and finalizes draining subscriptions.
func (s *DrainService) RunDrainLoop(ctx context.Context) error {
	ticker := time.NewTicker(DrainCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			s.checkDrainingSubscriptions(ctx)
		}
	}
}

func (s *DrainService) checkDrainingSubscriptions(ctx context.Context) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name FROM beacon.subscriptions
		WHERE draining = true AND deleted_at IS NULL
	`)
	if err != nil {
		s.logger.Error("failed to query draining subscriptions", "error", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var subID uuid.UUID
		var name string
		if err := rows.Scan(&subID, &name); err != nil {
			continue
		}

		complete, err := s.CheckDrainComplete(ctx, subID)
		if err != nil {
			s.logger.Error("failed to check drain complete", "subscription", name, "error", err)
			continue
		}

		if complete {
			_, err := s.pool.Exec(ctx, `
				UPDATE beacon.subscriptions
				SET deleted_at = now()
				WHERE id = $1
			`, subID)
			if err != nil {
				s.logger.Error("failed to finalize subscription delete", "subscription", name, "error", err)
				continue
			}
			s.logger.Info("subscription drain complete", "subscription", name)
		}
	}
}
