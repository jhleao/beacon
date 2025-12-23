package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"beacon/internal/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Repository handles outbox database operations.
type Repository struct {
	pool *db.Pool
}

// New creates a Repository.
func New(pool *db.Pool) *Repository {
	return &Repository{pool: pool}
}

// Claim atomically claims up to `limit` pending events for `workerID`.
// Returns events with their destination info.
func (r *Repository) Claim(ctx context.Context, workerID string, limit int) ([]ClaimedEvent, error) {
	rows, err := r.pool.Query(ctx, `
		WITH claimed AS (
			SELECT e.id
			FROM beacon.outbox_events e
			JOIN beacon.subscriptions s ON s.id = e.subscription_id
			WHERE e.state = 'pending'
			  AND e.visible_at <= now()
			  AND s.deleted_at IS NULL
			ORDER BY e.visible_at, e.occurred_at
			LIMIT $1
			FOR UPDATE OF e SKIP LOCKED
		),
		updated AS (
			UPDATE beacon.outbox_events e
			SET
				state = 'delivering',
				locked_by = $2,
				locked_at = now(),
				attempts = attempts + 1
			FROM claimed c
			WHERE e.id = c.id
			RETURNING e.*
		)
		SELECT
			u.id,
			u.subscription_id,
			u.occurred_at,
			u.table_schema,
			u.table_name,
			u.operation,
			u.pk,
			u.old_data,
			u.new_data,
			u.payload,
			u.state,
			u.visible_at,
			u.locked_by,
			u.locked_at,
			u.attempts,
			u.last_error,
			u.created_at,
			d.id,
			d.name,
			d.url,
			d.method,
			d.headers,
			d.timeout_ms,
			d.max_in_flight,
			d.ssrf_policy
		FROM updated u
		JOIN beacon.subscriptions s ON s.id = u.subscription_id
		JOIN beacon.destinations d ON d.id = s.destination_id
		ORDER BY u.visible_at, u.occurred_at
	`, limit, workerID)
	if err != nil {
		return nil, fmt.Errorf("claim events: %w", err)
	}
	defer rows.Close()

	var result []ClaimedEvent
	for rows.Next() {
		var ce ClaimedEvent
		var headers json.RawMessage
		err := rows.Scan(
			&ce.Event.ID,
			&ce.Event.SubscriptionID,
			&ce.Event.OccurredAt,
			&ce.Event.TableSchema,
			&ce.Event.TableName,
			&ce.Event.Operation,
			&ce.Event.PK,
			&ce.Event.OldData,
			&ce.Event.NewData,
			&ce.Event.Payload,
			&ce.Event.State,
			&ce.Event.VisibleAt,
			&ce.Event.LockedBy,
			&ce.Event.LockedAt,
			&ce.Event.Attempts,
			&ce.Event.LastError,
			&ce.Event.CreatedAt,
			&ce.Destination.ID,
			&ce.Destination.Name,
			&ce.Destination.URL,
			&ce.Destination.Method,
			&headers,
			&ce.Destination.TimeoutMs,
			&ce.Destination.MaxInFlight,
			&ce.Destination.SSRFPolicy,
		)
		if err != nil {
			return nil, fmt.Errorf("scan claimed event: %w", err)
		}

		// Parse headers JSON
		if len(headers) > 0 {
			if err := json.Unmarshal(headers, &ce.Destination.Headers); err != nil {
				ce.Destination.Headers = make(map[string]string)
			}
		} else {
			ce.Destination.Headers = make(map[string]string)
		}

		result = append(result, ce)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claimed events: %w", err)
	}

	return result, nil
}

// Ack marks an event as successfully delivered.
func (r *Repository) Ack(ctx context.Context, eventID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE beacon.outbox_events
		SET
			state = 'delivered',
			locked_by = NULL,
			locked_at = NULL
		WHERE id = $1
	`, eventID)
	if err != nil {
		return fmt.Errorf("ack event: %w", err)
	}
	return nil
}

// Reschedule marks an event for retry at `visibleAt` with error message.
func (r *Repository) Reschedule(ctx context.Context, eventID uuid.UUID, visibleAt time.Time, errMsg string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE beacon.outbox_events
		SET
			state = 'pending',
			locked_by = NULL,
			locked_at = NULL,
			visible_at = $2,
			last_error = $3
		WHERE id = $1
	`, eventID, visibleAt, errMsg)
	if err != nil {
		return fmt.Errorf("reschedule event: %w", err)
	}
	return nil
}

// ToDead moves an event to the dead-letter queue.
func (r *Repository) ToDead(ctx context.Context, eventID uuid.UUID, reason string) error {
	return r.pool.WithTx(ctx, func(tx pgx.Tx) error {
		// Insert snapshot into dead_letters
		_, err := tx.Exec(ctx, `
			INSERT INTO beacon.dead_letters (event_id, reason, snapshot)
			SELECT
				e.id,
				$2,
				jsonb_build_object(
					'subscription_id', e.subscription_id,
					'subscription_name', s.name,
					'destination_id', d.id,
					'destination_name', d.name,
					'destination_url', d.url,
					'occurred_at', e.occurred_at,
					'table_schema', e.table_schema,
					'table_name', e.table_name,
					'operation', e.operation,
					'pk', e.pk,
					'old_data', e.old_data,
					'new_data', e.new_data,
					'payload', e.payload,
					'attempts', e.attempts,
					'last_error', e.last_error
				)
			FROM beacon.outbox_events e
			JOIN beacon.subscriptions s ON s.id = e.subscription_id
			JOIN beacon.destinations d ON d.id = s.destination_id
			WHERE e.id = $1
		`, eventID, reason)
		if err != nil {
			return fmt.Errorf("insert dead letter: %w", err)
		}

		// Update state
		_, err = tx.Exec(ctx, `
			UPDATE beacon.outbox_events
			SET
				state = 'dead',
				locked_by = NULL,
				locked_at = NULL
			WHERE id = $1
		`, eventID)
		if err != nil {
			return fmt.Errorf("update event state: %w", err)
		}

		return nil
	})
}

// RecordAttempt logs a delivery attempt for audit.
func (r *Repository) RecordAttempt(ctx context.Context, attempt DeliveryAttempt) error {
	headers, _ := json.Marshal(attempt.ResponseHeaders)

	_, err := r.pool.Exec(ctx, `
		INSERT INTO beacon.delivery_attempts (
			event_id, destination_id, attempt,
			started_at, finished_at, status_code, error, response_headers
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, attempt.EventID, attempt.DestinationID, attempt.Attempt,
		attempt.StartedAt, attempt.FinishedAt, attempt.StatusCode, attempt.Error, headers)
	if err != nil {
		return fmt.Errorf("record attempt: %w", err)
	}
	return nil
}

// CountByState returns event counts grouped by state.
func (r *Repository) CountByState(ctx context.Context) (map[State]int64, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT state, COUNT(*) FROM beacon.outbox_events GROUP BY state
	`)
	if err != nil {
		return nil, fmt.Errorf("count by state: %w", err)
	}
	defer rows.Close()

	result := make(map[State]int64)
	for rows.Next() {
		var state State
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return nil, fmt.Errorf("scan state count: %w", err)
		}
		result[state] = count
	}

	return result, rows.Err()
}

// CountPendingForSubscription returns pending event count for a subscription.
func (r *Repository) CountPendingForSubscription(ctx context.Context, subID uuid.UUID) (int64, error) {
	var count int64
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM beacon.outbox_events
		WHERE subscription_id = $1 AND state IN ('pending', 'delivering')
	`, subID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count pending: %w", err)
	}
	return count, nil
}
