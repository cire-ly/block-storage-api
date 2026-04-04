# block-storage-api

Pluggable block storage API in Go — Ceph, Lustre, NVMe-oF backends with FSM volume lifecycle.

Demo project for **Scaleway Senior Software Engineer** application.

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
| Ceph backend | go-ceph / librbd (`-tags ceph`) |

---

## Quick start

### No infrastructure (mock backend)

```bash
go run ./cmd/api
# or
make run
```

### With Docker Compose (API + PostgreSQL)

```bash
docker-compose up
```

The API listens on `http://localhost:8080`.

---

## Environment variables

| Variable | Default | Description |
|----------|---------|-------------|
| `STORAGE_BACKEND` | `mock` | `mock` \| `ceph` \| `lustre` \| `nvmeof` |
| `CONSISTENCY_STRATEGY` | `cp` | `cp` (strong consistency) \| `ap` (availability) |
| `DATABASE_URL` | _(empty)_ | `postgres://user:pass@host/db?sslmode=disable` |
| `PORT` | `8080` | HTTP listen port |
| `ENV` | `development` | `development` \| `production` |
| `CEPH_MONITORS` | — | Comma-separated monitor addresses |
| `CEPH_POOL` | `rbd-demo` | Ceph RBD pool |
| `CEPH_KEYRING` | `/etc/ceph/ceph.client.admin.keyring` | Keyring path |
| `OTEL_EXPORTER` | `stdout` | `stdout` \| `jaeger` |
| `OTEL_JAEGER_ENDPOINT` | `http://localhost:4318/v1/traces` | OTLP HTTP endpoint |
| `OTEL_SERVICE_NAME` | `block-storage-api` | OTel service name |

---

## REST endpoints

```
POST   /api/v1/volumes              Create a volume
GET    /api/v1/volumes              List volumes
GET    /api/v1/volumes/{name}       Get a volume
PUT    /api/v1/volumes/{name}/attach   Attach to a node
PUT    /api/v1/volumes/{name}/detach   Detach
DELETE /api/v1/volumes/{name}       Delete
GET    /healthz                     Backend health
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

# Health
curl -s http://localhost:8080/healthz | jq
```

---

## Volume FSM lifecycle

```mermaid
stateDiagram-v2
    [*] --> pending
    attached --> detaching: detach
    attaching --> attached: attached
    attaching --> error: error
    available --> attaching: attach
    available --> deleting: delete
    creating --> error: error
    creating --> available: ready
    deleting --> deleted: deleted
    deleting --> error: error
    detaching --> available: detached
    detaching --> error: error
    pending --> creating: create
```

The `error` transition is available from any in-progress state: `creating | attaching | detaching | deleting`.

---

## CAP strategy

**CP (default)** — strong consistency, availability sacrificed if quorum is not reached. The healthcheck returns 503 when the backend is degraded.

**AP** — availability maintained. Responses include:
```
X-Data-Staleness: true
X-Data-Timestamp: now
```

```bash
CONSISTENCY_STRATEGY=ap make run
```

---

## Backends

### Mock (default)
In-memory backend. Enforces all business constraints: duplicate names, invalid size, forbidden FSM transitions. Uses `sync.RWMutex` (RLock for reads, Lock for writes).

### Ceph RBD
Requires `librados-dev`, `librbd-dev` and a running Ceph cluster.

```bash
# Install Microceph locally (Linux)
sudo ./scripts/setup-ceph.sh

# Run with Ceph backend
STORAGE_BACKEND=ceph \
CEPH_MONITORS=127.0.0.1:6789 \
CEPH_POOL=rbd-demo \
go run -tags ceph ./cmd/api
```

### Lustre / NVMe-oF
Functional stubs — the interface is fully implemented, kernel-level integration (lfs, configfs) is ready to plug in.

---

## Development

```bash
make test       # go test ./... -race
make coverage   # HTML coverage report (target ≥ 70%)
make lint       # golangci-lint
make migrate    # apply SQL migrations
```

### Coverage of critical packages

| Package | Coverage |
|---------|----------|
| `fsm` | 100% |
| `storage/mock` | ≥ 83% |
| `api` (handlers) | ≥ 78% |
| `config` | ≥ 90% |

---

## Architecture

### Hard rules

1. `api/`, `fsm/`, `observability/` **never** import a concrete backend package.
2. Only `cmd/api/setup.go` knows the concrete backend via `setupBackend()`.
3. Every FSM transition is logged in `volume_events` (audit trail).
4. All interface methods take `context.Context` as their first argument.
5. Interfaces are defined on the consumer side, not the producer side.

### ResourcesRegistry pattern

`cmd/api/setup.go` initialises each resource in an explicit order and shuts them down LIFO via `Shutdown()`.

```
setupConfig → setupLogger → setupObservability → setupDatabase
           → setupMigrations → setupBackend → setupHTTP
```

### Observability

- **Traces**: every HTTP handler + every backend operation (`trace.Span`)
- **Metrics**: `volume.operations.total` (counter per operation)
- **Logs**: `log/slog` JSON, trace ID injected into every entry via middleware
- **Exporter**: stdout (dev) or Jaeger OTLP HTTP (prod)
