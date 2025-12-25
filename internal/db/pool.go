// Package db provides PostgreSQL connection pooling and database utilities for Beacon.
package db

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Pool wraps pgxpool.Pool with Beacon-specific helpers.
type Pool struct {
	*pgxpool.Pool
}

// New creates a connection pool from a database URL.
func New(ctx context.Context, databaseURL string) (*Pool, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}

	// Configure pool settings
	config.MaxConns = 25
	config.MinConns = 5
	config.MaxConnLifetime = 1 * time.Hour
	config.MaxConnIdleTime = 30 * time.Minute
	config.HealthCheckPeriod = 1 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	// Verify connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &Pool{Pool: pool}, nil
}

// WithTx executes fn in a transaction, rolling back on error.
func (p *Pool) WithTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := p.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// Migrate runs all pending migrations.
func (p *Pool) Migrate(ctx context.Context) error {
	// Create schema and version table
	_, err := p.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS beacon;

		CREATE TABLE IF NOT EXISTS beacon.schema_version (
			version INT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
	`)
	if err != nil {
		return fmt.Errorf("create schema version table: %w", err)
	}

	// Get current version
	var currentVersion int
	err = p.QueryRow(ctx, `
		SELECT COALESCE(MAX(version), 0) FROM beacon.schema_version
	`).Scan(&currentVersion)
	if err != nil {
		return fmt.Errorf("get current version: %w", err)
	}

	// Read migration files
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations directory: %w", err)
	}

	// Sort migration files
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)

	// Apply pending migrations
	for _, file := range files {
		// Extract version number from filename (e.g., "001_core.sql" -> 1)
		var version int
		_, err := fmt.Sscanf(file, "%03d_", &version)
		if err != nil {
			continue // Skip files that don't match pattern
		}

		if version <= currentVersion {
			continue // Already applied
		}

		// Read migration file
		content, err := migrationsFS.ReadFile("migrations/" + file)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", file, err)
		}

		// Apply migration in transaction
		err = p.WithTx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(content)); err != nil {
				return fmt.Errorf("execute migration: %w", err)
			}

			if _, err := tx.Exec(ctx, `
				INSERT INTO beacon.schema_version (version) VALUES ($1)
			`, version); err != nil {
				return fmt.Errorf("record migration version: %w", err)
			}

			return nil
		})
		if err != nil {
			return fmt.Errorf("apply migration %s: %w", file, err)
		}
	}

	return nil
}
