.PHONY: run run-ceph test coverage lint migrate migrate-down build docker-up docker-down

BINARY   = block-storage-api
DATABASE_URL ?= postgres://blockstore:blockstore@localhost:5432/blockstore?sslmode=disable

# ── Dev ──────────────────────────────────────────────────────────────────────

run:
	STORAGE_BACKEND=mock go run ./cmd/api

run-ceph:
	STORAGE_BACKEND=ceph go run -tags ceph ./cmd/api

build:
	go build -o $(BINARY) ./cmd/api

# ── Tests ────────────────────────────────────────────────────────────────────

test:
	go test ./... -race -count=1

coverage:
	go test ./... -race -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@go tool cover -func=coverage.out | tail -1

lint:
	golangci-lint run ./...

# ── Database ─────────────────────────────────────────────────────────────────

migrate:
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/api -migrate-only

migrate-down:
	migrate -path internal/db/migrations -database "$(DATABASE_URL)" down 1

# ── Docker ───────────────────────────────────────────────────────────────────

docker-up:
	docker-compose up --build

docker-down:
	docker-compose down -v
