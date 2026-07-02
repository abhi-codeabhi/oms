# onboarding

Control-plane **saga orchestrator** for the client signup -> go-live workflow. It
drives the other control-plane services (identity, tenant, entitlements, staff,
settings) through a **resumable, idempotent** saga and emits a single completion
event so the data plane can seed the menu and table QR codes.

Hexagonal: `domain` (the OnboardingState ledger + step machine) -> `app` (the
saga use cases) -> `adapters` (pg / grpc / the five service clients), wired in
`cmd/server/main.go`.

## The saga (steps)

```
ACCOUNT -> PLAN -> BRAND -> OUTLET -> SETTINGS -> TEAM -> GOLIVE
```

Every step writes the `OnboardingState` ledger (completed steps + the ids each
step produced: owner / user / brand / outlet). A retried RPC consults the ledger
and **skips work already done**, reusing the stored ids — that is what makes each
step idempotent and the whole flow resumable after a crash or an at-least-once
client retry.

## RPCs (`restorna.onboarding.v1.OnboardingService`)

| RPC | Behaviour |
|-----|-----------|
| `StartOnboarding` | **Platform-admin only.** Registers the owner login (`identity`), creates the owner record (`tenant.CreateOwner`), assigns the plan (`entitlements.SetEntitlement` with `plan_id`, default `free`), persists the ledger with `ACCOUNT` + `PLAN` done. |
| `SubmitBrand` | `tenant.CreateBrand` + `tenant.SetBrandLogo` (uploads the logo bytes). Persists the brand id before the logo upload so a crash cannot duplicate the brand. Advances `BRAND`. Idempotent: a completed step returns the stored brand id. |
| `SubmitOutlet` | `tenant.CreateRestaurant`, then seeds default outlet settings via `settings.SetOverride` (`billing.currency=INR`, `billing.gst_pct`, `billing.service_charge_pct`, `billing.rounding`, `ordering.modes`). GST defaults to 5 when a GSTIN is supplied. Advances `OUTLET` + `SETTINGS`. |
| `InviteTeam` | Per invite: `staff.AddStaff` + `staff.InviteStaff`. A `ResourceExhausted` from staff (plan `staff.<role>` limit) is **reported per-invite** (which one failed) rather than aborting the step; the step still completes so the saga can go live, and the owner retries the failed invites after upgrading. Advances `TEAM`. |
| `Complete` | Marks `GOLIVE` and emits `restorna.onboarding.completed.v1` (transactional outbox) carrying owner / brand / restaurant ids. Table QR generation + menu seed are a later data-plane concern driven by that event. Idempotent: an already-done saga does not re-emit. |
| `GetState` | Returns the current ledger. |

## Downstream ports (the 5 service clients + repo)

The five service clients are **ports** (`internal/ports`) so the saga is unit-
tested against in-memory fakes that record calls:

- `Identity` — `EnsureOwnerUser` (identity service).
- `Tenant` — `CreateOwner` / `CreateBrand` / `SetBrandLogo` / `CreateRestaurant`.
- `Entitlements` — `AssignPlan` (SetEntitlement).
- `Staff` — `AddStaff` / `InviteStaff`; maps `ResourceExhausted` -> `ports.ErrQuotaExhausted`.
- `Settings` — `SetOverride`.
- `Repo` — Postgres saga ledger, RLS-scoped, transactional outbox.

Real Connect clients are wired in `cmd/server/main.go` over h2c from env URLs.

## Events emitted (outbox -> NATS)

- `restorna.onboarding.completed.v1` — `{onboarding_id, owner_id, user_id,
  brand_id, restaurant_id, plan_id}`. Downstream (catalog/floor) seeds the menu
  and generates table QR codes from it.

Staged in the **same transaction** as the GOLIVE state change via `pkg/outbox`;
a relay drains it to NATS.

## Data & multi-tenancy

`onboarding_states` is pooled multi-tenant (T1): `owner_id` is the tenant key,
with Postgres **Row-Level Security** scoped by `current_setting('app.tenant_id')`
(set per transaction by `pkg/pg.WithTenant`). Because a saga is looked up by its
`onb_` id before the owner is necessarily in the caller's scope, the RLS policy
scopes by `owner_id` when `app.tenant_id` is set and permits the trusted
control-plane path when it is unset; the app layer then asserts ownership for
owner callers. Goose migrations in `migrations/`, embedded and run at startup. A
per-service `outbox` table backs the transactional outbox.

## Config (env, 12-factor)

`PORT`, `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`,
`OTEL_EXPORTER_OTLP_ENDPOINT`, `APP_ENV`, plus the downstream service URLs:
`IDENTITY_URL`, `TENANT_URL`, `ENTITLEMENTS_URL`, `STAFF_URL`, `SETTINGS_URL`
(each default `http://<svc>:8080`).

## Build / test

```sh
go test ./...                      # domain + app unit tests (in-memory fakes)
go build ./cmd/server              # or: docker build -f Dockerfile ../..  (context = monorepo root)
```

> Note: this repo has no Go toolchain wired in the authoring environment; the
> onboarding/v1 generated code (`gen/go/.../onboardingv1connect`) and the other
> services' generated clients are assumed present from `buf generate`. Compile
> and `go test ./...` are expected to run on the developer's machine / CI.
