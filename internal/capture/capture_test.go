package capture_test

import (
	"context"
	"testing"

	"beacon/internal/capture"
	"beacon/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func setupTestTable(t *testing.T) {
	ctx := context.Background()
	_, err := testContainer.Pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.test_users (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT
		)
	`)
	require.NoError(t, err)
}

func cleanupTriggers(t *testing.T) {
	ctx := context.Background()
	// Remove any test triggers
	_, _ = testContainer.Pool.Exec(ctx, `DROP TRIGGER IF EXISTS beacon_capture_public_test_users ON public.test_users`)
}

func TestInstaller_InstallTrigger(t *testing.T) {
	setupTestTable(t)
	cleanupTriggers(t)
	ctx := context.Background()

	installer := capture.New(testContainer.Pool)

	// Install trigger
	err := installer.InstallTrigger(ctx, "public", "test_users")
	require.NoError(t, err)

	// Verify trigger exists
	triggers, err := installer.ListTriggers(ctx)
	require.NoError(t, err)

	found := false
	for _, trig := range triggers {
		if trig.Schema == "public" && trig.Name == "test_users" {
			found = true
			break
		}
	}
	assert.True(t, found, "trigger should exist")
}

func TestInstaller_InstallTrigger_Idempotent(t *testing.T) {
	setupTestTable(t)
	cleanupTriggers(t)
	ctx := context.Background()

	installer := capture.New(testContainer.Pool)

	// Install trigger twice
	err := installer.InstallTrigger(ctx, "public", "test_users")
	require.NoError(t, err)

	err = installer.InstallTrigger(ctx, "public", "test_users")
	require.NoError(t, err, "should be idempotent")
}

func TestInstaller_UninstallTrigger(t *testing.T) {
	setupTestTable(t)
	cleanupTriggers(t)
	ctx := context.Background()

	installer := capture.New(testContainer.Pool)

	// Install then uninstall
	err := installer.InstallTrigger(ctx, "public", "test_users")
	require.NoError(t, err)

	err = installer.UninstallTrigger(ctx, "public", "test_users")
	require.NoError(t, err)

	// Verify trigger removed
	triggers, err := installer.ListTriggers(ctx)
	require.NoError(t, err)

	for _, trig := range triggers {
		assert.False(t, trig.Schema == "public" && trig.Name == "test_users",
			"trigger should be removed")
	}
}

func TestInstaller_UninstallTrigger_Idempotent(t *testing.T) {
	setupTestTable(t)
	cleanupTriggers(t)
	ctx := context.Background()

	installer := capture.New(testContainer.Pool)

	// Uninstall non-existent trigger - should not error
	err := installer.UninstallTrigger(ctx, "public", "test_users")
	require.NoError(t, err, "should be idempotent")
}

func TestInstaller_ListTriggersMap(t *testing.T) {
	setupTestTable(t)
	cleanupTriggers(t)
	ctx := context.Background()

	installer := capture.New(testContainer.Pool)

	// Install trigger
	err := installer.InstallTrigger(ctx, "public", "test_users")
	require.NoError(t, err)

	// Get map
	triggerMap, err := installer.ListTriggersMap(ctx)
	require.NoError(t, err)

	ref := capture.TableRef{Schema: "public", Name: "test_users"}
	assert.True(t, triggerMap[ref], "trigger should be in map")
}

// Test DDL utility functions
func TestQuoteIdent(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", `"simple"`},
		{"with space", `"with space"`},
		{"with\"quote", `"with""quote"`},
		{"MixedCase", `"MixedCase"`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := capture.QuoteIdent(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestQuoteLiteral(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "'simple'"},
		{"with 'quote'", "'with ''quote'''"},
		{"", "''"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := capture.QuoteLiteral(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestTriggerName(t *testing.T) {
	result := capture.TriggerName("public", "users")
	assert.Equal(t, "beacon_capture_public_users", result)
}

func TestTableRef_String(t *testing.T) {
	ref := capture.TableRef{Schema: "public", Name: "users"}
	assert.Equal(t, "public.users", ref.String())
}
