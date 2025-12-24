package outbox_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"beacon/internal/outbox"
	"beacon/internal/testutil"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testLogger returns a discard logger for tests.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

var testContainer *testutil.PostgresContainer

func TestMain(m *testing.M) {
	ctx := context.Background()

	var err error
	testContainer, err = testutil.StartPostgres(ctx)
	if err != nil {
		panic("failed to start postgres: " + err.Error())
	}

	code := m.Run()

	testContainer.Close(ctx)
	if code != 0 {
		panic("tests failed")
	}
}

func setupTest(t *testing.T) (*outbox.Repository, func()) {
	ctx := context.Background()
	require.NoError(t, testContainer.Reset(ctx))

	// Insert test destination and subscription
	_, err := testContainer.Pool.Exec(ctx, `
		INSERT INTO beacon.destinations (id, name, url, method, timeout_ms, max_in_flight)
		VALUES ('11111111-1111-1111-1111-111111111111', 'test-dest', 'https://example.com/webhook', 'POST', 5000, 10)
	`)
	require.NoError(t, err)

	_, err = testContainer.Pool.Exec(ctx, `
		INSERT INTO beacon.subscriptions (id, destination_id, name, table_schema, table_name, operation)
		VALUES ('22222222-2222-2222-2222-222222222222', '11111111-1111-1111-1111-111111111111', 'test-sub', 'public', 'users', 'INSERT')
	`)
	require.NoError(t, err)

	repo := outbox.New(testContainer.Pool, testLogger())
	return repo, func() {}
}

func insertEvent(t *testing.T, state outbox.State, visibleAt time.Time) uuid.UUID {
	ctx := context.Background()
	eventID := uuid.New()

	_, err := testContainer.Pool.Exec(ctx, `
		INSERT INTO beacon.outbox_events (
			id, subscription_id, occurred_at, table_schema, table_name,
			operation, pk, new_data, payload, state, visible_at
		) VALUES (
			$1, '22222222-2222-2222-2222-222222222222', now(), 'public', 'users',
			'INSERT', '{"id": 1}', '{"name": "test"}', '{"event": "user.created"}',
			$2, $3
		)
	`, eventID, state, visibleAt)
	require.NoError(t, err)
	return eventID
}

func TestRepository_Claim(t *testing.T) {
	repo, cleanup := setupTest(t)
	defer cleanup()
	ctx := context.Background()

	// Insert pending events
	ev1 := insertEvent(t, outbox.StatePending, time.Now().Add(-time.Minute))
	ev2 := insertEvent(t, outbox.StatePending, time.Now().Add(-time.Second))
	_ = insertEvent(t, outbox.StatePending, time.Now().Add(time.Hour)) // Future event, not visible

	// Claim events
	claimed, err := repo.Claim(ctx, "worker-1", 10)
	require.NoError(t, err)

	// Should claim only 2 visible events
	assert.Len(t, claimed, 2)

	// Events should be in order (by visible_at)
	assert.Equal(t, ev1, claimed[0].Event.ID)
	assert.Equal(t, ev2, claimed[1].Event.ID)

	// State should be delivering
	assert.Equal(t, outbox.StateDelivering, claimed[0].Event.State)
	assert.Equal(t, "worker-1", *claimed[0].Event.LockedBy)
	assert.Equal(t, 1, claimed[0].Event.Attempts)

	// Destination should be populated
	assert.Equal(t, "test-dest", claimed[0].Destination.Name)
	assert.Equal(t, "https://example.com/webhook", claimed[0].Destination.URL)
}

func TestRepository_Claim_SkipLocked(t *testing.T) {
	repo, cleanup := setupTest(t)
	defer cleanup()
	ctx := context.Background()

	// Insert pending events
	insertEvent(t, outbox.StatePending, time.Now().Add(-time.Minute))
	insertEvent(t, outbox.StatePending, time.Now().Add(-time.Second))

	// First worker claims
	claimed1, err := repo.Claim(ctx, "worker-1", 10)
	require.NoError(t, err)
	assert.Len(t, claimed1, 2)

	// Second worker should get nothing (all locked)
	claimed2, err := repo.Claim(ctx, "worker-2", 10)
	require.NoError(t, err)
	assert.Len(t, claimed2, 0)
}

func TestRepository_Ack(t *testing.T) {
	repo, cleanup := setupTest(t)
	defer cleanup()
	ctx := context.Background()

	eventID := insertEvent(t, outbox.StatePending, time.Now().Add(-time.Minute))

	// Claim
	claimed, err := repo.Claim(ctx, "worker-1", 1)
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	// Ack
	err = repo.Ack(ctx, eventID)
	require.NoError(t, err)

	// Verify state
	var state outbox.State
	err = testContainer.Pool.QueryRow(ctx, `
		SELECT state FROM beacon.outbox_events WHERE id = $1
	`, eventID).Scan(&state)
	require.NoError(t, err)
	assert.Equal(t, outbox.StateDelivered, state)
}

func TestRepository_Reschedule(t *testing.T) {
	repo, cleanup := setupTest(t)
	defer cleanup()
	ctx := context.Background()

	eventID := insertEvent(t, outbox.StatePending, time.Now().Add(-time.Minute))

	// Claim
	_, err := repo.Claim(ctx, "worker-1", 1)
	require.NoError(t, err)

	// Reschedule
	nextVisibleAt := time.Now().Add(30 * time.Second).Truncate(time.Microsecond)
	err = repo.Reschedule(ctx, eventID, nextVisibleAt, "connection timeout")
	require.NoError(t, err)

	// Verify state
	var state outbox.State
	var visibleAt time.Time
	var lastError *string
	err = testContainer.Pool.QueryRow(ctx, `
		SELECT state, visible_at, last_error FROM beacon.outbox_events WHERE id = $1
	`, eventID).Scan(&state, &visibleAt, &lastError)
	require.NoError(t, err)
	assert.Equal(t, outbox.StatePending, state)
	assert.WithinDuration(t, nextVisibleAt, visibleAt, time.Second)
	assert.NotNil(t, lastError)
	assert.Equal(t, "connection timeout", *lastError)
}

func TestRepository_ToDead(t *testing.T) {
	repo, cleanup := setupTest(t)
	defer cleanup()
	ctx := context.Background()

	eventID := insertEvent(t, outbox.StatePending, time.Now().Add(-time.Minute))

	// Claim
	_, err := repo.Claim(ctx, "worker-1", 1)
	require.NoError(t, err)

	// Move to dead letter
	err = repo.ToDead(ctx, eventID, "max attempts exceeded")
	require.NoError(t, err)

	// Verify event state
	var state outbox.State
	err = testContainer.Pool.QueryRow(ctx, `
		SELECT state FROM beacon.outbox_events WHERE id = $1
	`, eventID).Scan(&state)
	require.NoError(t, err)
	assert.Equal(t, outbox.StateDead, state)

	// Verify dead letter entry
	var reason string
	err = testContainer.Pool.QueryRow(ctx, `
		SELECT reason FROM beacon.dead_letters WHERE event_id = $1
	`, eventID).Scan(&reason)
	require.NoError(t, err)
	assert.Equal(t, "max attempts exceeded", reason)
}

func TestRepository_RecordAttempt(t *testing.T) {
	repo, cleanup := setupTest(t)
	defer cleanup()
	ctx := context.Background()

	eventID := insertEvent(t, outbox.StatePending, time.Now().Add(-time.Minute))
	destID := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	attempt := outbox.DeliveryAttempt{
		EventID:       eventID,
		DestinationID: destID,
		Attempt:       1,
		StartedAt:     time.Now().Add(-time.Second),
		FinishedAt:    time.Now(),
		StatusCode:    intPtr(200),
		ResponseHeaders: map[string]string{
			"X-Request-Id": "abc123",
		},
	}

	err := repo.RecordAttempt(ctx, attempt)
	require.NoError(t, err)

	// Verify record
	var statusCode int
	err = testContainer.Pool.QueryRow(ctx, `
		SELECT status_code FROM beacon.delivery_attempts WHERE event_id = $1
	`, eventID).Scan(&statusCode)
	require.NoError(t, err)
	assert.Equal(t, 200, statusCode)
}

func TestRepository_CountByState(t *testing.T) {
	repo, cleanup := setupTest(t)
	defer cleanup()
	ctx := context.Background()

	// Insert events in different states
	insertEvent(t, outbox.StatePending, time.Now())
	insertEvent(t, outbox.StatePending, time.Now())
	insertEvent(t, outbox.StateDelivered, time.Now())

	counts, err := repo.CountByState(ctx)
	require.NoError(t, err)

	assert.Equal(t, int64(2), counts[outbox.StatePending])
	assert.Equal(t, int64(1), counts[outbox.StateDelivered])
}

func TestRepository_CountPendingForSubscription(t *testing.T) {
	repo, cleanup := setupTest(t)
	defer cleanup()
	ctx := context.Background()

	subID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	insertEvent(t, outbox.StatePending, time.Now())
	insertEvent(t, outbox.StatePending, time.Now())
	insertEvent(t, outbox.StateDelivered, time.Now())

	count, err := repo.CountPendingForSubscription(ctx, subID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

func intPtr(v int) *int {
	return &v
}
