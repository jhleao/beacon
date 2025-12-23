package db_test

import (
	"context"
	"errors"
	"testing"

	"beacon/internal/db"
	"beacon/internal/testutil"

	"github.com/jackc/pgx/v5"
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

func TestPool_Ping(t *testing.T) {
	ctx := context.Background()

	err := testContainer.Pool.Ping(ctx)
	require.NoError(t, err)
}

func TestPool_Query(t *testing.T) {
	ctx := context.Background()

	var result int
	err := testContainer.Pool.QueryRow(ctx, "SELECT 1 + 1").Scan(&result)
	require.NoError(t, err)
	assert.Equal(t, 2, result)
}

func TestPool_WithTx_Commit(t *testing.T) {
	ctx := context.Background()

	// Create test table
	_, err := testContainer.Pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS test_tx (id INT PRIMARY KEY, value TEXT)
	`)
	require.NoError(t, err)

	// Insert in transaction
	err = testContainer.Pool.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "INSERT INTO test_tx (id, value) VALUES (1, 'committed')")
		return err
	})
	require.NoError(t, err)

	// Verify committed
	var value string
	err = testContainer.Pool.QueryRow(ctx, "SELECT value FROM test_tx WHERE id = 1").Scan(&value)
	require.NoError(t, err)
	assert.Equal(t, "committed", value)

	// Cleanup
	_, _ = testContainer.Pool.Exec(ctx, "DROP TABLE test_tx")
}

func TestPool_WithTx_Rollback(t *testing.T) {
	ctx := context.Background()

	// Create test table
	_, err := testContainer.Pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS test_tx_rollback (id INT PRIMARY KEY, value TEXT)
	`)
	require.NoError(t, err)

	// Insert a base row
	_, err = testContainer.Pool.Exec(ctx, "INSERT INTO test_tx_rollback (id, value) VALUES (1, 'original')")
	require.NoError(t, err)

	// Transaction that returns error should rollback
	err = testContainer.Pool.WithTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE test_tx_rollback SET value = 'updated' WHERE id = 1")
		if err != nil {
			return err
		}
		return errors.New("intentional error")
	})
	require.Error(t, err)

	// Verify rollback - value should still be original
	var value string
	err = testContainer.Pool.QueryRow(ctx, "SELECT value FROM test_tx_rollback WHERE id = 1").Scan(&value)
	require.NoError(t, err)
	assert.Equal(t, "original", value)

	// Cleanup
	_, _ = testContainer.Pool.Exec(ctx, "DROP TABLE test_tx_rollback")
}

func TestPool_Migrate_CreatesSchema(t *testing.T) {
	ctx := context.Background()

	// Verify beacon schema exists (created by migration)
	var schemaExists bool
	err := testContainer.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.schemata
			WHERE schema_name = 'beacon'
		)
	`).Scan(&schemaExists)
	require.NoError(t, err)
	assert.True(t, schemaExists, "beacon schema should exist")
}

func TestPool_Migrate_CreatesTables(t *testing.T) {
	ctx := context.Background()

	expectedTables := []string{
		"destinations",
		"subscriptions",
		"outbox_events",
		"worker_heartbeats",
		"delivery_attempts",
		"dead_letters",
		"schema_version",
	}

	for _, tableName := range expectedTables {
		t.Run(tableName, func(t *testing.T) {
			var exists bool
			err := testContainer.Pool.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM information_schema.tables
					WHERE table_schema = 'beacon'
					  AND table_name = $1
				)
			`, tableName).Scan(&exists)
			require.NoError(t, err)
			assert.True(t, exists, "table %s should exist", tableName)
		})
	}
}

func TestNew_InvalidDSN(t *testing.T) {
	ctx := context.Background()

	_, err := db.New(ctx, "invalid-dsn")
	require.Error(t, err)
}
