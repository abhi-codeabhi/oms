# floor service

Data-plane (OMS) service: the **dining-room floor**. It owns tables, seating,
waiter assignment, table **move/swap**, and the proactive **nudge** timers. A table
runs MANY orders at once, so a single stored status can never be right — each
table's live status is **DERIVED at read time** from its kitchen tickets + open
bills. The stored floor doc keeps only the seat/order/waiter and the nudge
timestamps.

Ported from the proven Restorna Node floor + orchestration: `createFloor`,
`seat`/`assign`/`moveOrSwap`, `buildFloorView` (the derived view), and the nudge
engine (greet → check-in → anything with its suppression rules).

Hexagonal (`domain` → `app` → `ports` → `adapters`), per `CONVENTIONS.md`.

## RPCs (`restorna.floor.v1.FloorService`)

| RPC | Notes |
|-----|-------|
| `InitFloor` | Create the floor from a list of table numbers (rejects duplicates). Emits `floor.initialized`. |
| `GetFloor` | **Derived** view: each table's status computed from kitchen tickets + open bills (`billing > ready > cooking > seated > free`). The stored doc is never mutated. |
| `SeatParty` | Seat an arriving party at a table (ensures it exists). **Arms the greet timer** (`seated_at` set on first seating). Emits `table.seated`. |
| `AssignWaiter` | Assign one waiter to **one or more** tables in one call (all-or-nothing). Emits `table.assigned`. |
| `Move` | **Move** (dst free) or **swap** (dst busy) a table's seat/order/waiter. Also calls `OrderingService.Relocate` so open orders follow the seat. Returns the `verb`. Emits `table.moved`. |
| `GetNudges` | Build the active waiter prompts (greet / check-in / anything) from each table's timers vs the **effective nudge config** read from `SettingsService.GetEffective` (`floor.nudge.*`). Read-only. |
| `AckNudge` | Acknowledge a nudge: a **greet** ack sets `greeted_at`; a **check-in**/**anything** ack sets `last_checkin_at`. |

### Derived status (port of `buildFloorView`)

`GetFloor` calls `KitchenService.GetBoard` (cooking tickets) + `ServeQueue` (ready,
unserved) and `BillingService.ListOpen`, groups them onto floor tables by table
number (`T7`/`7` → 7), then sets each table's status by priority:

```
billing  — table has an open (unpaid) bill          (settle now)
ready    — ≥1 ticket all-ready, not yet served       (deliver now)
cooking  — ≥1 ticket still being made                (kitchen busy)
seated   — occupied, nothing outstanding             (idle / between rounds)
free     — unoccupied
```

### Nudge engine

Per-table timers (epoch ms) drive one prompt per table, surfaced oldest-first:

- **greet** — seated, not yet greeted, `greet_secs` elapsed since `seated_at`.
- **checkin** — served, no check-in since that serve, `checkin_secs` elapsed since
  `last_served_at`.
- **anything** — checked in, `anything_secs` elapsed since `last_checkin_at`, and
  no newer serve is pending (a serve after the last check-in resets to the
  check-in track).

Timings come from `SettingsService.GetEffective` (`floor.nudge.greet_secs`,
`floor.nudge.checkin_secs`, `floor.nudge.anything_secs`, plus `*_enabled` flags);
missing keys fall back to the Node defaults (30s / 300s / 600s). A settings outage
falls back to defaults rather than blocking the floor.

## Choreography (events consumed — idempotent)

- **`restorna.ordering.order.placed.v1`** `{ order_id, restaurant_id, table_id }`
  — ensure the table exists and **seat** it (set `seated_at` if unset → arm the
  greet timer) and record the order id.
- **`restorna.kitchen.ticket.served.v1`** `{ ticket_id, order_id, table }`
  — record the serve (set `last_served_at` → arm the check-in timer).

Both dedupe on the event id: `pkg/eventbus/nats` dedupes in process, and the app
marks the event id in `processed_events` in the **same transaction** as the floor
write, so a redelivery is a no-op (exactly-once effect).

## Events emitted (outbox → NATS, CloudEvents envelope)

- `restorna.floor.floor.initialized.v1` — `{tables[]}`
- `restorna.floor.table.seated.v1` — `{n}`
- `restorna.floor.table.assigned.v1` — `{tables[], waiter_id}`
- `restorna.floor.table.moved.v1` — `{src, dst, verb}`

Staged in the **same transaction** as the write (`pkg/outbox.Stage`); a relay
drains to NATS.

## Dependencies (calls out)

| Service | Port | Used for |
|---------|------|----------|
| **KitchenService** | `ports.KitchenBoard` | `GetBoard` + `ServeQueue` → derive cooking/ready |
| **BillingService** | `ports.BillingOpen` | `ListOpen` → derive billing |
| **SettingsService** | `ports.SettingsResolver` | `GetEffective` → nudge config (`floor.nudge.*`) |
| **OrderingService** | `ports.OrderRelocator` | `Relocate` → orders follow a seat on move/swap |

All injected; unit tests use in-memory fakes. Endpoints from
`KITCHEN_URL` / `BILLING_URL` / `SETTINGS_URL` / `ORDERING_URL`.

### Move and ticket relocation (known gap)

`Move`/`Swap` relocate the floor seat **and** the open **orders** (via
`OrderingService.Relocate`). **Kitchen tickets are not yet re-tabled**: there is no
`KitchenService.Relocate` RPC. Until that exists, in-flight tickets keep their old
table label, so a moved table's derived cooking/ready status lags until those
tickets are served. **Future work:** add a `kitchen.Relocate(from_table, to_table)`
RPC and call it here alongside the ordering relocation. (A swap relocates orders in
three hops via a scratch label so the two tables' orders don't clobber each other.)

## Multi-tenancy

The floor is **per-outlet**: the tenant key is `restaurant_id`. Tables `floors`,
`processed_events` (+ `outbox`) carry `restaurant_id`/`tenant_id` with **Postgres
RLS** scoped to `current_setting('app.tenant_id')`, set per transaction by
`pkg/pg.WithTenant`. RPC callers' scope comes from the JWT (`pkg/tenancy`); the
event consumers derive it from the trusted event envelope/payload — a request body
never supplies the trusted tenant id.

## Layout

```
cmd/server/main.go                 composition root (grpc + 2 consumers + outbox relay)
internal/
  domain/                          pure Floor aggregate + derived-status + nudge engine (no infra)
  app/                             use cases (depend on ports) + 2 choreography handlers
  ports/                           Repository + Kitchen/Billing/Settings/Ordering client ports
  adapters/
    pg/                            Postgres repo (floor doc as JSONB) + RLS + outbox/processed-event
    grpc/                          Connect handler (proto <-> domain, error mapping)
    clients/                       Kitchen/Billing/Settings/Ordering Connect clients (port impls)
    nats/                          order.placed + ticket.served consumers (idempotent)
migrations/                        goose SQL (floors, processed_events, outbox, RLS)
*_test.go                          table-driven unit tests (domain + app, in-memory fakes)
```

## Build / test / run

```bash
go test ./...                      # unit tests (domain + app), no DB needed

# container (build context = repo root so go.mod replaces resolve)
docker build -f services/floor/Dockerfile -t restorna/floor .
```

> No Go toolchain was available in the authoring environment, so the code was
> written but **not compiled or `go test`-run here** — run `go test ./...` (and
> `go vet`) on a machine with Go 1.22+ to confirm green.

### Env

`PORT`, `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`, `OTEL_EXPORTER_OTLP_ENDPOINT`
(shared base) plus `KITCHEN_URL`, `BILLING_URL`, `SETTINGS_URL`, `ORDERING_URL`
(downstream service endpoints).
