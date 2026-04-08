# Build stage — Ceph Reef (18.x) headers required by go-ceph v0.38+.
# Debian bookworm ships Quincy (17.x) which lacks rbd_encryption_load2 and
# RBD_ENCRYPTION_FORMAT_LUKS — added in Reef. We pin the official Ceph repo.
FROM golang:1.26-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends gnupg2 curl && \
    curl -fsSL https://download.ceph.com/keys/release.asc \
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

# Runtime stage — copy Ceph shared libs directly from builder to avoid
# reconfiguring the Ceph apt repo (which causes dependency conflicts on slim).
# Runtime stage
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Copy Ceph Reef shared libs from builder
COPY --from=builder /usr/lib/x86_64-linux-gnu/librados.so.2* /usr/lib/x86_64-linux-gnu/
COPY --from=builder /usr/lib/x86_64-linux-gnu/librbd.so.1* /usr/lib/x86_64-linux-gnu/
COPY --from=builder /usr/lib/x86_64-linux-gnu/libceph-common.so.2* /usr/lib/x86_64-linux-gnu/

# Update linker cache
RUN ldconfig

WORKDIR /app
COPY --from=builder /app/block-storage-api .
COPY --from=builder /app/internal/db/migrations ./internal/db/migrations

EXPOSE 8080
CMD ["./block-storage-api"]