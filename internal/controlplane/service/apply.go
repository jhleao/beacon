// Package service provides the business logic for the control plane.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"beacon/internal/capture"
	"beacon/internal/config"
	"beacon/internal/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ApplyService handles YAML config application.
type ApplyService struct {
	pool      *db.Pool
	installer *capture.Installer
	logger    *slog.Logger
}

// NewApplyService creates an ApplyService.
func NewApplyService(pool *db.Pool, installer *capture.Installer, logger *slog.Logger) *ApplyService {
	return &ApplyService{
		pool:      pool,
		installer: installer,
		logger:    logger.With("component", "apply"),
	}
}

// ApplyResult describes changes made by Apply.
type ApplyResult struct {
	Destinations  ChangeSet `json:"destinations"`
	Subscriptions ChangeSet `json:"subscriptions"`
	Triggers      ChangeSet `json:"triggers"`
}

// ChangeSet tracks what was created, updated, deleted.
type ChangeSet struct {
	Created []string `json:"created,omitempty"`
	Updated []string `json:"updated,omitempty"`
	Deleted []string `json:"deleted,omitempty"`
}

// Apply applies a configuration, returning changes made.
func (s *ApplyService) Apply(ctx context.Context, cfg *config.BeaconConfig) (*ApplyResult, error) {
	s.logger.Debug("applying configuration",
		"destinations", len(cfg.Destinations),
		"subscriptions", len(cfg.Subscriptions),
	)

	var result ApplyResult

	err := s.pool.WithTx(ctx, func(tx pgx.Tx) error {
		// Lock destinations table to prevent concurrent modifications
		if _, err := tx.Exec(ctx, `SELECT 1 FROM beacon.destinations FOR UPDATE`); err != nil {
			// Table might be empty, that's fine
		}

		// 1. Load current state
		currentDests, err := s.loadDestinations(ctx, tx)
		if err != nil {
			return fmt.Errorf("load destinations: %w", err)
		}

		currentSubs, err := s.loadSubscriptions(ctx, tx)
		if err != nil {
			return fmt.Errorf("load subscriptions: %w", err)
		}

		// 2. Apply destination changes
		destIDMap, destChanges, err := s.applyDestinations(ctx, tx, currentDests, cfg.Destinations)
		if err != nil {
			return fmt.Errorf("apply destinations: %w", err)
		}
		result.Destinations = destChanges

		// 3. Apply subscription changes
		subChanges, err := s.applySubscriptions(ctx, tx, currentSubs, cfg.Subscriptions, destIDMap)
		if err != nil {
			return fmt.Errorf("apply subscriptions: %w", err)
		}
		result.Subscriptions = subChanges

		return nil
	})

	if err != nil {
		return nil, err
	}

	// 4. Reconcile triggers (outside transaction for DDL)
	triggerChanges, err := s.reconcileTriggers(ctx)
	if err != nil {
		return &result, fmt.Errorf("reconcile triggers: %w", err)
	}
	result.Triggers = triggerChanges

	s.logger.Info("configuration applied",
		"destinations_created", len(result.Destinations.Created),
		"destinations_updated", len(result.Destinations.Updated),
		"destinations_deleted", len(result.Destinations.Deleted),
		"subscriptions_created", len(result.Subscriptions.Created),
		"subscriptions_updated", len(result.Subscriptions.Updated),
		"subscriptions_deleted", len(result.Subscriptions.Deleted),
		"triggers_created", len(result.Triggers.Created),
		"triggers_deleted", len(result.Triggers.Deleted),
	)

	return &result, nil
}

// DryRun computes what Apply would do without making changes.
func (s *ApplyService) DryRun(ctx context.Context, cfg *config.BeaconConfig) (*ApplyResult, error) {
	s.logger.Debug("dry run configuration",
		"destinations", len(cfg.Destinations),
		"subscriptions", len(cfg.Subscriptions),
	)

	var result ApplyResult

	// Load current state (read-only)
	currentDests, err := s.loadDestinations(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("load destinations: %w", err)
	}

	currentSubs, err := s.loadSubscriptions(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("load subscriptions: %w", err)
	}

	// Compute destination diffs
	destIDMap := make(map[string]uuid.UUID)
	for name, dest := range currentDests {
		destIDMap[name] = dest.ID
	}

	result.Destinations = s.diffDestinations(currentDests, cfg.Destinations)

	// For new destinations, generate placeholder IDs
	for _, d := range cfg.Destinations {
		if _, ok := destIDMap[d.Name]; !ok {
			destIDMap[d.Name] = uuid.New()
		}
	}

	result.Subscriptions = s.diffSubscriptions(currentSubs, cfg.Subscriptions, destIDMap)

	// Compute trigger diffs
	tablesNeeded := make(map[capture.TableRef]bool)
	for _, sub := range cfg.Subscriptions {
		if sub.IsEnabled() {
			schema, name := config.ParseTable(sub.Table)
			tablesNeeded[capture.TableRef{Schema: schema, Name: name}] = true
		}
	}

	tablesWithTriggers, _ := s.installer.ListTriggersMap(ctx)

	for table := range tablesNeeded {
		if !tablesWithTriggers[table] {
			result.Triggers.Created = append(result.Triggers.Created, table.String())
		}
	}
	for table := range tablesWithTriggers {
		if !tablesNeeded[table] {
			result.Triggers.Deleted = append(result.Triggers.Deleted, table.String())
		}
	}

	return &result, nil
}

// Export returns the current configuration as BeaconConfig.
func (s *ApplyService) Export(ctx context.Context) (*config.BeaconConfig, error) {
	cfg := &config.BeaconConfig{
		Version: 1,
	}

	// Load destinations
	rows, err := s.pool.Query(ctx, `
		SELECT name, url, method, timeout_ms, max_in_flight, headers, ssrf_policy
		FROM beacon.destinations
		ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("query destinations: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var dest config.DestinationConfig
		var headers, ssrfPolicy []byte

		err := rows.Scan(
			&dest.Name, &dest.URL, &dest.Method,
			&dest.TimeoutMs, &dest.MaxInFlight, &headers, &ssrfPolicy,
		)
		if err != nil {
			return nil, fmt.Errorf("scan destination: %w", err)
		}

		if len(headers) > 0 && string(headers) != "{}" {
			json.Unmarshal(headers, &dest.Headers)
		}
		if len(ssrfPolicy) > 0 && string(ssrfPolicy) != "{}" {
			var policy config.SSRFPolicy
			if json.Unmarshal(ssrfPolicy, &policy) == nil {
				if policy.AllowPrivate || len(policy.AllowedHosts) > 0 {
					dest.SSRFPolicy = &policy
				}
			}
		}

		cfg.Destinations = append(cfg.Destinations, dest)
	}

	// Load subscriptions
	rows, err = s.pool.Query(ctx, `
		SELECT
			s.name, s.table_schema, s.table_name, s.operation,
			d.name, s.trigger_columns, s.payload_columns, s.enabled
		FROM beacon.subscriptions s
		JOIN beacon.destinations d ON d.id = s.destination_id
		WHERE s.deleted_at IS NULL
		ORDER BY s.name
	`)
	if err != nil {
		return nil, fmt.Errorf("query subscriptions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var sub config.SubscriptionConfig
		var schema, table string
		var triggerCols, payloadCols []string
		var enabled bool

		err := rows.Scan(
			&sub.Name, &schema, &table, &sub.Operation,
			&sub.Destination, &triggerCols, &payloadCols, &enabled,
		)
		if err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}

		sub.Table = schema + "." + table
		sub.TriggerOn = triggerCols
		sub.Select = payloadCols
		if !enabled {
			sub.Enabled = &enabled
		}

		cfg.Subscriptions = append(cfg.Subscriptions, sub)
	}

	return cfg, nil
}

// Internal types

type storedDestination struct {
	ID          uuid.UUID
	Name        string
	URL         string
	Method      string
	TimeoutMs   int
	MaxInFlight int
	Headers     map[string]string
	SSRFPolicy  json.RawMessage
}

type storedSubscription struct {
	ID             uuid.UUID
	Name           string
	TableSchema    string
	TableName      string
	Operation      string
	DestinationID  uuid.UUID
	TriggerColumns []string
	PayloadColumns []string
	Enabled        bool
	Draining       bool
}
