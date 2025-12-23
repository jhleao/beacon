# Beacon Implementation Progress

## Overview
Building a PostgreSQL-native webhook delivery system based on the specs in `spec/`.

## Phase 1: Project Foundation
- [ ] Initialize Go module and dependencies
- [ ] Create directory structure
- [ ] Create Makefile
- [ ] Create docker-compose.yaml
- [ ] Create .env.example
- [ ] Create .gitignore

## Phase 2: Database Module (internal/db)
- [ ] Create Pool wrapper with pgxpool
- [ ] Implement connection pool configuration
- [ ] Implement WithTx transaction helper
- [ ] Implement Migrate function
- [ ] Create SQL migrations (001_core.sql)
- [ ] Write database tests

## Phase 3: Config Module (internal/config)
- [ ] Define YAML config types (BeaconConfig, DestinationConfig, SubscriptionConfig)
- [ ] Implement YAML parsing with defaults
- [ ] Implement validation logic
- [ ] Implement environment variable loading (EnvConfig)
- [ ] Implement secret loading (HMAC, control plane)
- [ ] Implement ParseTable helper
- [ ] Write config tests

## Phase 4: Capture Module (internal/capture)
- [ ] Implement Installer struct
- [ ] Implement SQL identifier quoting (ddl.go)
- [ ] Implement InstallTrigger (idempotent)
- [ ] Implement UninstallTrigger
- [ ] Implement ListTriggers
- [ ] Implement EnsureFunctions
- [ ] Add capture_changes() and extract_pk() to migrations
- [ ] Write capture tests

## Phase 5: Outbox Module (internal/outbox)
- [ ] Define Event type
- [ ] Define State constants
- [ ] Define Destination type
- [ ] Define ClaimedEvent type
- [ ] Implement Repository struct
- [ ] Implement Claim with FOR UPDATE SKIP LOCKED
- [ ] Implement Ack
- [ ] Implement Reschedule
- [ ] Implement ToDead (with snapshot)
- [ ] Implement RecordAttempt
- [ ] Implement CountByState
- [ ] Implement CountPendingForSubscription
- [ ] Write outbox tests

## Phase 6: Retry Module (internal/dispatcher/retry)
- [ ] Define constants (BaseDelay, MaxDelay, MaxAttempts, JitterRatio)
- [ ] Implement NextDelay (exponential backoff with jitter)
- [ ] Implement ShouldRetry
- [ ] Implement IsRetryableError
- [ ] Implement IsRetryableStatus
- [ ] Write retry tests

## Phase 7: HTTP Delivery Module (internal/httpdeliver)
- [ ] Implement SSRFGuard with blocked ranges
- [ ] Implement DNS caching for SSRF
- [ ] Implement SSRFPolicy support
- [ ] Implement Signer (HMAC-SHA256)
- [ ] Implement Client struct
- [ ] Implement Deliver method with all features
- [ ] Write httpdeliver tests

## Phase 8: Dispatcher Module (internal/dispatcher)
- [ ] Implement Dispatcher struct and Config
- [ ] Implement worker ID generation
- [ ] Implement main polling loop
- [ ] Implement worker pool
- [ ] Implement processEvent
- [ ] Implement failure handling
- [ ] Implement Semaphores for per-destination concurrency
- [ ] Implement heartbeat loop
- [ ] Implement reaper loop
- [ ] Implement graceful shutdown
- [ ] Write dispatcher tests

## Phase 9: Observability Module (internal/observability)
- [ ] Implement Metrics with Prometheus
- [ ] Implement all metric types (delivery, outbox, worker, API)
- [ ] Implement Logger with slog
- [ ] Implement HealthResponse struct
- [ ] Write observability tests

## Phase 10: Control Plane Module (internal/controlplane)
- [ ] Implement ApplyService
- [ ] Implement diff algorithm for destinations/subscriptions
- [ ] Implement Apply logic
- [ ] Implement DryRun
- [ ] Implement Export
- [ ] Implement DrainService
- [ ] Implement HTTP server
- [ ] Implement auth middleware
- [ ] Implement all routes (apply, config, validate, health, metrics, etc.)
- [ ] Implement replay endpoint
- [ ] Write controlplane tests

## Phase 11: Main Entry Point (cmd/beacon)
- [ ] Create main.go with CLI commands
- [ ] Implement serve command
- [ ] Implement migrate command
- [ ] Wire up all dependencies
- [ ] Implement graceful shutdown

## Phase 12: Development Tooling
- [ ] Create scripts/webhook-receiver.go
- [ ] Create scripts/seed.sh
- [ ] Create scripts/prometheus.yml
- [ ] Create testdata/config.yaml
- [ ] Create test utilities (internal/testutil)

## Phase 13: Testing & Integration
- [ ] Run all unit tests
- [ ] Run all integration tests
- [ ] Manual end-to-end testing
- [ ] Fix any issues

---

## Current Status
Starting Phase 1...

## Milestones Completed
(none yet)
