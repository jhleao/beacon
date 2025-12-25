// Package testutil provides test utilities including PostgreSQL container management.
package testutil

import (
	"context"
	"fmt"
	"time"

	"beacon/internal/db"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// PostgresContainer wraps a testcontainers PostgreSQL instance.
type PostgresContainer struct {
	Container testcontainers.Container
	DSN       string
	Pool      *db.Pool
}

// StartPostgres starts a PostgreSQL container and returns a connection pool.
func StartPostgres(ctx context.Context) (*PostgresContainer, error) {
	req := testcontainers.ContainerRequest{
		Image:        "postgres:16-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "beacon",
			"POSTGRES_PASSWORD": "beacon",
			"POSTGRES_DB":       "beacon_test",
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).
			WithStartupTimeout(60 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to start postgres container: %w", err)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("failed to get container host: %w", err)
	}

	port, err := container.MappedPort(ctx, "5432")
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("failed to get container port: %w", err)
	}

	dsn := fmt.Sprintf("postgres://beacon:beacon@%s:%s/beacon_test?sslmode=disable", host, port.Port())

	pool, err := db.New(ctx, dsn)
	if err != nil {
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("failed to connect to postgres: %w", err)
	}

	// Run migrations (uses embedded migrations)
	if err := pool.Migrate(ctx); err != nil {
		pool.Close()
		_ = container.Terminate(ctx)
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return &PostgresContainer{
		Container: container,
		DSN:       dsn,
		Pool:      pool,
	}, nil
}

// Close terminates the container and closes the pool.
func (pc *PostgresContainer) Close(ctx context.Context) {
	if pc.Pool != nil {
		pc.Pool.Close()
	}
	if pc.Container != nil {
		_ = pc.Container.Terminate(ctx)
	}
}

// Reset truncates all tables to reset the database state.
func (pc *PostgresContainer) Reset(ctx context.Context) error {
	_, err := pc.Pool.Exec(ctx, `
		TRUNCATE TABLE dead_letters, delivery_attempts, worker_heartbeats,
		               outbox_events, subscriptions, destinations CASCADE
	`)
	return err
}
