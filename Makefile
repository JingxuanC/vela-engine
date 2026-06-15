.PHONY: build test run clean dev docker-up docker-down lint

# Build the API server binary
build:
	go build -o bin/server ./cmd/server

# Run all tests
test:
	go test ./... -v -count=1 -timeout 60s

# Run tests with race detector
test-race:
	go test ./... -race -count=1 -timeout 60s

# Run tests with coverage
test-cover:
	go test ./... -coverprofile=coverage.out -count=1
	go tool cover -html=coverage.out -o coverage.html

# Run the API server locally (requires Redis + PostgreSQL)
run:
	go run ./cmd/server

# Run with live reload (requires air)
dev:
	air

# Lint
lint:
	go vet ./...
	go fmt ./...

# Clean
clean:
	rm -rf bin/ tmp/

# Docker operations
docker-up:
	docker compose --profile legacy up -d postgres redis go-api

docker-down:
	docker compose --profile legacy down

docker-up-legacy:
	docker compose --profile legacy up -d

# Database
migrate:
	go run ./cmd/server -migrate

# Install tools
tools:
	go install github.com/air-verse/air@latest
	go install honnef.co/go/tools/cmd/staticcheck@latest

# Full CI check
ci: lint test build
	@echo "CI OK"
