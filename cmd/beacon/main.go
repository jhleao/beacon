// Package main is the entry point for the Beacon webhook delivery service.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"beacon/internal/capture"
	"beacon/internal/config"
	"beacon/internal/controlplane/api"
	"beacon/internal/controlplane/service"
	"beacon/internal/db"
	"beacon/internal/dispatcher"
	"beacon/internal/httpdeliver"
	"beacon/internal/janitor"
	"beacon/internal/observability"
	"beacon/internal/outbox"
	"beacon/internal/version"

	"github.com/prometheus/client_golang/prometheus"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "serve":
		runServe()
	case "migrate":
		runMigrate()
	case "version", "-v", "--version":
		fmt.Printf("beacon %s (commit: %s, built: %s)\n",
			version.Version, version.Commit, version.BuildDate)
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Beacon - PostgreSQL-native webhook delivery

Usage:
  beacon <command>

Commands:
  serve     Start the Beacon server
  migrate   Run database migrations
  version   Show version information
  help      Show this help message

Environment Variables:
  DATABASE_URL                  PostgreSQL connection string (required)
  BEACON_HTTP_ADDR              Control plane listen address (default: :8080)
  BEACON_POLL_INTERVAL          Outbox poll interval (default: 100ms)
  BEACON_BATCH_SIZE             Events claimed per poll (default: 100)
  BEACON_WORKER_COUNT           Concurrent delivery workers (default: 10)
  BEACON_HMAC_SECRET            Global HMAC signing secret (optional)
  BEACON_CONTROLPLANE_SECRET    Bearer token for API auth (required)
  BEACON_LOG_LEVEL              Log level: debug, info, warn, error (default: info)
  BEACON_LOG_FORMAT             Log format: json, text (default: json)
  BEACON_RETENTION_HOURS        Retention period for delivered events (default: 168)
  BEACON_JANITOR_INTERVAL       Janitor cleanup interval (default: 1h)
  BEACON_JANITOR_BATCH_SIZE     Max events cleaned per cycle (default: 1000)`)
}

func runServe() {
	// Load environment config
	envCfg, err := config.LoadEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(1)
	}

	// Setup logger
	logger, err := observability.NewLogger(envCfg.LogLevel, envCfg.LogFormat)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger error: %v\n", err)
		os.Exit(1)
	}

	// Setup metrics
	registry := prometheus.NewRegistry()
	metrics := observability.NewMetrics(registry)

	// Create context with signal handling
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger.Info("starting beacon",
		"http_addr", envCfg.HTTPAddr,
		"poll_interval", envCfg.PollInterval,
		"batch_size", envCfg.BatchSize,
		"worker_count", envCfg.WorkerCount,
	)

	// Connect to database
	pool, err := db.New(ctx, envCfg.DatabaseURL)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	logger.Info("connected to database")

	// Run migrations
	if err := pool.Migrate(ctx); err != nil {
		logger.Error("migration failed", "error", err)
		os.Exit(1)
	}

	// Create components
	repo := outbox.New(pool)
	installer := capture.New(pool)
	hmacSecret := config.LoadHMACSecret()
	client := httpdeliver.NewClient(hmacSecret)

	// Create dispatcher
	dispatcherCfg := dispatcher.Config{
		PollInterval: envCfg.PollInterval,
		BatchSize:    envCfg.BatchSize,
		WorkerCount:  envCfg.WorkerCount,
	}
	disp := dispatcher.New(pool, repo, client, dispatcherCfg, logger)

	// Create control plane
	applySvc := service.NewApplyService(pool, installer)
	drainSvc := service.NewDrainService(pool, logger)
	apiServer := api.NewServer(pool, applySvc, envCfg.HTTPAddr, envCfg.ControlPlaneSecret, logger, metrics)

	// Start drain service
	go func() {
		if err := drainSvc.RunDrainLoop(ctx); err != nil && ctx.Err() == nil {
			logger.Error("drain loop failed", "error", err)
		}
	}()

	// Start janitor
	janitorCfg := janitor.Config{
		RetentionDuration: time.Duration(envCfg.RetentionHours) * time.Hour,
		Interval:          envCfg.JanitorInterval,
		BatchSize:         envCfg.JanitorBatchSize,
	}
	jan := janitor.New(repo, janitorCfg, logger, metrics)
	go jan.Run(ctx)

	// Start control plane API
	go func() {
		if err := apiServer.Start(ctx); err != nil && ctx.Err() == nil {
			logger.Error("API server failed", "error", err)
		}
	}()

	// Start dispatcher (blocking)
	go func() {
		if err := disp.Start(ctx); err != nil && ctx.Err() == nil {
			logger.Error("dispatcher failed", "error", err)
		}
	}()

	// Wait for shutdown signal
	<-ctx.Done()
	logger.Info("shutting down...")

	// Stop dispatcher gracefully
	disp.Stop()
	disp.Wait()

	logger.Info("shutdown complete")
}

func runMigrate() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL environment variable is required")
		os.Exit(1)
	}

	ctx := context.Background()

	pool, err := db.New(ctx, databaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to connect to database: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	fmt.Println("Running migrations...")
	if err := pool.Migrate(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Migrations complete!")
}
