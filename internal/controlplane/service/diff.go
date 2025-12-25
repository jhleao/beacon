package service

import (
	"context"
	"encoding/json"
	"fmt"

	"beacon/internal/capture"
	"beacon/internal/config"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// loadDestinations loads current destinations from database.
func (s *ApplyService) loadDestinations(ctx context.Context, tx pgx.Tx) (map[string]storedDestination, error) {
	query := `
		SELECT id, name, url, method, timeout_ms, max_in_flight, headers, ssrf_policy
		FROM beacon.destinations
	`

	var rows pgx.Rows
	var err error
	if tx != nil {
		rows, err = tx.Query(ctx, query)
	} else {
		rows, err = s.pool.Query(ctx, query)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]storedDestination)
	for rows.Next() {
		var d storedDestination
		var headers, ssrf []byte
		err := rows.Scan(&d.ID, &d.Name, &d.URL, &d.Method, &d.TimeoutMs, &d.MaxInFlight, &headers, &ssrf)
		if err != nil {
			return nil, err
		}
		if len(headers) > 0 {
			_ = json.Unmarshal(headers, &d.Headers)
		}
		d.SSRFPolicy = ssrf
		result[d.Name] = d
	}

	return result, rows.Err()
}

// loadSubscriptions loads current subscriptions from database.
func (s *ApplyService) loadSubscriptions(ctx context.Context, tx pgx.Tx) (map[string]storedSubscription, error) {
	query := `
		SELECT id, name, table_schema, table_name, operation, destination_id,
			   trigger_columns, payload_columns, enabled, draining
		FROM beacon.subscriptions
		WHERE deleted_at IS NULL
	`

	var rows pgx.Rows
	var err error
	if tx != nil {
		rows, err = tx.Query(ctx, query)
	} else {
		rows, err = s.pool.Query(ctx, query)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]storedSubscription)
	for rows.Next() {
		var sub storedSubscription
		err := rows.Scan(
			&sub.ID, &sub.Name, &sub.TableSchema, &sub.TableName, &sub.Operation,
			&sub.DestinationID, &sub.TriggerColumns, &sub.PayloadColumns, &sub.Enabled, &sub.Draining,
		)
		if err != nil {
			return nil, err
		}
		result[sub.Name] = sub
	}

	return result, rows.Err()
}

// diffDestinations computes changes between current and desired destinations.
func (s *ApplyService) diffDestinations(current map[string]storedDestination, desired []config.DestinationConfig) ChangeSet {
	var cs ChangeSet

	desiredNames := make(map[string]bool)
	for _, d := range desired {
		desiredNames[d.Name] = true

		if _, exists := current[d.Name]; !exists {
			cs.Created = append(cs.Created, d.Name)
		} else {
			// Check if changed
			cur := current[d.Name]
			if cur.URL != d.URL || cur.Method != d.Method ||
				cur.TimeoutMs != d.TimeoutMs || cur.MaxInFlight != d.MaxInFlight {
				cs.Updated = append(cs.Updated, d.Name)
			}
		}
	}

	for name := range current {
		if !desiredNames[name] {
			cs.Deleted = append(cs.Deleted, name)
		}
	}

	return cs
}

// diffSubscriptions computes changes between current and desired subscriptions.
func (s *ApplyService) diffSubscriptions(current map[string]storedSubscription, desired []config.SubscriptionConfig, destIDs map[string]uuid.UUID) ChangeSet {
	var cs ChangeSet

	desiredNames := make(map[string]bool)
	for _, sub := range desired {
		desiredNames[sub.Name] = true

		if _, exists := current[sub.Name]; !exists {
			cs.Created = append(cs.Created, sub.Name)
		} else {
			// Check if changed
			cur := current[sub.Name]
			schema, table := config.ParseTable(sub.Table)
			destID := destIDs[sub.Destination]

			if cur.TableSchema != schema || cur.TableName != table ||
				cur.Operation != sub.Operation || cur.DestinationID != destID ||
				cur.Enabled != sub.IsEnabled() {
				cs.Updated = append(cs.Updated, sub.Name)
			}
		}
	}

	for name := range current {
		if !desiredNames[name] {
			cs.Deleted = append(cs.Deleted, name)
		}
	}

	return cs
}

// applyDestinations creates, updates, and deletes destinations.
func (s *ApplyService) applyDestinations(ctx context.Context, tx pgx.Tx, current map[string]storedDestination, desired []config.DestinationConfig) (map[string]uuid.UUID, ChangeSet, error) {
	cs := s.diffDestinations(current, desired)
	idMap := make(map[string]uuid.UUID)

	// Copy existing IDs
	for name, dest := range current {
		idMap[name] = dest.ID
	}

	// Create new destinations
	for _, d := range desired {
		if _, exists := current[d.Name]; exists {
			continue
		}

		headers, _ := json.Marshal(d.Headers)
		ssrf, _ := json.Marshal(d.SSRFPolicy)

		var id uuid.UUID
		err := tx.QueryRow(ctx, `
			INSERT INTO beacon.destinations (name, url, method, timeout_ms, max_in_flight, headers, ssrf_policy)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING id
		`, d.Name, d.URL, d.Method, d.TimeoutMs, d.MaxInFlight, headers, ssrf).Scan(&id)
		if err != nil {
			return nil, cs, fmt.Errorf("create destination %s: %w", d.Name, err)
		}
		idMap[d.Name] = id
	}

	// Update existing destinations
	for _, d := range desired {
		cur, exists := current[d.Name]
		if !exists {
			continue
		}

		headers, _ := json.Marshal(d.Headers)
		ssrf, _ := json.Marshal(d.SSRFPolicy)

		_, err := tx.Exec(ctx, `
			UPDATE beacon.destinations
			SET url = $2, method = $3, timeout_ms = $4, max_in_flight = $5, headers = $6, ssrf_policy = $7
			WHERE id = $1
		`, cur.ID, d.URL, d.Method, d.TimeoutMs, d.MaxInFlight, headers, ssrf)
		if err != nil {
			return nil, cs, fmt.Errorf("update destination %s: %w", d.Name, err)
		}
	}

	// Delete removed destinations
	desiredNames := make(map[string]bool)
	for _, d := range desired {
		desiredNames[d.Name] = true
	}

	for name, dest := range current {
		if desiredNames[name] {
			continue
		}

		_, err := tx.Exec(ctx, `DELETE FROM beacon.destinations WHERE id = $1`, dest.ID)
		if err != nil {
			return nil, cs, fmt.Errorf("delete destination %s: %w", name, err)
		}
		delete(idMap, name)
	}

	return idMap, cs, nil
}

// applySubscriptions creates, updates, and soft-deletes subscriptions.
func (s *ApplyService) applySubscriptions(ctx context.Context, tx pgx.Tx, current map[string]storedSubscription, desired []config.SubscriptionConfig, destIDs map[string]uuid.UUID) (ChangeSet, error) {
	cs := s.diffSubscriptions(current, desired, destIDs)

	// Create new subscriptions
	for _, sub := range desired {
		if _, exists := current[sub.Name]; exists {
			continue
		}

		schema, table := config.ParseTable(sub.Table)
		destID := destIDs[sub.Destination]

		_, err := tx.Exec(ctx, `
			INSERT INTO beacon.subscriptions
				(name, table_schema, table_name, operation, destination_id, trigger_columns, payload_columns, enabled)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, sub.Name, schema, table, sub.Operation, destID, sub.TriggerOn, sub.Select, sub.IsEnabled())
		if err != nil {
			return cs, fmt.Errorf("create subscription %s: %w", sub.Name, err)
		}
	}

	// Update existing subscriptions
	for _, sub := range desired {
		cur, exists := current[sub.Name]
		if !exists {
			continue
		}

		schema, table := config.ParseTable(sub.Table)
		destID := destIDs[sub.Destination]

		_, err := tx.Exec(ctx, `
			UPDATE beacon.subscriptions
			SET table_schema = $2, table_name = $3, operation = $4, destination_id = $5,
				trigger_columns = $6, payload_columns = $7, enabled = $8, draining = false
			WHERE id = $1
		`, cur.ID, schema, table, sub.Operation, destID, sub.TriggerOn, sub.Select, sub.IsEnabled())
		if err != nil {
			return cs, fmt.Errorf("update subscription %s: %w", sub.Name, err)
		}
	}

	// Soft-delete removed subscriptions (set draining)
	desiredNames := make(map[string]bool)
	for _, sub := range desired {
		desiredNames[sub.Name] = true
	}

	for name, sub := range current {
		if desiredNames[name] {
			continue
		}

		_, err := tx.Exec(ctx, `
			UPDATE beacon.subscriptions
			SET draining = true
			WHERE id = $1
		`, sub.ID)
		if err != nil {
			return cs, fmt.Errorf("drain subscription %s: %w", name, err)
		}
	}

	return cs, nil
}

// reconcileTriggers installs/removes triggers based on active subscriptions.
func (s *ApplyService) reconcileTriggers(ctx context.Context) (ChangeSet, error) {
	var cs ChangeSet

	// Tables that need triggers (have active subscriptions)
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT table_schema, table_name
		FROM beacon.subscriptions
		WHERE deleted_at IS NULL AND enabled = true
	`)
	if err != nil {
		return cs, fmt.Errorf("query tables: %w", err)
	}
	defer rows.Close()

	tablesNeeded := make(map[capture.TableRef]bool)
	for rows.Next() {
		var ref capture.TableRef
		if err := rows.Scan(&ref.Schema, &ref.Name); err != nil {
			return cs, err
		}
		tablesNeeded[ref] = true
	}

	// Tables that have triggers
	tablesWithTriggers, err := s.installer.ListTriggersMap(ctx)
	if err != nil {
		return cs, fmt.Errorf("list triggers: %w", err)
	}

	// Install missing triggers
	for table := range tablesNeeded {
		if tablesWithTriggers[table] {
			continue
		}
		if err := s.installer.InstallTrigger(ctx, table.Schema, table.Name); err != nil {
			return cs, fmt.Errorf("install trigger on %s: %w", table.String(), err)
		}
		cs.Created = append(cs.Created, table.String())
	}

	// Remove orphan triggers
	for table := range tablesWithTriggers {
		if tablesNeeded[table] {
			continue
		}
		if err := s.installer.UninstallTrigger(ctx, table.Schema, table.Name); err != nil {
			return cs, fmt.Errorf("uninstall trigger from %s: %w", table.String(), err)
		}
		cs.Deleted = append(cs.Deleted, table.String())
	}

	return cs, nil
}
