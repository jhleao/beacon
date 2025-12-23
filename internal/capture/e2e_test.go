package capture_test

import (
	"context"
	"encoding/json"
	"testing"

	"beacon/internal/capture"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTriggerCapture_E2E tests the full flow from trigger installation
// through row changes to outbox event creation.
func TestTriggerCapture_E2E(t *testing.T) {
	ctx := context.Background()

	// Create a test table
	_, err := testContainer.Pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.users_e2e (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT
		)
	`)
	require.NoError(t, err)
	defer func() {
		testContainer.Pool.Exec(ctx, "DROP TABLE public.users_e2e CASCADE")
	}()

	// Create destination and subscription
	_, err = testContainer.Pool.Exec(ctx, `
		INSERT INTO beacon.destinations (id, name, url, method, timeout_ms)
		VALUES ('eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee', 'e2e-dest', 'https://example.com/webhook', 'POST', 5000)
		ON CONFLICT (name) DO NOTHING
	`)
	require.NoError(t, err)

	_, err = testContainer.Pool.Exec(ctx, `
		INSERT INTO beacon.subscriptions (id, destination_id, name, table_schema, table_name, operation)
		VALUES ('ffffffff-ffff-ffff-ffff-ffffffffffff', 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee', 'e2e-sub-insert', 'public', 'users_e2e', 'INSERT')
		ON CONFLICT (name) DO NOTHING
	`)
	require.NoError(t, err)

	// Install trigger
	installer := capture.New(testContainer.Pool)
	err = installer.InstallTrigger(ctx, "public", "users_e2e")
	require.NoError(t, err)
	defer installer.UninstallTrigger(ctx, "public", "users_e2e")

	// Clear any existing events
	_, err = testContainer.Pool.Exec(ctx, `
		DELETE FROM beacon.outbox_events WHERE subscription_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff'
	`)
	require.NoError(t, err)

	// Insert a row - this should trigger an event
	_, err = testContainer.Pool.Exec(ctx, `
		INSERT INTO public.users_e2e (name, email) VALUES ('Test User', 'test@example.com')
	`)
	require.NoError(t, err)

	// Verify event was created
	var eventCount int
	err = testContainer.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM beacon.outbox_events
		WHERE subscription_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff'
	`).Scan(&eventCount)
	require.NoError(t, err)
	assert.Equal(t, 1, eventCount, "should create one outbox event")

	// Verify event content
	var payload []byte
	var operation, tableSchema, tableName string
	err = testContainer.Pool.QueryRow(ctx, `
		SELECT payload, operation, table_schema, table_name
		FROM beacon.outbox_events
		WHERE subscription_id = 'ffffffff-ffff-ffff-ffff-ffffffffffff'
		LIMIT 1
	`).Scan(&payload, &operation, &tableSchema, &tableName)
	require.NoError(t, err)

	assert.Equal(t, "INSERT", operation)
	assert.Equal(t, "public", tableSchema)
	assert.Equal(t, "users_e2e", tableName)

	// Verify payload structure
	var payloadData map[string]any
	err = json.Unmarshal(payload, &payloadData)
	require.NoError(t, err)

	assert.Equal(t, float64(1), payloadData["version"])
	assert.NotNil(t, payloadData["trigger"])
	assert.NotNil(t, payloadData["new"])
	assert.Nil(t, payloadData["old"]) // INSERT has no old data

	// Verify new data contains our values
	newData := payloadData["new"].(map[string]any)
	assert.Equal(t, "Test User", newData["name"])
	assert.Equal(t, "test@example.com", newData["email"])
}

func TestTriggerCapture_Update(t *testing.T) {
	ctx := context.Background()

	// Create a test table
	_, err := testContainer.Pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.users_update_test (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL
		)
	`)
	require.NoError(t, err)
	defer func() {
		testContainer.Pool.Exec(ctx, "DROP TABLE public.users_update_test CASCADE")
	}()

	// Create destination and subscription for UPDATE
	_, err = testContainer.Pool.Exec(ctx, `
		INSERT INTO beacon.destinations (id, name, url, method, timeout_ms)
		VALUES ('dddddddd-dddd-dddd-dddd-dddddddddddd', 'update-dest', 'https://example.com/webhook', 'POST', 5000)
		ON CONFLICT (name) DO NOTHING
	`)
	require.NoError(t, err)

	_, err = testContainer.Pool.Exec(ctx, `
		INSERT INTO beacon.subscriptions (id, destination_id, name, table_schema, table_name, operation)
		VALUES ('cccccccc-cccc-cccc-cccc-cccccccccccc', 'dddddddd-dddd-dddd-dddd-dddddddddddd', 'update-sub', 'public', 'users_update_test', 'UPDATE')
		ON CONFLICT (name) DO NOTHING
	`)
	require.NoError(t, err)

	// Install trigger
	installer := capture.New(testContainer.Pool)
	err = installer.InstallTrigger(ctx, "public", "users_update_test")
	require.NoError(t, err)
	defer installer.UninstallTrigger(ctx, "public", "users_update_test")

	// Insert a row (no subscription for INSERT, so no event)
	_, err = testContainer.Pool.Exec(ctx, `
		INSERT INTO public.users_update_test (name) VALUES ('Original')
	`)
	require.NoError(t, err)

	// Clear any events
	_, err = testContainer.Pool.Exec(ctx, `
		DELETE FROM beacon.outbox_events WHERE subscription_id = 'cccccccc-cccc-cccc-cccc-cccccccccccc'
	`)
	require.NoError(t, err)

	// Update the row - this should trigger an event
	_, err = testContainer.Pool.Exec(ctx, `
		UPDATE public.users_update_test SET name = 'Updated' WHERE id = 1
	`)
	require.NoError(t, err)

	// Verify event was created
	var payload []byte
	err = testContainer.Pool.QueryRow(ctx, `
		SELECT payload FROM beacon.outbox_events
		WHERE subscription_id = 'cccccccc-cccc-cccc-cccc-cccccccccccc'
		LIMIT 1
	`).Scan(&payload)
	require.NoError(t, err)

	var payloadData map[string]any
	err = json.Unmarshal(payload, &payloadData)
	require.NoError(t, err)

	// UPDATE should have both old and new data
	oldData := payloadData["old"].(map[string]any)
	newData := payloadData["new"].(map[string]any)

	assert.Equal(t, "Original", oldData["name"])
	assert.Equal(t, "Updated", newData["name"])
}

func TestTriggerCapture_DisabledSubscription(t *testing.T) {
	ctx := context.Background()

	// Create a test table
	_, err := testContainer.Pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.disabled_test (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL
		)
	`)
	require.NoError(t, err)
	defer func() {
		testContainer.Pool.Exec(ctx, "DROP TABLE public.disabled_test CASCADE")
	}()

	// Create a disabled subscription
	_, err = testContainer.Pool.Exec(ctx, `
		INSERT INTO beacon.destinations (id, name, url, method, timeout_ms)
		VALUES ('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'disabled-dest', 'https://example.com/webhook', 'POST', 5000)
		ON CONFLICT (name) DO NOTHING
	`)
	require.NoError(t, err)

	_, err = testContainer.Pool.Exec(ctx, `
		INSERT INTO beacon.subscriptions (id, destination_id, name, table_schema, table_name, operation, enabled)
		VALUES ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'disabled-sub', 'public', 'disabled_test', 'INSERT', false)
		ON CONFLICT (name) DO NOTHING
	`)
	require.NoError(t, err)

	// Install trigger
	installer := capture.New(testContainer.Pool)
	err = installer.InstallTrigger(ctx, "public", "disabled_test")
	require.NoError(t, err)
	defer installer.UninstallTrigger(ctx, "public", "disabled_test")

	// Insert a row - should NOT create event since subscription is disabled
	_, err = testContainer.Pool.Exec(ctx, `
		INSERT INTO public.disabled_test (name) VALUES ('Test')
	`)
	require.NoError(t, err)

	// Verify no event was created
	var eventCount int
	err = testContainer.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM beacon.outbox_events
		WHERE subscription_id = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa'
	`).Scan(&eventCount)
	require.NoError(t, err)
	assert.Equal(t, 0, eventCount, "should not create event for disabled subscription")
}
