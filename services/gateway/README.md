# gateway service

The browser-facing **edge** of the Restorna control plane. It terminates
Connect/gRPC-Web/JSON over h2c, applies the edge concerns (CORS, JWT auth → tenancy
scope, role gates, rate limiting, request logging, OTel via the downstream client
interceptors), routes to the backend control-plane services through generated
Connect **clients**, and exposes thin **per-surface BFFs** as plain JSON HTTP
endpoints the React consoles call (so the frontend never speaks gRPC).

Hexagonal-ish edge layout, per `CONVENTIONS.md`:

```
cmd/server/main.go            composition root: build clients, wire middleware + routes, serve
internal/
  clients/                    typed wrappers over the generated Connect clients (the only
                              package importing *v1connect); ForwardAuth interceptor
  middleware/                 auth (verify JWT -> scope, require role), CORS, rate limit, logging
  bff/                        per-surface JSON handlers + the route table (router.go)
*_test.go                     table-driven httptest unit tests (middleware + BFF, fake clients)
Dockerfile  README.md  go.mod
```

The gateway holds **no business logic**: every handler maps JSON ↔ a backend Connect
call, enforces the caller's role from the verified token, and forwards the bearer
token + tenancy scope downstream (backends re-verify and apply RLS).

## Route groups

| Group | Auth | Routes → backend RPC |
|-------|------|----------------------|
| `/api/auth/*` | **public** (start/verify/customer-session); scoped-token requires auth | `start-otp`→`Identity.StartOtp`, `verify-otp`→`VerifyOtp`, `refresh`→`Refresh`, `scoped-token`→`IssueScopedToken`, `customer-session`→`CustomerSession` |
| `/api/me` | any authenticated | `GET`→`Identity.Introspect` (the caller's own token) |
| `/api/platform/*` | `platform_admin` | `GET owner`→`Tenant.GetOwner`, `GET entitlement`→`Entitlements.GetEntitlement`, `POST plan`→`Entitlements.UpsertPlan` |
| `/api/owner/*` | `owner` / `brand_admin` | `onboarding/{start,submit-brand,submit-outlet,invite-team,complete,state}`→`Onboarding.*`; `GET brands`→`Tenant.ListBrands`; `GET outlets`→`Tenant.ListRestaurants`; `GET entitlement`→`Entitlements.GetEntitlement`; `GET/POST settings`→`Settings.{GetEffective,SetOverride}` |
| `/api/manager/*` | `manager` / `owner` | `POST staff`→`Staff.AddStaff`, `GET staff`→`ListStaff`, `POST staff/disable`→`SetStaffActive`, `POST staff/change-role`→`ChangeRole`, `POST staff/invite`→`InviteStaff`; `GET/POST settings`→`Settings.{GetEffective,SetOverride}` (scope from token) |

Health: `GET /healthz` (liveness), `GET /readyz` (readiness) — served by `pkg/grpcx`.

> **Contract gap noted:** the platform-admin "list owners / list plans" views have no
> backing RPC in the M1 `tenant`/`entitlements` protos (only per-owner reads +
> `UpsertPlan` exist). The platform BFF therefore covers get-owner, get-entitlement,
> and upsert-plan; add `ListOwners`/`ListPlans` RPCs in a later proto rev to back a
> full index.

## Backend routing

Generated Connect clients, one per service, built over an **h2c** `http.Client` so the
gateway speaks gRPC/Connect to the backends. Base URLs come from env. A
`ForwardAuth` client interceptor copies the caller's verified bearer token onto each
downstream call; public BFF calls simply carry no token.

## Settings & tenancy

Settings handlers build the `TenantRef` scope from the **JWT-derived** tenancy
(`pkg/tenancy`), never the request body. The manager surface is implicitly
restaurant-scoped (the token carries `restaurant_id`); the owner surface can write at
owner/brand scope — the `settings` service enforces `editable_by` / `max_scope`.

## Rate limiting

In-memory **token bucket** (`middleware.NewTokenBucket`) keyed by user id (or client
IP when unauthenticated), behind a `Limiter` interface so a Redis-backed bucket can be
swapped in for multi-replica deploys without touching the handlers.

## Build / test / run

```bash
go test ./...                      # unit tests (middleware + BFF), no network needed

# container (build context = repo root so go.mod replaces resolve)
docker build -f services/gateway/Dockerfile -t restorna/gateway .
```

### Env

`PORT`, `JWT_PUBLIC_KEY`, `APP_ENV` (shared base) plus the downstream URLs
`IDENTITY_URL`, `TENANT_URL`, `ENTITLEMENTS_URL`, `STAFF_URL`, `SETTINGS_URL`,
`ONBOARDING_URL`, and the edge knobs `CORS_ALLOWED_ORIGINS` (default `*`),
`RATE_LIMIT_RPS` (default `20`), `RATE_LIMIT_BURST` (default `40`). `DATABASE_URL` /
`NATS_URL` are unused — the gateway is stateless.
```
