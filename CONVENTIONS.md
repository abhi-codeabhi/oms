# Restorna Platform — Engineering Conventions

Every service follows these rules so any agent can build any service identically and
the pieces fit together. Read this before writing code.

## Language & tooling
- **Go 1.22+**, modules per service, tied by root `go.work`.
- **Connect-Go** (`connectrpc.com/connect`) for RPC servers/clients (gRPC + gRPC-Web
  + JSON from one handler). Contracts in **Protobuf**, codegen via **Buf**.
- **Postgres** via `pgx/v5` (+ `pgxpool`). Migrations via `goose` in
  `services/<name>/migrations`, embedded with `embed.FS`.
- **NATS JetStream** (`nats.go`) for events. **zerolog** for logs. **OTel** for traces.
- Lint: `golangci-lint`. Format: `gofmt`/`goimports`. No lint errors in CI.

## Service layout (hexagonal — identical everywhere)
```
services/<name>/
  cmd/server/main.go            # composition root: wire adapters, start server
  internal/
    domain/                     # pure types + rules, NO imports of pgx/nats/connect
    app/                        # use cases; depend on PORTS (interfaces), not adapters
    ports/                      # interfaces: repos, publishers, clients
    adapters/
      pg/                       # Postgres repo impls (+ RLS)
      grpc/                     # Connect handlers (map proto <-> domain)
      nats/                     # event publisher + consumers
      clients/                  # generated clients to OTHER services
  migrations/                   # goose SQL
  *_test.go                     # table-driven unit tests on domain + app
  go.mod
```
Dependency rule: `adapters → app → domain`. Domain imports nothing infra. App
imports only `ports` + `domain`. Wiring happens only in `cmd/server/main.go`.

## Contracts (proto)
- One Buf module under `proto/restorna/<context>/v1/`. Package
  `restorna.<context>.v1`. Services named `<Thing>Service`. Versioned `v1`.
- Reuse `proto/restorna/common/v1` for `Money`, `TenantRef`, `PageRequest`, etc.
- Never break a published message; add fields (new tags) or a new `v2`.
- Generated Go lands in `gen/go/...`; import it, never hand-edit.

## Errors
- Return Connect errors with codes: `InvalidArgument`, `NotFound`,
  `AlreadyExists`, `PermissionDenied`, `ResourceExhausted` (quota), `FailedPrecondition`,
  `Internal`. Wrap domain errors → connect codes in the grpc adapter only.
- Domain returns typed errors (`var ErrX = errors.New(...)`); never `panic` for
  expected conditions.

## Money & ids
- Money = `int64` minor units + currency string. Use `pkg/money`. Never float.
- IDs = ULID via `pkg/ids`, type-prefixed: `usr_`, `own_`, `brnd_`, `out_`, `stf_`,
  `ord_`, `pay_`. Generated in the domain, not the DB.

## Multi-tenancy (mandatory)
- The tenancy context (`owner_id`, `brand_id`, `restaurant_id`, `role`) comes from
  the JWT, parsed by `pkg/auth` interceptor into `ctx`. Read it via
  `tenancy.From(ctx)`. Never trust a tenant id from the request body.
- Postgres repos open a tx and `SELECT set_config('app.tenant_id', $1, true)` before
  queries so **RLS** scopes rows. Every tenant table has `tenant_id` + an RLS policy.
- A service NEVER queries another service's database. Use its gRPC client or events.

## Events (outbox + idempotent)
- State change + outbox row in ONE transaction (`pkg/outbox`). A relay publishes to
  NATS. Envelope = CloudEvents (`pkg/events`): `id, type, tenant, occurredAt, data`.
- Event type = `restorna.<context>.<aggregate>.<event>.v1`.
- Consumers dedupe on event `id` (processed-events table) → exactly-once effect.

## Testing
- **Domain + app**: table-driven unit tests, no DB/network (use in-memory fakes for
  ports). This is the bulk of coverage and must pass via `go test ./...`.
- **Adapters**: integration tests behind a build tag `//go:build integration` using a
  Postgres/NATS testcontainer (run in CI, not required locally).
- Every service ships with passing unit tests. A use case isn't done without tests.

## Config & runtime
- Config from env (12-factor) via `pkg/config`; no secrets in code. Required:
  `PORT`, `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`, `OTEL_EXPORTER_OTLP_ENDPOINT`.
- `main.go`: load config → connect deps → run migrations → register Connect handlers
  → start HTTP/2 server → graceful shutdown on SIGTERM. Expose `/healthz` `/readyz`.

## Definition of done (per service)
proto committed · domain+app+adapters+wiring · migrations · unit tests green
(`go test ./...`) · `/healthz` · Dockerfile · Helm values · README with the RPCs and
events it exposes/consumes.
