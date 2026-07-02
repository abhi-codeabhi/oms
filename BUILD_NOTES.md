# M1 Control Plane — Build Notes

Consolidation + compile-readiness pass over the seven M1 control-plane services
(identity, tenant, entitlements, staff, settings, onboarding, gateway) plus
`pkg/` and `gen/go`. The services were built in parallel by separate agents;
this pass aligns cross-service wiring, standardizes shared dependency versions,
verifies `pkg` usage against `pkg/INTERFACES.md`, and implements the two newly
added RPCs.

> No Go toolchain was available during this pass, so all work was static. The
> generated proto/Connect code under `gen/go` is still empty — it is produced by
> `make gen`. Until then, the `*v1connect` and `restorna/<ctx>/v1` imports will
> not resolve; this is expected.

## Bring-up order

Run from the repo root, in order:

```bash
make tools      # install buf, goose, golangci-lint, protoc-gen-go, protoc-gen-connect-go
make gen        # buf lint + buf generate  -> populates gen/go (proto + Connect stubs)
go work sync    # resolve every module against the freshly generated gen/go
make test       # go test ./... across the workspace (domain + app unit tests)
```

Optional/local:

```bash
make up         # local infra: postgres, nats, redis, jaeger (docker compose)
make migrate    # goose migrations per service (needs DATABASE_URL)
make build      # build all service binaries
make run/<svc>  # run a single service, e.g. make run/tenant
```

`go work sync` must run **after** `make gen`: several modules `require
github.com/restorna/platform/gen/go v0.0.0` and only the workspace + the
`replace ... => ../../gen/go` directives make that resolve locally (no proxy
fetch). The root `go.work` already lists all nine modules (`gen/go`, `pkg`, and
the seven services).

## What changed in this pass

### 1. Dependency version alignment (go.mod)
All `go.mod` files were confirmed to (a) use module path
`github.com/restorna/platform/<path>`, (b) carry the correct
`replace github.com/restorna/platform/pkg => ../../pkg` and
`... /gen/go => ../../gen/go` directives, and (c) agree on shared dependency
versions. Two drifts were found and fixed in **`services/identity/go.mod`**:

| Dependency                 | Standardized on | Was (identity) |
|----------------------------|-----------------|----------------|
| `github.com/pressly/goose/v3` | `v3.21.1`    | `v3.20.0`      |
| `golang.org/x/net`         | `v0.27.0`       | `v0.26.0`      |

Versions chosen to match `pkg/go.mod` (the foundation module) and the majority
of services. The remaining shared deps were already uniform everywhere:

- `connectrpc.com/connect v1.16.2`
- `github.com/jackc/pgx/v5 v5.6.0`
- `github.com/nats-io/nats.go v1.36.0`
- `github.com/golang-jwt/jwt/v5 v5.2.1`
- `github.com/rs/zerolog v1.33.0`
- `github.com/oklog/ulid/v2 v2.1.0`
- `go.opentelemetry.io/otel v1.28.0`
- `google.golang.org/protobuf v1.34.2`

### 2. `pkg` usage audit vs `pkg/INTERFACES.md`
Every service's calls into `pkg/{config,tenancy,errors,pg,grpcx,outbox,events,auth,ids,money}`
and into the generated `*v1connect` packages were checked against the real `pkg`
signatures and the proto/Connect codegen layout. **No mismatches were found** —
all call sites (arg counts, names, return handling) are correct, and every
`gen/go` import path corresponds to an existing proto package with the right
`<ctx>v1connect` subpackage and `New<Thing>Service{Handler,Client}` constructor.

One documentation note (not a defect): `pkg/grpcx.NewServer` takes
`config.Base`. Services with a custom `Config` that embeds `config.Base`
correctly pass `cfg.Base`; services typed directly as `config.Base` pass `cfg`.
Both forms are present and both are correct.

### 3. New RPCs implemented

**`TenantService.ListOwners`** (`services/tenant`) — platform-admin, cross-tenant,
paginated owner index with optional case-insensitive name filter.
- `internal/app/app.go`: `App.ListOwners` — gates on
  `ROLE_PLATFORM_ADMIN` via `tenancy.From(ctx)` + `Scope.Require(...)`; returns
  `tenancy.ErrPermissionDenied` when the scope is missing or the role is wrong.
  Clamps the page limit, then delegates to the repo.
- `internal/ports/ports.go`: added `ListOwners` to `Repository`.
- `internal/adapters/pg/repo.go`: `Repo.ListOwners` — runs under the **empty
  tenant** (`pg.WithTenant(..., "", ...)`) so the admin connection bypasses RLS
  and sees every owner; `count(*)` + paginated `SELECT ... ORDER BY created_at
  DESC, id DESC LIMIT/OFFSET`; `name ILIKE` filter when a query is supplied.
- `internal/adapters/grpc/handler.go`: `Handler.ListOwners` maps
  `PageRequest`/query in and `Owner`s + `PageResponse` out; `toConnect` now maps
  `tenancy.ErrPermissionDenied -> CodePermissionDenied`.
- Tests: `TestListOwners_RequiresPlatformAdmin` (no scope / owner role denied /
  platform admin allowed) and `TestListOwners_PaginatesAndFilters`; `fakeRepo`
  gained a `ListOwners` implementation.

**`EntitlementsService.ListPlans`** (`services/entitlements`) — platform-admin
catalog listing (no pagination; the catalog is small + global).
- `internal/app/app.go`: `Service.ListPlans` delegates to the plan repo (plans
  are global control-plane data, no owner scoping needed).
- `internal/ports/ports.go`: added `ListPlans` to `PlanRepo`.
- `internal/adapters/pg/pg.go`: `Repo.ListPlans` — empty-tenant query
  `SELECT ... FROM plans ORDER BY id`, decoding the `quotas`/`features` JSONB.
- `internal/adapters/grpc/grpc.go`: `Handler.ListPlans` maps the (empty) request
  and returns `repeated Plan`.
- Tests: `TestListPlansReturnsCatalog`; `fakePlans` gained a `ListPlans`
  implementation.

> Auth note: the proto comments mark both RPCs "platform admin". `ListOwners`
> enforces this in the app layer via `tenancy.Require(ROLE_PLATFORM_ADMIN)`.
> `ListPlans` is enforced at the edge/gateway only (it returns the shared,
> non-tenant plan catalog and leaks no tenant data); see follow-ups.

## Cross-service mismatches found

None blocking. The two go.mod version drifts above were the only cross-service
inconsistencies; both are fixed. `pkg` API usage and `gen/go` import paths are
consistent across all seven services.

## Known follow-ups

1. **Codegen is a hard prerequisite.** `gen/go` is empty; nothing compiles until
   `make gen` runs. The new handlers reference `tenantv1.ListOwnersRequest/Response`
   and `entitlementsv1.ListPlansRequest/Response`, which the protos already
   define — they will exist after generation.
2. **`go.sum` files are absent** for every module. They populate on the first
   `go mod download` / `go work sync` against a live module cache; CI must run
   `make gen` first so `gen/go` is resolvable.
3. **`ListOwners` RLS bypass relies on the admin DB role.** The repo issues the
   cross-tenant query under the empty `app.tenant_id`, matching the convention
   used for the global `plans`/entitlements seeding (see migration comments:
   "platform admins connect with the table owner / BYPASSRLS role"). Confirm the
   tenant service's runtime DB role is `BYPASSRLS` (or that the `owners` RLS
   policy permits an empty tenant for admin reads) before relying on this in a
   non-superuser deployment. An alternative hardening is a dedicated
   `owners_admin` SELECT policy.
4. **`ListPlans` role gating.** Enforcement currently lives at the gateway. If
   the entitlements service is ever reachable directly by tenants, add a
   `tenancy.Require(ROLE_PLATFORM_ADMIN)` check in the handler/app to match
   `ListOwners`.
5. **Adapter (integration) tests** for the two new repo methods are not added —
   only domain/app unit tests, per CONVENTIONS.md (`//go:build integration`
   tests run in CI, not locally). Add `ListOwners`/`ListPlans` cases to the
   entitlements pg integration suite when bringing up a Postgres testcontainer.
6. **Pagination tokens** for `ListOwners` use a numeric offset encoded as the
   page token (same scheme as `ListBrands`/`ListRestaurants`). If a cursor-based
   scheme is later standardized, update all three together.

---

# M2 — OMS Data Plane — Build Notes

Deploy-finalization + cross-service consistency pass over the **seven M2 Order
Management System services** (catalog, ordering, kitchen, floor, billing,
promotions, servicerequests), bringing the full platform to **14 services**.
`go.work`, `Makefile` `SERVICES`, and the CI matrix already listed all 14; this
pass extends the deploy kit and re-checks shared dependency versions. As before,
**no Go toolchain was available** — all work is static (configs + SQL + docs).

## The 14 services

| Milestone | Services |
|---|---|
| M1 control plane | identity, tenant, entitlements, staff, settings, onboarding, gateway |
| M2 OMS data plane | catalog, ordering, kitchen, floor, billing, promotions, servicerequests |

## Event bus (NATS) — a hard dependency for the OMS

The OMS is **event-choreographed**: ordering/kitchen/floor/billing publish and
consume domain events over **NATS/JetStream** (via `pkg/events` + the
transactional outbox), driving the order→kitchen→floor→billing lifecycle. So
`NATS_URL` is **required** for every M2 service (and is set on all of them in
render/helm/k8s as `nats://nats:4222` / cluster NATS). The synchronous
inter-service `*_URL` calls sit on top of the event flow:

| Service | DB | NATS | Synchronous inter-service URLs |
|---|---|---|---|
| catalog | ✅ | ✅ | — |
| ordering | ✅ | ✅ | — |
| promotions | ✅ | ✅ | — |
| kitchen | ✅ | ✅ | `CATALOG_URL` |
| billing | ✅ | ✅ | `ORDERING_URL`, `CATALOG_URL`, `SETTINGS_URL`, `PROMOTIONS_URL` |
| floor | ✅ | ✅ | `KITCHEN_URL`, `BILLING_URL`, `SETTINGS_URL`, `ORDERING_URL` |
| servicerequests | ✅ | ✅ | `SETTINGS_URL` |

(Each set was read from the service's `cmd/server/main.go` `Config` struct env tags.)

## What changed in this pass

### 1. Deploy kit extended to all 14 services
- **`render.yaml`** — added a Docker `web` block per M2 service (same shape as
  M1: `dockerfilePath`, `dockerContext: .`, `/healthz`, `DATABASE_URL` from the
  shared `restorna-postgres`, `NATS_URL=nats://nats:4222`, `JWT_PUBLIC_KEY`
  `sync:false`). Inter-service URLs wired via `fromService … property: hostport`
  per the table above.
- **Helm** — created `deploy/helm/<svc>/` for all seven M2 services by cloning the
  M1 chart structure (the templates use chart-agnostic `svc.*` helpers, so they
  are reused verbatim). `serviceURLs` in each `values.yaml` carries the OMS URLs
  as cluster DNS (`http://<svc>:8080`). Added all seven to the umbrella chart's
  `dependencies` and `values.yaml` (enabled).
- **Kustomize** — added `deploy/k8s/base/<svc>.yaml`
  (Deployment + Service + HPA) and `<svc>-config.yaml` (ConfigMap with the OMS
  URLs) for all seven, included in `base/kustomization.yaml`. Also promoted
  `settings` into the base (M2 billing/floor/servicerequests read it over cluster
  DNS). Overlays (dev/staging/prod) now pin a per-environment `newTag` for
  settings + all seven M2 images.

### 2. Settings seed — `floor.call.escalate_secs`
Added to `services/settings/migrations/0002_seed_definitions.sql`: an `INT`
definition, default `30`, `max_scope=3` (RESTAURANT), `editable_by='manager'`.
The **servicerequests** service reads it to decide when an unattended customer
call escalates to a manager. Down-migration `DELETE` list updated to match.

### 3. Dependency version consistency — no drift found
Re-scanned all 14 `go.mod` files against the M1 baseline (connect v1.16.2,
pgx/v5 v5.6.0, nats.go v1.36.0, jwt/v5 v5.2.1, zerolog v1.33.0, ulid/v2 v2.1.0,
goose/v3 v3.21.1, otel v1.28.0, protobuf v1.34.2, x/net v0.27.0). **All seven M2
`go.mod`s already match the baseline** — they declare the same minimal direct
set the M1 leaf services do (`connect`, `pgx/v5`, and `rs/zerolog` where used in
`main.go`), with the rest pulled transitively via `pkg`. No version drift to fix.
`go.sum`s are still absent platform-wide (populate on first `go work sync` after
`make gen`, as documented for M1).

### 4. Docs
`DEPLOY.md` (artifact table, env-var matrix, OMS wiring table, Render/Helm/
Kustomize sections) and this file updated to reflect all 14 services and the NATS
event-bus dependency.

## Result

render.yaml, `deploy/helm/*` (+ umbrella), and `deploy/k8s/base` (+ overlays) now
cover **all 14 services**. All 65 deploy YAML files parse; all seven M2
Dockerfiles referenced by the configs exist.

---

# M3 — Integration Plane — Build Notes

Deploy-finalization + wiring pass over the **four M3 integration-plane services**
(connectorhub, payments, notifications, aggregators) and the **React web console**
(`web/console/`), bringing the platform to **18 services**. As before, **no Go /
npm / docker toolchain was available** — all work is static (configs + docs).

## The 18 services

| Milestone | Services |
|---|---|
| M1 control plane | identity, tenant, entitlements, staff, settings, onboarding, gateway |
| M2 OMS data plane | catalog, ordering, kitchen, floor, billing, promotions, servicerequests |
| M3 integration plane | connectorhub, payments, notifications, aggregators |

## The connector framework (what M3 plugs into)

- **`pkg/connector`** — the connector **SDK contract**: the `Connector` /
  `PaymentConnector` / `AggregatorConnector` / `NotificationConnector` interfaces,
  the `Manifest` (id + declared capabilities + config schema), and the
  `VerifyWebhook(ctx, body, sig)` contract. The app/domain layers of the M3
  services code only against this SDK (never the concrete adapters).
- **`pkg/connectors`** — the concrete provider **adapters + registry**:
  `razorpay`/`paytm`/`phonepe`/`mockpay` (payments), `zomato`/`swiggy`/`mockagg`
  (aggregators), `twilio`/`msg91`/`lognotify` (notifications). Exposes
  `All() []Manifest` + typed constructors (`NewPayment`/`NewAggregator`/
  `NewNotification`) used by connector-hub's registry port and by each M3 service.

**connector-hub** is the registry + per-tenant config + capability routing +
inbound-webhook ingestion; payments / notifications / aggregators call its
`Resolve` to pick the active provider + get its decrypted config, then instantiate
the `pkg/connectors` adapter.

### CONNECTOR_KEK — secret requirement (connector-hub only)

connector-hub encrypts **secret connector config at rest** (AES-256-GCM,
envelope-ready) under a 32-byte **Key-Encryption-Key** decoded from
`CONNECTOR_KEK` (base64/hex/raw). It has **no default and the service refuses to
start without it** (`cryptoadapter.FromKEK` fails). So `CONNECTOR_KEK` is wired as
a *secret* everywhere, never a ConfigMap value:
- **Render:** `generateValue: true` (Render generates + stores a strong value).
- **Helm/Kustomize:** a key in the `connectorhub-secrets` Secret (created
  out-of-band; e.g. `openssl rand -base64 32`).

### Public webhook edge

Provider webhooks (payment captures, aggregator orders, delivery reports) reach
connector-hub's **`IngestWebhook`**, which verifies the provider signature via the
connector's `VerifyWebhook`, normalizes to a CloudEvent, and publishes it to NATS
(payments/notifications/aggregators consume it). This is the **only public path**
into the integration plane and is meant to be exposed **through the gateway**; the
M3 services themselves stay private.

## Inter-service wiring (read from each `cmd/server/main.go` `Config`)

| Service | Reads | Secret |
|---|---|---|
| connectorhub | `ENTITLEMENTS_URL` | **`CONNECTOR_KEK`** |
| payments | `CONNECTORHUB_URL` | — |
| notifications | `CONNECTORHUB_URL` | — |
| aggregators | `CONNECTORHUB_URL`, `CATALOG_URL`, `ORDERING_URL` | — |

(All four also take `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`; all run
migrations on startup, an outbox relay, and NATS consumers for choreography.)
Note: the M3 `main.go` defaults use `http://connector-hub:8080` /
`http://connectorhub:8080`; the deploy configs set the URLs explicitly to the
`connectorhub` Service DNS (Render `hostport`, cluster `http://connectorhub:8080`),
so the default is never relied on.

## What changed in this pass

### 1. Deploy kit extended to all 18 services + the web console
- **`render.yaml`** — added a Docker `web` block per M3 service (same shape:
  `dockerfilePath`, `dockerContext: .`, `/healthz`, `DATABASE_URL` from
  `restorna-postgres`, `NATS_URL`, `JWT_PUBLIC_KEY` `sync:false`). Inter-service
  URLs via `fromService … property: hostport`; connectorhub's `CONNECTOR_KEK` via
  `generateValue: true`. Added the **web console** as a `runtime: static` site
  (`rootDir: web/console`, `npm install && npm run build`, publish `dist`, SPA
  rewrite, `VITE_GATEWAY_URL` `sync:false`).
- **Helm** — created `deploy/helm/{connectorhub,payments,notifications,aggregators}/`
  by cloning the M2 chart structure (chart-agnostic `svc.*` templates reused
  verbatim). `serviceURLs` carries the cluster-DNS URLs; connectorhub's values doc
  the `CONNECTOR_KEK` requirement in `connectorhub-secrets`. Added all four to the
  umbrella `dependencies` + `values.yaml` (enabled).
- **Kustomize** — added `deploy/k8s/base/<svc>.yaml` (Deployment/Service/HPA) and
  `<svc>-config.yaml` (ConfigMap) for the four services, included in
  `base/kustomization.yaml`; `connectorhub-config.yaml` intentionally omits
  `CONNECTOR_KEK` (it lives in `connectorhub-secrets`). Overlays (dev/staging/prod)
  now pin a per-environment `newTag` for all four M3 images.
- **CI + Makefile** — added connectorhub, payments, notifications, aggregators to
  the CI docker-build matrix (`.github/workflows/ci.yml`) and to `Makefile`
  `SERVICES`.

### 2. go.work — already complete
`go.work` already lists **all 18 services + `pkg` + `gen/go`** (20 `use` entries);
no fix needed. `go work sync` still runs after `make gen` (gen/go is generated).

### 3. Docs
`DEPLOY.md` (service count 18, artifact table + M3 rows, env-var matrix with
`CONNECTORHUB_URL`/`CONNECTOR_KEK`, an M3 wiring table, the public-webhook note,
Render/Helm/Kustomize sections, and the connector-hub secret-creation example) and
this file updated.

## Known parallel-agent artifacts (harmless — safe to delete)

`pkg/connectors` contains two **intentionally empty package stubs** left by an
earlier parallel-agent draft:

- **`pkg/connectors/mock.go`** — an earlier `mockProvider` payment connector,
  superseded by `mockpay.go` (`MockPay`, id `mockpay`).
- **`pkg/connectors/providers.go`** — an earlier shared `hmacProvider` payment
  base, superseded by the real per-provider adapters `razorpay.go`, `paytm.go`,
  `phonepe.go`.

Both files are `package connectors` with only a doc comment (no symbols), so they
**compile cleanly and cause no duplicate-symbol conflicts** — they are valid and
harmless. The team can delete them at any time with no code impact.

## Result

render.yaml, `deploy/helm/*` (+ umbrella), `deploy/k8s/base` (+ overlays), the CI
matrix, and `Makefile SERVICES` now cover **all 18 services**; render.yaml and the
standalone `web/console/render.yaml` cover the **web console** static site.
`go.work` lists all 18 services + `pkg` + `gen/go`.
