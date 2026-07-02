# entitlements

The **single source of truth** for "what is this owner allowed to do" — plans
(quotas + feature flags), per-owner entitlements/overrides, and usage accounting.
Every constrained service (staff, tenant/outlets, connectors) asks here before
creating a resource.

Part of the Restorna control plane (M1). Hexagonal, Connect-Go, Postgres + RLS.

## RPCs (`restorna.entitlements.v1.EntitlementsService`)

| RPC | Purpose |
|-----|---------|
| `GetEntitlement` | Owner's entitlement + **effective plan** (plan ∪ overrides). |
| `CheckQuota` | "May I create `delta` more of `key`?" → `allowed, remaining, limit, upgrade_hint`. Read-only. |
| `ReserveQuota` | **Atomic + idempotent** (`reservation_id`) reservation of `delta` of `key`. Over-limit → `ResourceExhausted`. |
| `ReleaseQuota` | Undo a reservation by `reservation_id` (idempotent no-op if unknown). |
| `HasFeature` | Is a feature flag enabled for the owner (overrides win)? |
| `SetEntitlement` | Assign/update an owner's plan + overrides (admin / billing-saas). |
| `UpsertPlan` | Create/replace a plan (admin). |

Quota keys: `outlets`, `staff.manager`, `staff.waiter`, `staff.kitchen`,
`staff.cashier`, `tables`, `brands`, `connectors`. Feature flags: `multi_brand`,
`aggregators`, `analytics_pro`, `crm`. **`-1` = unlimited.**

## Model

- **Plans are data**, not code — seeded by migration `0002_seed_plans.sql`
  (`free` / `growth` / `pro` / `enterprise`) and editable via `UpsertPlan`.
- **Effective plan** = `plan.quotas` merged with the owner's `quota_overrides`
  (override wins key-by-key); features likewise. See `domain.EffectivePlan`.
- **Remaining** = `limit - used` (clamped ≥ 0; unlimited stays `-1`).

## Quota safety (the important bit)

`ReserveQuota` runs entirely in one Postgres transaction (RLS-scoped by
`owner_id`):

1. **Dedupe** by `reservation_id` in the `reservations` ledger — a replay returns
   the prior `remaining` without re-counting.
2. **Lock** the `usage_counters` row (`SELECT … FOR UPDATE`, created at 0 if
   missing) so concurrent reservers serialise.
3. **Cap check** against the effective limit → `domain.ErrQuotaExceeded`
   (mapped to Connect `ResourceExhausted`) if it would breach.
4. **Insert** the ledger row + **increment** the counter.

`ReleaseQuota` deletes the ledger row (the stored `delta` is authoritative) and
decrements the counter, clamped at 0; an unknown id is a harmless no-op.

This makes "add the Nth waiter" race-safe: `staff.AddStaff` calls
`ReserveQuota(reservation_id = staffID)` inside its create; a retry with the same
id never double-charges.

## Events

None emitted. Entitlement/plan changes are admin-driven and read synchronously;
this service has no outbox.

## Layout (hexagonal)

```
cmd/server/main.go                     composition root
internal/domain/                       pure model + rules (merge, remaining, allows)
internal/app/                          use cases over ports
internal/ports/                        repo interfaces
internal/adapters/pg/                  Postgres repos (RLS, FOR UPDATE, ledger)
internal/adapters/grpc/                Connect handler + proto<->domain mapping
migrations/                            goose: 0001 schema+RLS, 0002 seed plans
*_test.go                              table-driven unit tests (domain + app, in-memory fakes)
```

## Config (env, 12-factor)

`PORT`, `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`, `OTEL_EXPORTER_OTLP_ENDPOINT`.

## Run

```bash
make run/entitlements          # from repo root, against docker-compose infra
go test ./...                  # unit tests (no DB/network)
go test -tags=integration ./...# adapter tests (Postgres testcontainer; CI)
```

## Build image

```bash
# build context is the repo root (sibling gen/ + pkg/ are needed by replaces)
docker build -f services/entitlements/Dockerfile -t restorna/entitlements .
```

Multi-stage, distroless static, non-root, reads `PORT`.
