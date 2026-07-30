# ims-backend

The Go REST API behind the Inventory Management System: authentication and RBAC, product
catalog, multi-warehouse stock with an append-only movement ledger and FIFO cost layers,
purchasing with a PO approval workflow, sales with pick-pack-ship fulfilment, reporting, and
notifications. It is the **single source of truth for the API contract** — the web and mobile
clients generate their code from the OpenAPI spec this repo publishes, and never hand-write
DTOs.

| Repository | What it is |
|---|---|
| **ims-backend** (this repo) | Go + Fiber + PostgreSQL API. Owns `docs/openapi.yaml`. |
| [`ims-web`](../ims-web) | React + TypeScript web client. |
| [`ims-mobile`](../ims-mobile) | Flutter app for warehouse-floor scanning. |

Contract: [`docs/openapi.yaml`](docs/openapi.yaml) · Interactive docs: `http://localhost:8080/docs/index.html`

---

## Quick start

Everything runs in Docker; nothing but Docker and Make is required on the host.

```bash
git clone git@github.com:<org>/ims-backend.git
cd ims-backend
cp .env.example .env          # then set JWT_SECRET: openssl rand -hex 32

make dev                      # Postgres + Redis + Mailpit + API + worker
make seed                     # realistic demo data (idempotent)
```

| Service | URL |
|---|---|
| API | http://localhost:8080 |
| Swagger UI | http://localhost:8080/docs/index.html |
| Mailpit (catches outbound email) | http://localhost:8025 |
| Asynq queue dashboard | http://localhost:8081 |

The seed prints four sign-in accounts, one per role, all with the password
`Password123!`:

| Email | Role | Can do |
|---|---|---|
| `admin@acme.test` | admin | everything, including user management |
| `manager@acme.test` | manager | approve purchase orders and returns, edit the catalog |
| `warehouse@acme.test` | warehouse_staff | scan, adjust, transfer, count, receive, ship |
| `viewer@acme.test` | viewer | read-only |

Check it is alive:

```bash
curl -s localhost:8080/health/ready | jq
curl -s -X POST localhost:8080/api/v1/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@acme.test","password":"Password123!"}' | jq '.data.tokens.access_token'
```

### Running without Docker

Requires Go 1.26+, Postgres 16+ and (optionally) Redis.

```bash
createdb ims && psql ims -c "CREATE USER ims WITH PASSWORD 'ims' SUPERUSER"
make run          # applies migrations on boot when AUTO_MIGRATE=true
make run-worker   # in a second terminal
```

Postgres **16 or newer** is required: the `stock_items` natural key uses
`UNIQUE NULLS NOT DISTINCT`, without which rows with a NULL location or batch would
duplicate freely.

Redis is optional in development. Without it the cache misses, the rate limiter fails open
and background jobs are dropped with a warning — the API keeps serving, which is the same
degradation the production uptime target relies on.

---

## Commands

```bash
make help              # list every target

make dev               # start the local stack
make down / make clean # stop / stop and wipe volumes
make logs              # tail API and worker logs
make seed              # load demo data

make test              # unit tests
make test-integration  # integration tests (needs Docker; uses testcontainers)
make cover             # unit + integration with the 70% coverage gate on internal/
make lint              # golangci-lint
make vet               # go vet, including integration-tagged files
make fmt               # gofmt

make docs              # regenerate docs/openapi.yaml from code annotations
make migrate-new NAME=add_widgets
make migrate-up / make migrate-down
```

---

## How it is put together

```
cmd/
  api/         HTTP server
  worker/      background jobs (email, push, low-stock and expiry scans)
  seed/        demo data loader
internal/
  api/         dependency wiring and every route (start here)
  auth/        JWT + refresh rotation, RBAC matrix, users, login audit
  product/     catalog: products, variants, categories, units, barcode scan
  warehouse/   warehouses and their bin/shelf location tree
  stock/       the ledger, FIFO cost layers, batches, transfers, cycle counts
  purchasing/  suppliers, purchase orders, approvals, goods receipts
  sales/       customers, sales orders, invoices, returns
  reporting/   valuation, turnover, reorder suggestions, dashboard, CSV export
  notification/ in-app, email and push delivery
  middleware/  request id, access log, error rendering, auth, RBAC, rate limit
  platform/    config, logging, Postgres, Redis, queue, health probes
  shared/      error taxonomy, response envelope, pagination, audit, sequences
migrations/    golang-migrate SQL pairs, embedded into the binary
pkg/money/     fixed-point decimal used for every quantity and amount
```

### Decisions worth knowing before you change anything

**Everything that moves stock goes through `stock.Ledger.Apply`.** It locks the
`stock_items` row, refuses to drive the balance negative, writes one immutable
`stock_movements` row with before/after quantities, and creates or consumes FIFO cost
layers — all inside the caller's transaction. A ten-line goods receipt is one atomic unit.
Writing to `stock_items` directly bypasses the audit trail and loses updates under
concurrency, so don't.

**Money and quantities are `decimal`, never `float64`.** FIFO repeatedly adds and subtracts
fractional quantities; binary floats accumulate error that eventually surfaces as stock being
a fraction of a unit short. See `pkg/money`.

**Every query is organization-scoped.** The system launches single-tenant, but
`organization_id` is on every table and every read and write goes through
`shared.InOrg(orgID)`. A missing scope is a cross-tenant data leak — the integration suite
asserts isolation explicitly.

**Routes are guarded by permission, not by role.** `auth/rbac.go` holds the single
role→permission matrix; `middleware.RequirePermission` guards each route, and `/auth/me`
returns the caller's permissions so the clients build navigation from the server's answer
instead of re-deriving the matrix and drifting from it.

**Offline sync is idempotent.** The mobile app sends a `client_request_id` with every queued
scan; a unique index makes a retried sync a no-op rather than double-counting stock. Batch
sync applies each movement in its own transaction and reports a per-item outcome, so one bad
entry never discards the rest of the queue.

**`AUTO_MIGRATE` is refused in production.** Migrations run as a deploy step; N replicas
racing to migrate is how a schema gets corrupted.

### Request and response shape

Success:

```json
{ "data": { "id": "…" }, "meta": { "page": 1, "per_page": 25, "total": 84, "total_pages": 4 } }
```

Failure — `code` is stable and safe to switch on:

```json
{ "error": { "code": "INSUFFICIENT_STOCK", "message": "insufficient stock",
             "details": { "available": 3, "requested": 10 },
             "request_id": "0f9c…" } }
```

Every list endpoint accepts `?page=&per_page=&sort=-created_at&q=&filter[status]=active`.
Sortable and filterable columns are allow-listed per endpoint, so query parameters never
reach the SQL string.

Every response carries `X-Request-ID`. Quote it in a bug report and the matching log lines
are one search away.

---

## Testing

```bash
make test              # fast; no external services
make test-integration  # spins up Postgres via testcontainers
make cover             # both, with the coverage gate CI enforces
```

The integration suite in `internal/api` drives the assembled application over HTTP against a
real Postgres, deliberately **without** Redis, so it also exercises the degraded-cache path.
It covers the acceptance-criteria workflow end to end (receive against an approved PO → store
→ sell → report), FIFO COGS arithmetic, the negative-stock guard, offline-sync idempotency,
concurrent adjustments, RBAC per role, and tenant isolation.

CI fails below 70% coverage of `internal/`. Unit tests alone do not reach that — the gate is
measured across unit **and** integration runs merged together, which is why
`make test-integration` needs to keep working.

---

## Deployment

| Trigger | What happens |
|---|---|
| Pull request → `dev` or `main` | `ci.yml`: gofmt, vet, golangci-lint, OpenAPI freshness, unit + integration tests, coverage gate, image build and boot smoke test |
| Merge → `dev` | image published to GHCR, then auto-deploy to **staging** |
| Merge → `main` | image published; `release-please` opens/lands the release PR and tags `vX.Y.Z` |
| Tag `v*` | deploy to **production**, gated by the GitHub Environment's required reviewers |

Deploys apply migrations as their own step, then roll the API and worker deployments and wait
on `/health/ready`. A failed health check rolls back automatically.

Secrets live in GitHub Environments (`staging`, `production`), never in the repo:
`DATABASE_URL`, `JWT_SECRET`, `REDIS_URL`, SMTP credentials, `FCM_SERVER_KEY`, `KUBE_CONFIG`.

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) — identical across all three repos. In short: branch
from `dev` as `feat/<name>`, use Conventional Commits, keep `docs/openapi.yaml` regenerated,
and expect a reviewer to look hardest at concurrency, tenant scoping, authorisation and the
audit trail.
