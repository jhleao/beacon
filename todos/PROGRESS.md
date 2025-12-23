# Beacon Implementation Progress

## Overview
Building a PostgreSQL-native webhook delivery system based on the specs in `spec/`.

## Phase 1: Project Foundation
- [x] Initialize Go module and dependencies
- [x] Create directory structure
- [x] Create Makefile
- [x] Create docker-compose.yaml
- [x] Create .env.example
- [x] Create .gitignore

## Phase 2: Database Module (internal/db)
- [x] Create Pool wrapper with pgxpool
- [x] Implement connection pool configuration
- [x] Implement WithTx transaction helper
- [x] Implement Migrate function
- [x] Create SQL migrations (001_core.sql)
- [ ] Write database tests

## Phase 3: Config Module (internal/config)
- [x] Define YAML config types (BeaconConfig, DestinationConfig, SubscriptionConfig)
- [x] Implement YAML parsing with defaults
- [x] Implement validation logic
- [x] Implement environment variable loading (EnvConfig)
- [x] Implement secret loading (HMAC, control plane)
- [x] Implement ParseTable helper
- [x] Write config tests

## Phase 4: Capture Module (internal/capture)
- [x] Implement Installer struct
- [x] Implement SQL identifier quoting (ddl.go)
- [x] Implement InstallTrigger (idempotent)
- [x] Implement UninstallTrigger
- [x] Implement ListTriggers
- [x] Add capture_changes() and extract_pk() to migrations
- [x] Write capture tests (integration + DDL unit tests)

## Phase 5: Outbox Module (internal/outbox)
- [x] Define Event type
- [x] Define State constants
- [x] Define Destination type
- [x] Define ClaimedEvent type
- [x] Implement Repository struct
- [x] Implement Claim with FOR UPDATE SKIP LOCKED
- [x] Implement Ack
- [x] Implement Reschedule
- [x] Implement ToDead (with snapshot)
- [x] Implement RecordAttempt
- [x] Implement CountByState
- [x] Implement CountPendingForSubscription
- [x] Write outbox tests (integration with testcontainers)

## Phase 6: Retry Module (internal/dispatcher/retry)
- [x] Define constants (BaseDelay, MaxDelay, MaxAttempts, JitterRatio)
- [x] Implement NextDelay (exponential backoff with jitter)
- [x] Implement ShouldRetry
- [x] Implement IsRetryableError
- [x] Implement IsRetryableStatus
- [x] Write retry tests

## Phase 7: HTTP Delivery Module (internal/httpdeliver)
- [x] Implement SSRFGuard with blocked ranges
- [x] Implement DNS caching for SSRF
- [x] Implement SSRFPolicy support
- [x] Implement Signer (HMAC-SHA256)
- [x] Implement Client struct
- [x] Implement Deliver method with all features
- [x] Write httpdeliver tests (signer, ssrf, client)

## Phase 8: Dispatcher Module (internal/dispatcher)
- [x] Implement Dispatcher struct and Config
- [x] Implement worker ID generation
- [x] Implement main polling loop
- [x] Implement worker pool
- [x] Implement processEvent
- [x] Implement failure handling
- [x] Implement Semaphores for per-destination concurrency
- [x] Implement heartbeat loop
- [x] Implement reaper loop
- [x] Implement graceful shutdown
- [ ] Write dispatcher tests

## Phase 9: Observability Module (internal/observability)
- [x] Implement Metrics with Prometheus
- [x] Implement all metric types (delivery, outbox, worker, API)
- [x] Implement Logger with slog
- [x] Implement HealthResponse struct
- [x] Write observability tests (metrics, logging)

## Phase 10: Control Plane Module (internal/controlplane)
- [x] Implement ApplyService
- [x] Implement diff algorithm for destinations/subscriptions
- [x] Implement Apply logic
- [x] Implement DryRun
- [x] Implement Export
- [x] Implement DrainService
- [x] Implement HTTP server
- [x] Implement auth middleware
- [x] Implement all routes (apply, config, validate, health, metrics, etc.)
- [ ] Implement replay endpoint
- [ ] Write controlplane tests

## Phase 11: Main Entry Point (cmd/beacon)
- [x] Create main.go with CLI commands
- [x] Implement serve command
- [x] Implement migrate command
- [x] Wire up all dependencies
- [x] Implement graceful shutdown

## Phase 12: Development Tooling
- [x] Create scripts/webhook-receiver.go
- [x] Create scripts/seed.sh
- [x] Create scripts/prometheus.yml
- [x] Create testdata/config.yaml
- [x] Create test utilities (internal/testutil)

## Phase 13: Testing & Integration
- [ ] Write unit tests for all modules
- [ ] Run all integration tests
- [ ] Manual end-to-end testing
- [ ] Fix any issues

---

## Current Status
Core implementation complete! Tests passing for all core modules:
- Config, retry, httpdeliver, observability: Unit tests
- Outbox, capture: Integration tests with testcontainers

Next: Add database pool tests and consider control plane tests.

## Milestones Completed
- [x] Phase 1: Project Foundation
- [x] Phase 2: Database Module (implementation)
- [x] Phase 3: Config Module (implementation)
- [x] Phase 4: Capture Module (implementation)
- [x] Phase 5: Outbox Module (implementation)
- [x] Phase 6: Retry Module (implementation)
- [x] Phase 7: HTTP Delivery Module (implementation)
- [x] Phase 8: Dispatcher Module (implementation)
- [x] Phase 9: Observability Module (implementation)
- [x] Phase 10: Control Plane Module (implementation)
- [x] Phase 11: Main Entry Point
- [x] Phase 12: Development Tooling (partial)

## Git Commits Made
1. feat: add project foundation and core modules
2. feat: add outbox and retry modules
3. feat: add HTTP delivery module
4. feat: add dispatcher module with worker pool
5. feat: add observability module
6. feat: add control plane module
7. feat: add main entry point and development scripts
8. test: add unit tests for core modules
9. test: add HTTP client delivery tests
10. test: add outbox repository integration tests
11. test: add capture module integration tests
