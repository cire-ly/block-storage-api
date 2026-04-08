# block-storage-api

> Pluggable block storage API in Go — Ceph backend with FSM volume lifecycle, retry policy, and real-time SSE streaming.

[![CI](https://github.com/cire-ly/block-storage-api/actions/workflows/ci-cd.yml/badge.svg)](https://github.com/cire-ly/block-storage-api/actions/workflows/ci-cd.yml)
[![Go Version](https://img.shields.io/badge/go-1.26-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/cire-ly/block-storage-api)](https://goreportcard.com/report/github.com/cire-ly/block-storage-api)

---

## Live demo

Deployed on a **Scaleway DEV1-S instance** (Paris, fr-par-1):

| Endpoint | URL |
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
| Language | Go 1.26 |
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
| Tailscale VPN | Ceph node connectivity (NAT traversal) |
| cephadm | Ceph cluster deployment (Docker-based) |
| ghcr.io | Container registry |

---

## Infrastructure & Data Flow

![Infrastructure](docs/block-storage-api-architecture.png)


---

## Architecture

Hexagonal architecture with a feature-based package structure
inspired by production Go codebases.

```
block-storage-api/
├── cmd/
│   └── api/
│       ├── main.go              # minimal entrypoint (~20 lines)
│       ├── setup.go             # ResourcesRegistry — Setup() + Shutdown() LIFO
│       ├── backend_mock.go      # newBackend() — mock (//go:build !ceph)
│       └── backend_ceph.go      # newBackend() — Ceph  (//go:build ceph)
├── config/
│   └── config.go                # env vars + validation + RetryPolicy + ReconcilePolicy
├── assertor/
│   └── assertor.go              # lightweight dependency validation
├── internal/                    # private — not importable externally
│   ├── db/
│   │   ├── postgres.go          # pgxpool connection
│   │   ├── migrate.go           # golang-migrate runner
│   │   └── migrations/          # SQL migration files
│   └── observability/
│       ├── tracing.go           # OpenTelemetry tracer provider
│       └── metrics.go           # OpenTelemetry meter provider
├── volume/                      # main feature — hexagonal architecture
│   ├── contract.go              # FeatureContract + ApplicationContract
│   ├── dependency.go            # dependency interfaces (consumer-side)
│   ├── application.go           # pure business logic — zero transport imports
│   ├── factory.go               # wiring + WaitGroup + cancelCtx + bgCtx
│   ├── controller_http.go       # HTTP transport only — zero business logic
│   ├── fsm.go                   # FSM states, transitions, RetryPolicy
│   └── repository/
│       ├── postgres.go          # PostgreSQL impl of DatabaseDependency
│       └── inmemory.go          # in-memory impl for tests
├── storage/
│   ├── backend.go               # VolumeBackend interface + Volume type
│   ├── mock/
│   │   ├── mock.go              # in-memory backend (default, no deps)
│   │   └── mock_test.go
│   └── ceph/
│       ├── ceph.go              # Ceph RBD via go-ceph (-tags ceph)
│       └── stub.go              # stub without -tags ceph (//go:build !ceph)
├── transport/
│   └── nvmeof/
│       ├── target.go            # NVMe-oF target (transport, not a backend)
│       └── target_test.go
├── grafana/
│   └── provisioning/
│       ├── datasources/
│       │   └── loki.yaml        # Loki datasource — auto-provisioned at startup
│       └── dashboards/
│           ├── dashboard.yaml   # dashboard provider config
│           └── block-storage-api.json  # 4-panel dashboard
├── docs/
│   ├── docs.go                  # Swagger generated docs
│   ├── swagger.json
│   ├── block-storage-api-architecture.png   # infrastructure diagram
│   └── block-storage-api-infrastructure.html # animated diagram
├── .github/
│   └── workflows/
│       └── ci-cd.yml            # lint → test → build-and-push → deploy (manual)
├── Dockerfile                   # multi-stage: golang:1.26-bookworm + debian:bookworm-slim
├── docker-compose.yml           # API + PostgreSQL + Loki + Grafana
├── Makefile                     # run, run-ceph, test, coverage, lint, migrate
├── .golangci.yml                # golangci-lint v2 config
├── CLAUDE.md                    # architecture rules for Claude Code
├── LICENSE
└── README.md
```

### Feature pattern

The `volume/` package is self-contained:

- **`application.go`** — zero imports from `net/http`, `chi`, or any storage impl
- **`controller_http.go`** — zero business logic, only HTTP ↔ ApplicationContract
- **`repository/postgres.go`** — implements `DatabaseDependency`, only known by `setup.go`
- **`factory.go`** — wires everything, WaitGroup + LIFO shutdown

```go
feat, err := volume.NewVolumeFeature(volume.NewVolumeFeatureParams{
    Logger:          logger,
    Backend:         storageBackend,
    DB:              volumeRepo,
    Tracer:          tracer,
    Meter:           meter,
    Router:          router,
    RetryPolicy:     volume.RetryPolicy{MaxAttempts: 3, InitialWait: 500*time.Millisecond},
    ReconcilePolicy: config.ReconcilePolicy{DBOnly: "error", CephOnly: "ignore"},
})
```

### NVMe-oF — transport, not a backend

NVMe-oF is a **transport layer** — it exposes an existing volume over the network.
It does NOT store data and does NOT implement `VolumeBackend`.

```
Ceph RBD ──► NVMe-oF Target ──► Initiator (sees /dev/nvme1n1 as local disk)
```

### Ceph CAP strategy

Ceph is **CP by default** — managed at pool level, not application level:

```bash
ceph osd pool set rbd-demo min_size 2  # refuse writes if quorum not met
ceph osd pool set rbd-demo size 3
```

### Goroutine lifecycle — WaitGroup

`VolumeFeature` tracks every internal goroutine with `sync.WaitGroup`:

```go
type VolumeFeature struct {
    wg        sync.WaitGroup     // tracks all internal goroutines
    cancelCtx context.CancelFunc // cancels them on shutdown
    bgCtx     context.Context    // independent of HTTP request context
}
```

**Shutdown sequence** (`VolumeFeature.Close`):

1. `f.cancelCtx()` — signals all goroutines via `ctx.Done()`
2. `f.wg.Wait()` — blocks until every goroutine has exited
3. LIFO closers — remaining resources closed in reverse init order

**Retry goroutines** check `ctx.Done()` before each attempt and during backoff:

```go
for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
    select {
    case <-ctx.Done():
        return // canceled — stop silently
    default:
    }
    err := op(ctx)
    if err == nil { return }
    select {
    case <-ctx.Done():
        return
    case <-time.After(backoff):
    }
}
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

    error --> pending: reconcile (absent from backend)
    error --> available: reconcile (exists, not attached)
    error --> attached: reconcile (exists, attached to nodeX)
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
| `error` | Terminal failure — recovery via `POST /reconcile` |

### Retry policy

Every in-progress state has a `*_failed` intermediate with exponential backoff:

| Parameter | Default | Env variable |
|-----------|---------|--------------|
| `MaxAttempts` | `3` | `VOLUME_RETRY_MAX_ATTEMPTS` |
| `InitialWait` | `500ms` | `VOLUME_RETRY_INITIAL_WAIT` |
| `Multiplier` | `2.0` | `VOLUME_RETRY_MULTIPLIER` |
| `MaxWait` | `10s` | `VOLUME_RETRY_MAX_WAIT` |

Delays: 500ms → 1s → 2s → terminal `error` after `MaxAttempts`.

Every FSM transition is persisted in `volume_events` (full audit trail for RCA).

**Important:** retry is triggered only by **Ceph backend errors** — never by FSM
transition errors. An invalid transition (e.g. DELETE on an `attached` volume)
returns `409 Conflict` immediately with no retry.

### Error recovery — reconcile

`error` is a terminal state. Recovery is explicit via `POST /reconcile`:

| Real backend state | FSM target |
|--------------------|------------|
| Backend unreachable | stays `error` — returns 503 |
| Volume absent | `pending` |
| Volume exists, not attached | `available` |
| Volume exists, attached to node X | `attached` |

### Startup reconciliation

On startup, volumes stuck in transitional states are reconciled against the
real Ceph state. The policy is configurable:

| Variable | Default | Description |
|----------|---------|-------------|
| `RECONCILE_DB_ONLY` | `error` | Volume in DB but absent from Ceph: `error` \| `delete` \| `ignore` |
| `RECONCILE_CEPH_ONLY` | `ignore` | Volume in Ceph but absent from DB: `ignore` \| `import` |

---

## REST endpoints

```
POST   /api/v1/volumes                    Create a volume
GET    /api/v1/volumes                    List volumes
GET    /api/v1/volumes/{name}             Get a volume
GET    /api/v1/volumes/{name}/events      Stream state changes (SSE)
PUT    /api/v1/volumes/{name}/attach      Attach to a node
PUT    /api/v1/volumes/{name}/detach      Detach
DELETE /api/v1/volumes/{name}             Delete
POST   /api/v1/volumes/{name}/reconcile   Align FSM with real Ceph state
GET    /healthz                           Backend health check
GET    /swagger/index.html                Swagger UI
```

### Server-Sent Events — real-time state streaming

Volume operations are asynchronous (202 Accepted). The `/events` endpoint streams
FSM state transitions in real time — no polling needed:

```bash
# Terminal 1 — open stream immediately after create
curl -N http://163.172.144.70:8080/api/v1/volumes/vol-01/events

# Output:
# data: {"name":"vol-01","state":"creating","event":"current","timestamp":"..."}
# data: {"name":"vol-01","state":"available","event":"ready","timestamp":"..."}
# event: done
# data: {"name":"vol-01","state":"available"}
```

The handler sends the **current state immediately** on connection, then pushes
future transitions. The stream closes automatically on terminal states
(`available`, `attached`, `deleted`, `error`).

### Examples

```bash
# Create
curl -s -X POST http://localhost:8080/api/v1/volumes \
  -H 'Content-Type: application/json' \
  -d '{"name":"vol-01","size_mb":1024}' | jq

# Stream state changes
curl -N http://localhost:8080/api/v1/volumes/vol-01/events

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

# Reconcile from error state
curl -s -X POST http://localhost:8080/api/v1/volumes/vol-01/reconcile | jq

# Health
curl -s http://localhost:8080/healthz | jq
```

---

## Quick start

### Mock backend (no infrastructure)

```bash
make run
```

### Docker Compose (API + PostgreSQL + Loki + Grafana)

```bash
docker-compose up
```

- API: `http://localhost:8080`
- Swagger: `http://localhost:8080/swagger/index.html`
- Grafana: `http://localhost:3000` — datasource and dashboards provisioned automatically

### Ceph backend

```bash
STORAGE_BACKEND=ceph \
CEPH_MONITORS=127.0.0.1:6789 \
CEPH_POOL=rbd-demo \
make run-ceph
```

### Backend selection — the CGO nuance

| Context | Build flags | Ceph available |
|---------|-------------|----------------|
| `make run` | no `-tags ceph`, `CGO_ENABLED=0` | no — stub returns error |
| `make run-ceph` | `-tags ceph`, needs local libs | yes |
| `docker build` | `-tags ceph` inside builder image | yes |

The `storage/ceph/` package ships a `stub.go` (`//go:build !ceph`) that makes
the package importable without the C libs, returning an explicit error if Ceph
is selected at runtime without the real implementation compiled in.

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
| `RECONCILE_DB_ONLY` | `error` | `error` \| `delete` \| `ignore` |
| `RECONCILE_CEPH_ONLY` | `ignore` | `ignore` \| `import` |

---

## Development

```bash
make test         # go test ./... -race
make coverage     # HTML coverage report
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

---

## Deployment

### CI/CD pipeline

```
push → lint → test → build-and-push (main only)
                            ↓
               deploy via workflow_dispatch (manual trigger)
```

Build is automatic on every push to `main`. Deploy to production is **manual** —
triggered via GitHub Actions → Run workflow. This prevents accidental deploys
and allows grouping multiple commits into a single release.

The deploy step copies `grafana/` and `docker-compose.yml` to the VPS via SCP,
then restarts the containers.

### Update production manually

```bash
ssh root@163.172.144.70
cd /app/block-storage-api
docker compose pull && docker compose up -d
docker compose logs -f
```

### Observability — Grafana + Loki

Grafana datasource and dashboards are **provisioned automatically** at startup
via `grafana/provisioning/` — no manual configuration needed after `docker compose up`.

```logql
# HTTP traffic
{service="block-storage-api"} | json | msg="http request"
  | line_format "{{.method}} {{.path}} → {{.status}} ({{.duration_ms}}ms)"

# Errors only
{service="block-storage-api"} | json | level="ERROR"

# HTTP errors 4xx/5xx
{service="block-storage-api"} | json | msg="http request" | status=~"4.*|5.*"

# System / startup logs
{service="block-storage-api"} | json | level="INFO" | msg!="http request"
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
When SIGTERM fires, `appCtx` is canceled, propagating through all layers to retry
goroutines which stop at the next `ctx.Done()` check. The `bgCtx` used by retry
goroutines is independent of the HTTP request context — it lives for the full
duration of the application, not the request.
