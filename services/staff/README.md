# staff

Control-plane service that owns the **outlet roster + RBAC role bindings** and
**enforces staff limits** by reserving quota in `entitlements` before a member is
persisted. Hexagonal: `domain` (pure) → `app` (use cases) → `adapters`
(pg / grpc / entitlements client / invites), wired in `cmd/server/main.go`.

## RPCs (`restorna.staff.v1.StaffService`)

| RPC | Behaviour |
|-----|-----------|
| `AddStaff` | Validates + **`ReserveQuota("staff.<role>", +1)`** in entitlements **before** persisting. Over-limit → `ResourceExhausted` carrying the upgrade hint; nothing is written. On a persist failure the reservation is released (no leak). Emits `restorna.staff.member.added.v1`. |
| `ListStaff` | Roster for an outlet, paged (numeric offset page token). RLS-scoped to the owner. |
| `SetStaffActive` | `false` → `ReleaseQuota` for the member's role (after persisting the deactivation); emits `member.deactivated.v1`. `true` → re-`ReserveQuota` (may return `ResourceExhausted`); emits `member.reactivated.v1`. Idempotent. |
| `ChangeRole` | Reserves the **new** role's slot, persists, then releases the **old** role's slot. New-role over-limit rolls back with `ResourceExhausted`. Reservation ids are `"<staff_id>:<role>"` so the swap is idempotent and targets the exact slot. Emits `member.role_changed.v1`. Only touches quota for active members. |
| `InviteStaff` | Creates an invite, sends it via the `InviteSender` port (notifications), and emits `restorna.staff.invited.v1`. No quota change (slot was reserved at add time). A member already linked to an identity user cannot be re-invited (`FailedPrecondition`). |

## Quota keys

`staff.<role_slug>` — `staff.manager`, `staff.waiter`, `staff.kitchen`,
`staff.cashier`, `staff.brand_admin`. Owners/platform-admins/customers are not
roster roles and consume no slot.

## Events emitted (outbox → NATS)

- `restorna.staff.member.added.v1`
- `restorna.staff.member.deactivated.v1`
- `restorna.staff.member.reactivated.v1`
- `restorna.staff.member.role_changed.v1`
- `restorna.staff.invited.v1`

All staged in the **same transaction** as the state change via `pkg/outbox`; a
relay drains them to NATS. The CloudEvents envelope carries `tenant_id`.

## Ports (hexagonal interfaces, `internal/ports`)

- `Repo` — Postgres roster, RLS-scoped, transactional outbox.
- `Entitlements` — `Reserve` / `Release` against the entitlements service (gRPC
  client adapter; fake in tests).
- `InviteSender` — delivers invites (notifications; log adapter by default).

## Data & multi-tenancy

`staff_members` is pooled multi-tenant (T1): `owner_id` is the tenant key, with
Postgres **Row-Level Security** scoped by `current_setting('app.tenant_id')`
(set per transaction by `pkg/pg.WithTenant` from the JWT-derived scope). Goose
migrations in `migrations/`, embedded and run at startup. A per-service `outbox`
table backs the transactional outbox.

## Config (env, 12-factor)

`PORT`, `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`,
`OTEL_EXPORTER_OTLP_ENDPOINT`, `APP_ENV`, plus `ENTITLEMENTS_URL`
(default `http://entitlements:8080`).

## Build / test

```sh
go test ./...                      # domain + app unit tests (in-memory fakes)
go build ./cmd/server              # or: docker build -f Dockerfile ../..  (context = monorepo root)
```

> Note: this repo has no Go toolchain wired in the authoring environment; compile
> and `go test ./...` are expected to run on the developer's machine / CI.
