# kitchen service (KDS)

Data-plane (OMS) service: the **Kitchen Display System**. It does not place orders —
it **consumes** `ordering.order.placed` (choreography), resolves each line's display
name + station from `catalog`, and fires a **ticket** onto the cook board. Per-ticket
lifecycle: **cooking → ready → served**. A ready (bumped/all-ready) ticket leaves the
cook board and enters the waiter serve queue; serving marks just that one ticket.

Ported from the proven Restorna Node KDS: item states `new`/`prep`/`ready` (0/1/2),
bump-all, served flag, `ticketPhase` cooking/ready/served, `getBoard` = cooking,
`readyQueue` = ready-unserved, `allDay` = outstanding item counts.

Hexagonal (`domain` → `app` → `ports` → `adapters`), per `CONVENTIONS.md`.

## RPCs (`restorna.kitchen.v1.KitchenService`)

| RPC | Notes |
|-----|-------|
| `ReceiveTicket` | Manually fire a ticket from already-resolved lines (e.g. aggregator order). Normally created from the event. |
| `AdvanceItem` | Cycle one item `new → prep → ready` (capped). When this makes the **whole** ticket ready, emits `ticket.ready` (same signal as a bump). |
| `Bump` | Mark the whole ticket ready. Emits `ticket.ready` once. The ticket then leaves `GetBoard` and appears in `ServeQueue`. |
| `Serve` | Waiter delivered **one** ticket → marks just that ticket served, emits `ticket.served`. Other tickets at the same table are untouched. |
| `GetBoard` | Cook screen: **cooking** tickets only, oldest first. |
| `ServeQueue` | Waiter: **ready, unserved** tickets, oldest first. |
| `AllDay` | The all-day rail: `map<item name → count>` of not-yet-ready items across live tickets. |

## Choreography (events consumed)

- **`restorna.ordering.order.placed.v1`** `{ order_id, restaurant_id, table_id, lines[] }`
  — the `adapters/nats` consumer decodes it, scopes the request to the restaurant,
  resolves each line's `name` + `station` via **`CatalogService.GetItem`** (falling
  back to any name/station already on the line), and calls `ReceiveTicket`. One
  ticket item per unit (qty expanded). **Idempotent**: `pkg/eventbus/nats` dedupes
  on `Event.ID` in process, and the app records the event id in `processed_events`
  in the **same transaction** as the ticket insert, so a redelivery is a no-op.

## Events emitted (outbox → NATS, CloudEvents envelope)

- `restorna.kitchen.ticket.ready.v1` — `{ticket_id, order_id, table}` (on bump, or when the last item is advanced to ready)
- `restorna.kitchen.ticket.served.v1` — `{ticket_id, order_id, table}` (on serve)

Staged in the **same transaction** as the write (`pkg/outbox.Stage`); a relay drains
to NATS.

## Dependencies (calls out)

- **CatalogService** (`restorna.catalog.v1`) via the `ports.MenuResolver` port:
  `GetItem` to resolve a menu item's `name` + `station`. Injected; unit tests use a
  fake. `CATALOG_URL` selects the endpoint.

## Multi-tenancy

The KDS is **per-outlet**: the tenant key is `restaurant_id`. Tables `tickets`,
`processed_events` (+ `outbox`) carry `restaurant_id`/`tenant_id` with **Postgres
RLS** scoped to `current_setting('app.tenant_id')`, set per transaction by
`pkg/pg.WithTenant`. RPC callers' scope comes from the JWT (`pkg/tenancy`); the
event consumer derives it from the trusted event envelope/payload — a request body
never supplies the trusted tenant id.

## Layout

```
cmd/server/main.go                 composition root (grpc + consumer goroutine + outbox relay)
internal/
  domain/                          pure Ticket aggregate + item state machine (no infra)
  app/                             use cases (depend on ports) + OrderPlaced handler
  ports/                           Repository, MenuResolver, ProcessedEvents interfaces
  adapters/
    pg/                            Postgres repo + RLS + outbox/processed-event staging
    grpc/                          Connect handler (proto <-> domain, error mapping)
    catalog/                       CatalogService client (ports.MenuResolver impl)
    nats/                          order.placed consumer (idempotent choreography)
migrations/                        goose SQL (tickets, processed_events, outbox, RLS)
*_test.go                          table-driven unit tests (domain + app, in-memory fakes)
```

## Build / test / run

```bash
go test ./...                      # unit tests (domain + app), no DB needed

# container (build context = repo root so go.mod replaces resolve)
docker build -f services/kitchen/Dockerfile -t restorna/kitchen .
```

### Env

`PORT`, `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`, `OTEL_EXPORTER_OTLP_ENDPOINT`
(shared base) plus `CATALOG_URL` (CatalogService endpoint).
