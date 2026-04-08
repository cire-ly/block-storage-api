# Build stage — Ceph Reef (18.x) headers required by go-ceph v0.38+.
# Debian bookworm ships Quincy (17.x) which lacks rbd_encryption_load2 and
# RBD_ENCRYPTION_FORMAT_LUKS — added in Reef. We pin the official Ceph repo.
FROM golang:1.26-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends gnupg2 && \
    wget -qO- https://download.ceph.com/keys/release.asc \
        | gpg --dearmor > /usr/share/keyrings/ceph.gpg && \
    echo "deb [signed-by=/usr/share/keyrings/ceph.gpg] \
        https://download.ceph.com/debian-reef/ bookworm main" \
        > /etc/apt/sources.list.d/ceph.list && \
    apt-get update && apt-get install -y --no-install-recommends \
        librados-dev librbd-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -tags ceph -o block-storage-api ./cmd/api

# Runtime stage — debian:bookworm (non-slim) already ships all transitive deps
# of librados2/librbd1. We only add the Ceph Reef repo for those two packages.
FROM debian:bookworm

RUN apt-get update && apt-get install -y --no-install-recommends gnupg2 wget ca-certificates && \
    wget -qO- https://download.ceph.com/keys/release.asc \
        | gpg --dearmor > /usr/share/keyrings/ceph.gpg && \
    echo "deb [signed-by=/usr/share/keyrings/ceph.gpg] \
        https://download.ceph.com/debian-reef/ bookworm main" \
        > /etc/apt/sources.list.d/ceph.list && \
    apt-get update && apt-get install -y --no-install-recommends \
        librados2 librbd1 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/block-storage-api .
COPY --from=builder /app/internal/db/migrations ./internal/db/migrations

EXPOSE 8080

# STORAGE_BACKEND=mock (default) or ceph — switch at runtime via env var.
CMD ["./block-storage-api"]
