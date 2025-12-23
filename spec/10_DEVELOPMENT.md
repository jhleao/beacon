# 10. Development Specification

## Purpose

Defines the local development setup, tooling, and workflows for developing and testing Beacon. Prioritizes fast feedback loops and realistic testing against real PostgreSQL.

---

## Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| Go | 1.22+ | Build and test |
| Docker | 24+ | PostgreSQL and test containers |
| Docker Compose | 2.20+ | Local stack orchestration |
| psql | 16+ | Database inspection (optional) |
| curl | any | API testing |
| jq | any | JSON formatting (optional) |

---

## Quick Start

```bash
# 1. Clone and enter directory
git clone https://github.com/yourorg/beacon.git
cd beacon

# 2. Start dependencies
make up

# 3. Run migrations
make migrate

# 4. Start Beacon
make run

# 5. Apply sample config
make seed

# 6. Run tests
make test
```

---

## Project Layout

```
beacon/
├── cmd/
│   └── beacon/
│       └── main.go           # Entry point
├── internal/                  # Application code
├── spec/                      # Specifications
├── migrations/                # SQL migrations
├── testdata/                  # Test fixtures
│   ├── config.yaml           # Sample config
│   └── seed.sql              # Sample data
├── scripts/                   # Dev scripts
│   ├── seed.sh               # Seed script
│   └── webhook-receiver.go   # Test webhook server
├── docker-compose.yaml        # Local stack
├── Makefile                   # Dev commands
├── .env.example              # Environment template
└── .env                      # Local overrides (gitignored)
```

---

## Docker Compose

### `docker-compose.yaml`

```yaml
services:
  postgres:
    image: postgres:16-alpine
    container_name: beacon-postgres
    ports:
      - "5432:5432"
    environment:
      POSTGRES_USER: beacon
      POSTGRES_PASSWORD: beacon
      POSTGRES_DB: beacon_dev
    volumes:
      - postgres_data:/var/lib/postgresql/data
      - ./migrations:/docker-entrypoint-initdb.d:ro
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U beacon -d beacon_dev"]
      interval: 5s
      timeout: 5s
      retries: 5

  # Test webhook receiver (echoes requests)
  webhook:
    build:
      context: ./scripts
      dockerfile: Dockerfile.webhook
    container_name: beacon-webhook
    ports:
      - "9000:9000"
    environment:
      - PORT=9000
      - FAIL_RATE=0  # Set 0-100 to simulate failures

  # Optional: Prometheus for metrics
  prometheus:
    image: prom/prometheus:latest
    container_name: beacon-prometheus
    ports:
      - "9090:9090"
    volumes:
      - ./scripts/prometheus.yml:/etc/prometheus/prometheus.yml:ro
    profiles:
      - monitoring

  # Optional: Grafana for dashboards
  grafana:
    image: grafana/grafana:latest
    container_name: beacon-grafana
    ports:
      - "3000:3000"
    environment:
      - GF_SECURITY_ADMIN_PASSWORD=admin
    volumes:
      - grafana_data:/var/lib/grafana
    profiles:
      - monitoring

volumes:
  postgres_data:
  grafana_data:
```

### Profiles

```bash
# Basic stack (postgres + webhook)
docker compose up -d

# With monitoring
docker compose --profile monitoring up -d
```

---

## Environment Configuration

### `.env.example`

```bash
# Database
DATABASE_URL=postgres://beacon:beacon@localhost:5432/beacon_dev?sslmode=disable

# Beacon server
BEACON_HTTP_ADDR=:8080
BEACON_POLL_INTERVAL=100ms
BEACON_BATCH_SIZE=100
BEACON_WORKER_COUNT=10

# Secrets (generate your own for production)
BEACON_HMAC_SECRET=dev-hmac-secret-change-in-prod
BEACON_CONTROLPLANE_SECRET=dev-control-secret-change-in-prod

# Logging
BEACON_LOG_LEVEL=debug
BEACON_LOG_FORMAT=text

# Limits
BEACON_MAX_PAYLOAD_BYTES=1048576
```

### Setup

```bash
cp .env.example .env
# Edit .env as needed
source .env  # Or use direnv
```

---

## Makefile

```makefile
.PHONY: help up down logs migrate run test lint build clean seed webhook

# Default target
help:
	@echo "Beacon Development Commands"
	@echo ""
	@echo "  make up        - Start docker compose stack"
	@echo "  make down      - Stop docker compose stack"
	@echo "  make logs      - Tail postgres logs"
	@echo "  make migrate   - Run database migrations"
	@echo "  make run       - Run Beacon locally"
	@echo "  make test      - Run all tests"
	@echo "  make test-unit - Run unit tests only"
	@echo "  make test-int  - Run integration tests"
	@echo "  make lint      - Run linters"
	@echo "  make build     - Build binary"
	@echo "  make seed      - Apply sample config"
	@echo "  make webhook   - Start test webhook receiver"
	@echo "  make psql      - Open psql shell"
	@echo "  make clean     - Clean build artifacts"

# Load .env if exists
ifneq (,$(wildcard ./.env))
    include .env
    export
endif

# Docker
up:
	docker compose up -d
	@echo "Waiting for postgres..."
	@sleep 2
	@docker compose exec postgres pg_isready -U beacon -d beacon_dev

down:
	docker compose down

logs:
	docker compose logs -f postgres

# Database
migrate:
	go run ./cmd/beacon migrate

psql:
	docker compose exec postgres psql -U beacon -d beacon_dev

reset-db:
	docker compose down -v
	docker compose up -d postgres
	@sleep 2
	$(MAKE) migrate

# Application
run:
	go run ./cmd/beacon serve

build:
	go build -o bin/beacon ./cmd/beacon

# Testing
test:
	go test ./... -v -race

test-unit:
	go test ./... -v -short

test-int:
	go test ./... -v -run Integration

test-cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html

# Linting
lint:
	golangci-lint run

fmt:
	go fmt ./...
	goimports -w .

# Development helpers
seed:
	@./scripts/seed.sh

webhook:
	go run ./scripts/webhook-receiver.go

# Cleanup
clean:
	rm -rf bin/ coverage.out coverage.html
```

---

## Test Webhook Receiver

A simple HTTP server that echoes webhook requests for local testing.

### `scripts/webhook-receiver.go`

```go
//go:build ignore

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9000"
	}

	failRate, _ := strconv.Atoi(os.Getenv("FAIL_RATE"))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		// Log request
		log.Printf("[%s] %s %s", r.Method, r.URL.Path, r.RemoteAddr)
		log.Printf("  Headers:")
		for k, v := range r.Header {
			if len(k) > 6 && k[:6] == "Beacon" {
				log.Printf("    %s: %s", k, v[0])
			}
		}

		// Pretty print JSON body
		var pretty map[string]any
		if json.Unmarshal(body, &pretty) == nil {
			formatted, _ := json.MarshalIndent(pretty, "  ", "  ")
			log.Printf("  Body:\n  %s", formatted)
		} else {
			log.Printf("  Body: %s", body)
		}

		// Simulate failures
		if failRate > 0 && rand.Intn(100) < failRate {
			status := []int{500, 502, 503, 504}[rand.Intn(4)]
			log.Printf("  -> Simulated failure: %d", status)
			w.WriteHeader(status)
			fmt.Fprintf(w, `{"error": "simulated failure"}`)
			return
		}

		// Success
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"received": true, "timestamp": "%s"}`, time.Now().Format(time.RFC3339))
		log.Printf("  -> 200 OK")
	})

	// Slow endpoint for timeout testing
	http.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		delay := 10 * time.Second
		if d := r.URL.Query().Get("delay"); d != "" {
			if parsed, err := time.ParseDuration(d); err == nil {
				delay = parsed
			}
		}
		log.Printf("[%s] /slow - sleeping %s", r.Method, delay)
		time.Sleep(delay)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"delayed": true}`)
	})

	// Always fail endpoint
	http.HandleFunc("/fail", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[%s] /fail - returning 500", r.Method)
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error": "always fails"}`)
	})

	// 4xx endpoint
	http.HandleFunc("/bad-request", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[%s] /bad-request - returning 400", r.Method)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `{"error": "bad request"}`)
	})

	log.Printf("Webhook receiver listening on :%s", port)
	log.Printf("Endpoints:")
	log.Printf("  /           - Echo (FAIL_RATE=%d%%)", failRate)
	log.Printf("  /slow?delay=10s - Delayed response")
	log.Printf("  /fail       - Always 500")
	log.Printf("  /bad-request - Always 400")
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
```

### `scripts/Dockerfile.webhook`

```dockerfile
FROM golang:1.22-alpine
WORKDIR /app
COPY webhook-receiver.go .
CMD ["go", "run", "webhook-receiver.go"]
```

---

## Seed Script

### `scripts/seed.sh`

```bash
#!/bin/bash
set -euo pipefail

# Load environment
if [ -f .env ]; then
    source .env
fi

BEACON_URL="${BEACON_HTTP_ADDR:-localhost:8080}"
SECRET="${BEACON_CONTROLPLANE_SECRET:-dev-control-secret-change-in-prod}"

echo "Creating test table..."
docker compose exec -T postgres psql -U beacon -d beacon_dev <<'SQL'
CREATE TABLE IF NOT EXISTS public.users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    status TEXT DEFAULT 'active',
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS public.orders (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID REFERENCES public.users(id),
    total NUMERIC(10,2) NOT NULL,
    status TEXT DEFAULT 'pending',
    created_at TIMESTAMPTZ DEFAULT now()
);
SQL

echo "Applying Beacon config..."
curl -s -X POST "http://${BEACON_URL}/v1/apply" \
    -H "Authorization: Bearer ${SECRET}" \
    -H "Content-Type: application/x-yaml" \
    -d '
version: 1

destinations:
  - name: local-webhook
    url: http://webhook:9000/
    timeout_ms: 5000
    max_in_flight: 10

  - name: slow-webhook
    url: http://webhook:9000/slow?delay=2s
    timeout_ms: 10000
    max_in_flight: 5

subscriptions:
  - name: users-insert
    table: public.users
    operation: INSERT
    destination: local-webhook

  - name: users-update
    table: public.users
    operation: UPDATE
    destination: local-webhook
    trigger_on: [email, name, status]
    select: [id, email, name, status, updated_at]

  - name: orders-insert
    table: public.orders
    operation: INSERT
    destination: local-webhook
    select: [id, user_id, total, status]
' | jq .

echo ""
echo "Seed complete! Try these commands:"
echo ""
echo "  # Insert a user (triggers webhook)"
echo "  make psql"
echo "  INSERT INTO users (email, name) VALUES ('test@example.com', 'Test User');"
echo ""
echo "  # Watch webhook receiver"
echo "  docker compose logs -f webhook"
```

---

## Sample Config

### `testdata/config.yaml`

```yaml
version: 1

destinations:
  - name: analytics
    url: https://analytics.example.com/events
    method: POST
    timeout_ms: 5000
    max_in_flight: 50
    headers:
      X-Source: beacon
      X-Environment: development

  - name: audit-log
    url: https://audit.internal:8443/ingest
    timeout_ms: 10000
    max_in_flight: 20
    ssrf_policy:
      allow_private: true

  - name: slack
    url: https://hooks.slack.com/services/XXX/YYY/ZZZ
    timeout_ms: 3000
    max_in_flight: 5

subscriptions:
  # User lifecycle events
  - name: users-created
    table: public.users
    operation: INSERT
    destination: analytics

  - name: users-updated
    table: public.users
    operation: UPDATE
    destination: analytics
    trigger_on: [email, name, plan_id]
    select: [id, email, name, plan_id, updated_at]

  - name: users-deleted
    table: public.users
    operation: DELETE
    destination: audit-log

  # Order events
  - name: orders-created
    table: public.orders
    operation: INSERT
    destination: analytics
    select: [id, user_id, total, created_at]

  - name: orders-status-changed
    table: public.orders
    operation: UPDATE
    destination: analytics
    trigger_on: [status]
    select: [id, status, updated_at]

  # High-value order alerts
  - name: large-orders-slack
    table: public.orders
    operation: INSERT
    destination: slack
    select: [id, user_id, total]
```

---

## Testing Workflows

### Manual Testing

```bash
# 1. Start everything
make up
make run &  # Run in background or separate terminal

# 2. Apply config and create tables
make seed

# 3. Watch webhook receiver
docker compose logs -f webhook

# 4. In another terminal, insert data
make psql
# Then in psql:
INSERT INTO users (email, name) VALUES ('alice@example.com', 'Alice');
UPDATE users SET name = 'Alice Smith' WHERE email = 'alice@example.com';
```

### Testing Retries

```bash
# Start webhook with 50% failure rate
FAIL_RATE=50 docker compose up -d webhook

# Insert data and watch retries
make psql
INSERT INTO users (email, name) VALUES ('retry-test@example.com', 'Retry Test');

# Check outbox state
SELECT id, state, attempts, last_error, visible_at
FROM beacon.outbox_events
ORDER BY created_at DESC LIMIT 10;
```

### Testing Dead Letters

```bash
# Use always-fail endpoint
curl -X POST "http://localhost:8080/v1/apply" \
    -H "Authorization: Bearer ${BEACON_CONTROLPLANE_SECRET}" \
    -H "Content-Type: application/x-yaml" \
    -d '
version: 1
destinations:
  - name: failing
    url: http://webhook:9000/fail
    timeout_ms: 1000
    max_in_flight: 5
subscriptions:
  - name: users-to-fail
    table: public.users
    operation: INSERT
    destination: failing
'

# Insert and wait for DLQ (max ~19 minutes with default retry)
# Or reduce MaxAttempts for faster testing

# Check dead letters
SELECT event_id, reason, dead_at FROM beacon.dead_letters;
```

### Load Testing

```bash
# Generate load
make psql
INSERT INTO users (email, name)
SELECT
    'user-' || generate_series || '@example.com',
    'User ' || generate_series
FROM generate_series(1, 1000);

# Monitor metrics
curl -s http://localhost:8080/v1/metrics | grep beacon_

# Check outbox depth
SELECT state, COUNT(*) FROM beacon.outbox_events GROUP BY state;
```

---

## Debugging

### Inspect Outbox

```sql
-- Pending events
SELECT * FROM beacon.outbox_events
WHERE state = 'pending'
ORDER BY visible_at LIMIT 20;

-- Stuck in delivering (potential crash)
SELECT * FROM beacon.outbox_events
WHERE state = 'delivering'
AND locked_at < now() - interval '1 minute';

-- Recent failures
SELECT id, attempts, last_error, visible_at
FROM beacon.outbox_events
WHERE last_error IS NOT NULL
ORDER BY visible_at DESC LIMIT 20;

-- Dead letters
SELECT
    dl.event_id,
    dl.reason,
    dl.dead_at,
    dl.replay_count,
    dl.snapshot->>'destination_name' as destination
FROM beacon.dead_letters dl
ORDER BY dl.dead_at DESC;
```

### Inspect Workers

```sql
-- Active workers
SELECT * FROM beacon.worker_heartbeats
ORDER BY last_heartbeat DESC;

-- Stale workers (should be reaped)
SELECT * FROM beacon.worker_heartbeats
WHERE last_heartbeat < now() - interval '30 seconds';
```

### Check Subscriptions

```sql
-- Active subscriptions
SELECT
    s.name,
    s.table_schema || '.' || s.table_name as table,
    s.operation,
    d.name as destination,
    s.enabled,
    s.draining
FROM beacon.subscriptions s
JOIN beacon.destinations d ON d.id = s.destination_id
WHERE s.deleted_at IS NULL;

-- Event counts per subscription
SELECT
    s.name,
    COUNT(*) FILTER (WHERE e.state = 'pending') as pending,
    COUNT(*) FILTER (WHERE e.state = 'delivering') as delivering,
    COUNT(*) FILTER (WHERE e.state = 'delivered') as delivered,
    COUNT(*) FILTER (WHERE e.state = 'dead') as dead
FROM beacon.subscriptions s
LEFT JOIN beacon.outbox_events e ON e.subscription_id = s.id
GROUP BY s.id, s.name;
```

### Log Levels

```bash
# Verbose logging
BEACON_LOG_LEVEL=debug make run

# Structured JSON (for production-like testing)
BEACON_LOG_LEVEL=info BEACON_LOG_FORMAT=json make run
```

---

## IDE Setup

### VS Code

`.vscode/settings.json`:
```json
{
  "go.testFlags": ["-v", "-race"],
  "go.testTimeout": "120s",
  "go.lintTool": "golangci-lint",
  "go.lintFlags": ["--fast"]
}
```

`.vscode/launch.json`:
```json
{
  "version": "0.2.0",
  "configurations": [
    {
      "name": "Run Beacon",
      "type": "go",
      "request": "launch",
      "mode": "auto",
      "program": "${workspaceFolder}/cmd/beacon",
      "args": ["serve"],
      "envFile": "${workspaceFolder}/.env"
    },
    {
      "name": "Debug Test",
      "type": "go",
      "request": "launch",
      "mode": "test",
      "program": "${fileDirname}",
      "envFile": "${workspaceFolder}/.env"
    }
  ]
}
```

### GoLand / IntelliJ

1. Run Configuration → Go Build
2. Package path: `beacon/cmd/beacon`
3. Program arguments: `serve`
4. Environment: Load from `.env` file

---

## CI Integration

### GitHub Actions Example

```yaml
name: CI

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest

    services:
      postgres:
        image: postgres:16-alpine
        env:
          POSTGRES_USER: beacon
          POSTGRES_PASSWORD: beacon
          POSTGRES_DB: beacon_test
        ports:
          - 5432:5432
        options: >-
          --health-cmd pg_isready
          --health-interval 10s
          --health-timeout 5s
          --health-retries 5

    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'

      - name: Run tests
        env:
          DATABASE_URL: postgres://beacon:beacon@localhost:5432/beacon_test?sslmode=disable
        run: go test ./... -v -race -coverprofile=coverage.out

      - name: Upload coverage
        uses: codecov/codecov-action@v4
        with:
          files: coverage.out

  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: golangci/golangci-lint-action@v4
```

---

## Common Issues

| Issue | Solution |
|-------|----------|
| "connection refused" to postgres | Run `make up` and wait for healthcheck |
| Webhook not receiving events | Check Beacon is running (`make run`) and config applied (`make seed`) |
| Events stuck in "delivering" | Worker may have crashed; reaper will reclaim after 30s |
| "SSRF blocked" error | Webhook URL resolves to private IP; use `ssrf_policy.allow_private: true` |
| Tests timeout | Ensure Docker is running; testcontainers needs Docker daemon |
| "permission denied" on scripts | Run `chmod +x scripts/*.sh` |

---

## Dependencies

Development dependencies (install separately):

```bash
# Linting
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# Import formatting
go install golang.org/x/tools/cmd/goimports@latest

# SQL formatting (optional)
go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
```
