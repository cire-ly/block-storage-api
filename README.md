# block-storage-api

> Pluggable block storage API in Go — Ceph backend with FSM volume lifecycle and retry policy.

[![CI](https://github.com/cire-ly/block-storage-api/actions/workflows/ci-cd.yml/badge.svg)](https://github.com/cire-ly/block-storage-api/actions/workflows/ci-cd.yml)
[![Go Version](https://img.shields.io/badge/go-1.24-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/cire-ly/block-storage-api)](https://goreportcard.com/report/github.com/cire-ly/block-storage-api)

---

## Live demo

Deployed on a **Scaleway DEV1-S instance** (Paris, fr-par-1):

|| Endpoint | URL |
|----------|-----|
| Health check | http://163.172.144.70:8080/healthz |
| Swagger UI | http://163.172.144.70:8080/swagger/index.html |
| Grafana (logs) | http://163.172.144.70:3000 |

```bash
curl -s http://163.172.144.70:8080/healthz | jq
```

---

## Stack

| Component | Choice |
|-----------|--------|
| Language | Go 1.24 |
| Router | [chi v5](https://github.com/go-chi/chi) |
| FSM | [looplab/fsm](https://github.com/looplab/fsm) |
| Database | PostgreSQL (pgx v5) |
| Migrations | golang-migrate |
| Observability | OpenTelemetry (traces + metrics) |
| Logs | Loki + Grafana |
| Ceph backend | go-ceph / librbd (`-tags ceph`) |
| API docs | Swagger (swaggo) |
| CI/CD | GitHub Actions → ghcr.io |
| Deployment | Docker Compose on Scaleway DEV1-S |

---

## Architecture

Hexagonal architecture with a feature-based package structure
inspired by production Go codebases.

```
block-storage-api/
├── cmd/api/               # entrypoint + ResourcesRegistry (startup/shutdown)
├── config/                # env vars + validation
├── assertor/              # lightweight dependency validation
├── internal/
│   ├── db/                # shared pgx pool + golang-migrate (private)
│   └── observability/     # OpenTelemetry setup (private)
├── volume/                # main feature — hexagonal architecture
│   ├── contract.go        # FeatureContract + ApplicationContract
│   ├── dependency.go      # dependency interfaces (consumer-side)
│   ├── application.go     # pure business logic — zero transport imports
│   ├── factory.go         # wiring + WaitGroup shutdown
│   ├── controller_http.go # HTTP transport only — zero business logic
│   ├── fsm.go             # FSM states, transitions, retry policy
│   └── repository/
│       ├── postgres.go    # PostgreSQL impl of DatabaseDependency
│       └── inmemory.go    # in-memory impl for local dev
├── storage/
│   ├── backend.go         # VolumeBackend interface + Volume type
│   ├── mock/              # in-memory backend (default, no deps)
│   └── ceph/              # Ceph RBD via go-ceph (-tags ceph)
└── transport/
    └── nvmeof/            # NVMe-oF target (transport layer, not a backend)
```

### Feature pattern

The `volume/` package is self-contained:

- **`application.go`** — zero imports from `net/http`, `chi`, or any storage impl
- **`controller_http.go`** — zero business logic, only HTTP ↔ ApplicationContract
- **`repository/postgres.go`** — implements `DatabaseDependency`, only known by `setup.go`
- **`factory.go`** — wires everything, WaitGroup + LIFO shutdown

```go
feat, err := volume.NewVolumeFeature(volume.NewVolumeFeatureParams{
    Logger:      logger,
    Backend:     storageBackend,
    DB:          volumeRepo,
    Tracer:      tracer,
    Meter:       meter,
    Router:      router,
    RetryPolicy: volume.RetryPolicy{MaxAttempts: 3, InitialWait: 500*time.Millisecond, ...},
})
```

### Goroutine lifecycle — WaitGroup

`VolumeFeature` tracks every internal goroutine with a `sync.WaitGroup` to guarantee
a clean shutdown with no leaks.

```go
type VolumeFeature struct {
    wg        sync.WaitGroup     // tracks all internal goroutines
    cancelCtx context.CancelFunc // cancels them on shutdown
    ...
}
```

**Shutdown sequence** (`VolumeFeature.Close`):

1. `f.cancelCtx()` — signals all goroutines via `ctx.Done()`
2. `f.wg.Wait()` — blocks until every goroutine has exited (bounded by caller deadline)
3. LIFO closers — remaining resources closed in reverse init order

**Retry goroutines** check `ctx.Done()` at two points:

```go
for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
    select {
    case <-ctx.Done():
        return // canceled before attempt — stop silently
    default:
    }

    err := op(ctx)
    if err == nil { return } // success

    select {
    case <-ctx.Done():
        return // canceled during backoff — stop silently
    case <-time.After(backoff):
    }
}
```

**Startup reconciliation** runs each stuck volume in parallel:

```go
var wg sync.WaitGroup
for _, vol := range transitionalVolumes {
    wg.Add(1)
    go func(v *storage.Volume) {
        defer wg.Done()
        f.reconcileVolume(ctx, v)
    }(vol)
}
wg.Wait()
```

---

## Volume FSM lifecycle

```mermaid
stateDiagram-v2
    [*] --> pending
    pending --> creating: create
    creating --> available: ready
    creating --> creating_failed: error
    creating_failed --> creating: retry
    creating_failed --> error: fail

    available --> attaching: attach
    attaching --> attached: attached
    attaching --> attaching_failed: error
    attaching_failed --> attaching: retry
    attaching_failed --> error: fail

    attached --> detaching: detach
    detaching --> available: detached
    detaching --> detaching_failed: error
    detaching_failed --> detaching: retry
    detaching_failed --> error: fail

    available --> deleting: delete
    deleting --> deleted: deleted
    deleting --> deleting_failed: error
    deleting_failed --> deleting: retry
    deleting_failed --> error: fail

    error --> pending: reset
```

### States

| State | Description |
|-------|-------------|
| `pending` | Volume requested |
| `creating` | Backend provisioning in progress |
| `creating_failed` | Provisioning failed — retry pending |
| `available` | Ready for use |
| `attaching` | Attach to node in progress |
| `attaching_failed` | Attach failed — retry pending |
| `attached` | Mounted on a node |
| `detaching` | Detach in progress |
| `detaching_failed` | Detach failed — retry pending |
| `deleting` | Deletion in progress |
| `deleting_failed` | Deletion failed — retry pending |
| `deleted` | Removed |
| `error` | Terminal failure — manual `reset` required |

### Retry policy

Every in-progress state has a `*_failed` intermediate with exponential backoff:

| Parameter | Default | Env variable |
|-----------|---------|--------------|
| `MaxAttempts` | `3` | `VOLUME_RETRY_MAX_ATTEMPTS` |
| `InitialWait` | `500ms` | `VOLUME_RETRY_INITIAL_WAIT` |
| `Multiplier` | `2.0` | `VOLUME_RETRY_MULTIPLIER` |
| `MaxWait` | `10s` | `VOLUME_RETRY_MAX_WAIT` |

Delays: 500ms → 1s → 2s → terminal `error` after `MaxAttempts`.

Every FSM transition is persisted in `volume_events` (audit trail for RCA).

---

## REST endpoints

```
POST   /api/v1/volumes                  Create a volume
GET    /api/v1/volumes                  List volumes
GET    /api/v1/volumes/{name}           Get a volume
PUT    /api/v1/volumes/{name}/attach    Attach to a node
PUT    /api/v1/volumes/{name}/detach    Detach
DELETE /api/v1/volumes/{name}           Delete
POST   /api/v1/volumes/{name}/reset     Reset from error → pending
GET    /healthz                         Backend health check
GET    /swagger/index.html              Swagger UI
```

### Examples

```bash
# Create
curl -s -X POST http://localhost:8080/api/v1/volumes \
  -H 'Content-Type: application/json' \
  -d '{"name":"vol-01","size_mb":1024}' | jq

# List
curl -s http://localhost:8080/api/v1/volumes | jq

# Attach
curl -s -X PUT http://localhost:8080/api/v1/volumes/vol-01/attach \
  -H 'Content-Type: application/json' \
  -d '{"node_id":"node-paris-01"}' | jq

# Detach
curl -s -X PUT http://localhost:8080/api/v1/volumes/vol-01/detach | jq

# Delete
curl -s -X DELETE http://localhost:8080/api/v1/volumes/vol-01

# Reset from error state
curl -s -X POST http://localhost:8080/api/v1/volumes/vol-01/reset | jq

# Health
curl -s http://localhost:8080/healthz | jq
```

---

## Quick start

### Mock backend (no infrastructure)

```bash
go run ./cmd/api
# or
make run
```

### Docker Compose (API + PostgreSQL + Loki + Grafana)

```bash
docker-compose up
```

- API: `http://localhost:8080`
- Swagger: `http://localhost:8080/swagger/index.html`
- Grafana: `http://localhost:3000`

### Ceph backend

```bash
# Install Microceph locally (Linux)
sudo ./scripts/setup-ceph.sh

# Run with Ceph backend
STORAGE_BACKEND=ceph \
CEPH_MONITORS=127.0.0.1:6789 \
CEPH_POOL=rbd-demo \
go run -tags ceph ./cmd/api
```

---

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `STORAGE_BACKEND` | `mock` | `mock` \| `ceph` |
| `DATABASE_URL` | _(empty)_ | `postgres://user:pass@host/db?sslmode=disable` |
| `PORT` | `8080` | HTTP listen port |
| `ENV` | `development` | `development` \| `production` |
| `CEPH_MONITORS` | — | Comma-separated monitor addresses |
| `CEPH_POOL` | `rbd-demo` | Ceph RBD pool name |
| `CEPH_KEYRING` | `/etc/ceph/ceph.client.admin.keyring` | Keyring path |
| `OTEL_EXPORTER` | `stdout` | `stdout` \| `jaeger` |
| `OTEL_JAEGER_ENDPOINT` | `http://localhost:4318/v1/traces` | OTLP HTTP endpoint |
| `OTEL_SERVICE_NAME` | `block-storage-api` | OTel service name |
| `VOLUME_RETRY_MAX_ATTEMPTS` | `3` | Max retry attempts before `error` state |
| `VOLUME_RETRY_INITIAL_WAIT` | `500ms` | Initial backoff delay |
| `VOLUME_RETRY_MULTIPLIER` | `2.0` | Exponential backoff multiplier |
| `VOLUME_RETRY_MAX_WAIT` | `10s` | Maximum backoff delay |

---

## Development

```bash
make test         # go test ./... -race
make coverage     # HTML coverage report (target ≥ 70%)
make lint         # golangci-lint
make migrate      # apply SQL migrations
make migrate-down # rollback last migration
```

### Test results

```
ok   config
ok   storage/mock
ok   transport/nvmeof
ok   volume
ok   volume/repository
```

### Coverage targets

| Package | Target |
|---------|--------|
| `volume/fsm` | 100% |
| `storage/mock` | ≥ 83% |
| `volume/controller_http` | ≥ 78% |
| `volume/application` | ≥ 80% |
| `config` | ≥ 90% |
| `storage/ceph` | excluded (requires live cluster) |

---

## Deployment

### CI/CD

Every push to `main` triggers GitHub Actions:
1. Build Docker image
2. Push to `ghcr.io/cire-ly/block-storage-api:latest`

### Update production

```bash
ssh root@163.172.144.70
cd /app/block-storage-api
docker compose pull && docker compose up -d
docker compose logs -f
```

### Observability — Grafana + Loki

```logql
# HTTP traffic
{service="block-storage-api"} | json | msg="http request"
  | line_format "{{.method}} {{.path}} → {{.status}} ({{.duration_ms}}ms)"

# Errors only
{service="block-storage-api"} | json | level="ERROR"

# HTTP errors 4xx/5xx
{service="block-storage-api"} | json | msg="http request" | status >= 400
```

---

## Context propagation

Context flows through every layer — never dropped or replaced with `context.Background()`:

| Layer | Timeout |
|-------|---------|
| Backend operations (Ceph) | 30s |
| DB queries | 5s |
| Health check | 3s |
| Graceful shutdown | 10s |

The HTTP server's `BaseContext` is set to the application lifecycle context (`appCtx`).
When SIGTERM fires, `appCtx` is canceled, propagating through all request contexts to
retry goroutines, which stop at the next `ctx.Done()` check.
