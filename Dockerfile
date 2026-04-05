# Build stage
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Dependencies
COPY go.mod go.sum ./
RUN go mod download

# Build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o block-storage-api ./cmd/api

# Runtime stage
FROM alpine:3.19

WORKDIR /app

# Ca binaire uniquement
COPY --from=builder /app/block-storage-api .
COPY --from=builder /app/internal/db/migrations ./internal/db/migrations

EXPOSE 8080

CMD ["./block-storage-api"]