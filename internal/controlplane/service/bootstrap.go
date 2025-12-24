package service

import (
	"context"
	"fmt"
	"os"

	"beacon/internal/config"
	"beacon/internal/db"
)

// BootstrapService handles automatic seed config application.
type BootstrapService struct {
	pool     *db.Pool
	applySvc *ApplyService
}

// NewBootstrapService creates a BootstrapService.
func NewBootstrapService(pool *db.Pool, applySvc *ApplyService) *BootstrapService {
	return &BootstrapService{
		pool:     pool,
		applySvc: applySvc,
	}
}

// BootstrapResult describes the bootstrap outcome.
type BootstrapResult struct {
	Applied     bool         `json:"applied"`
	Skipped     bool         `json:"skipped"`
	SkipReason  string       `json:"skip_reason,omitempty"`
	ApplyResult *ApplyResult `json:"apply_result,omitempty"`
}

// Bootstrap applies seed config if the database is completely clean.
// Returns nil result if no seed config path is provided.
func (s *BootstrapService) Bootstrap(ctx context.Context, seedConfigPath string) (*BootstrapResult, error) {
	if seedConfigPath == "" {
		return nil, nil
	}

	// Check if database is clean
	clean, err := s.isDatabaseClean(ctx)
	if err != nil {
		return nil, fmt.Errorf("check database state: %w", err)
	}

	if !clean {
		return &BootstrapResult{
			Skipped:    true,
			SkipReason: "database already contains destinations or subscriptions",
		}, nil
	}

	// Read seed config file
	data, err := os.ReadFile(seedConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read seed config file: %w", err)
	}

	// Parse and validate config
	cfg, err := config.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse seed config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate seed config: %w", err)
	}

	// Apply the config
	applyResult, err := s.applySvc.Apply(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("apply seed config: %w", err)
	}

	return &BootstrapResult{
		Applied:     true,
		ApplyResult: applyResult,
	}, nil
}

// isDatabaseClean checks if there are zero destinations and zero subscriptions.
func (s *BootstrapService) isDatabaseClean(ctx context.Context) (bool, error) {
	var destCount, subCount int

	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM beacon.destinations`).Scan(&destCount)
	if err != nil {
		return false, fmt.Errorf("count destinations: %w", err)
	}

	if destCount > 0 {
		return false, nil
	}

	err = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM beacon.subscriptions WHERE deleted_at IS NULL`).Scan(&subCount)
	if err != nil {
		return false, fmt.Errorf("count subscriptions: %w", err)
	}

	return subCount == 0, nil
}
