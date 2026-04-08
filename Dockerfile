# Build stage — Ceph dev headers required for CGO compilation.
FROM golang:1.26-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    librados-dev librbd-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -tags ceph -o block-storage-api ./cmd/api

# Runtime stage — Ceph shared libs required to load the backend at startup.
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    librados2 librbd1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/block-storage-api .
COPY --from=builder /app/internal/db/migrations ./internal/db/migrations

EXPOSE 8080

# STORAGE_BACKEND=mock (default) or ceph — switch at runtime via env var.
CMD ["./block-storage-api"]
