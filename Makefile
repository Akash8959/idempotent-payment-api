.PHONY: help run test test-race test-integration cover lint migrate-up migrate-down docker-up docker-down clean

SHELL := /bin/bash
GO ?= go

# Default target
all: test lint

help: ## Display list of available commands
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

run: ## Run the API service locally
	$(GO) run ./cmd/api

test: ## Run unit tests
	$(GO) test -v ./internal/...

test-race: ## Run all unit tests with race detection
	$(GO) test -v -race -count=1 ./internal/...

test-integration: ## Run full integration tests against real PostgreSQL testcontainer
	$(GO) test -v -timeout 10m ./tests/integration/...

cover: ## Run tests and print statement coverage per package and total
	@echo "=== Calculating Package Test Coverage ==="
	@$(GO) test -coverprofile=coverage.out ./internal/...
	@$(GO) tool cover -func=coverage.out
	@echo ""
	@echo "=== Core Packages Coverage ==="
	@$(GO) test -cover ./internal/payment ./internal/idempotency

lint: ## Run go vet and static checks
	$(GO) vet ./...
	@if command -v golangci-lint > /dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed, skipping advanced linter"; \
	fi

migrate-up: ## Apply all database migrations up
	$(GO) run ./cmd/api -migrate-up || $(GO) run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate -path ./migrations -database "postgres://postgres:postgres@localhost:5432/payment_db?sslmode=disable" up

migrate-down: ## Rollback database migrations down
	$(GO) run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate -path ./migrations -database "postgres://postgres:postgres@localhost:5432/payment_db?sslmode=disable" down 1

docker-up: ## Start PostgreSQL and API via Docker Compose
	docker compose up --build -d

docker-down: ## Stop Docker Compose containers and remove volumes
	docker compose down -v

clean: ## Clean build binaries and test artifacts
	rm -rf bin/ coverage.out coverage.html
