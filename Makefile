.PHONY: help up down logs migrate run test lint build clean seed webhook psql

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
	@docker compose exec postgres pg_isready -U beacon -d beacon_dev || true

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

# Dependencies
deps:
	go mod download
	go mod tidy
