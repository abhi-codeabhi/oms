# ordering service

Data-plane (OMS) service that records **multi-round dine-in orders** per table. No
payment lives here — the bill is settled later by `billing`. On `PlaceOrder` it
emits `order.placed`, which **kitchen** (ticket) and **floor** (seat) consume via
events (choreography). Ported from the proven Node ordering service: order/line
model, subtotal, tolerant table matching, `listForTable`, `markBilled`, `relocate`.

Hexagonal (`domain` → `app` → `ports` → `adapters`), per `CONVENTIONS.md`.

## RPCs (`restorna.ordering.v1.OrderingService`)

| RPC | Notes |
|-----|-------|
| `PlaceOrder` | Build an order from line items, compute the **subtotal** (Σ `unit_price` × `qty`), persist, and stage `order.placed` in the same tx. |
| `GetOrder` | Fetch one order (+ lines), RLS-scoped to the caller's restaurant. |
| `ListForTable` | All orders for a table, matched **tolerantly** (`T7` / `7` / `Table 7` all collide on the digit run). **Unbilled by default**; `include_billed` returns the whole table history. |
| `MarkBilled` | Flag orders as billed so a finalized bill never includes them twice. Missing/foreign/already-billed ids are skipped; returns the count flipped. |
| `Relocate` | Move every **open (unbilled)** order from one table label to another (waiter move/swap). Tolerant matching; returns the count moved. |

The restaurant id is the tenant key and **always comes from the JWT-derived tenancy
scope** (`pkg/tenancy`), never the request body.

## Events emitted (outbox → NATS, CloudEvents envelope)

- `restorna.ordering.order.placed.v1` —
  `{order_id, restaurant_id, table_id, subtotal{minor,currency}, lines[{line_id, menu_item_id, name, qty, station, unit_price{minor,currency}}], created_at}`

Staged in the **same transaction** as the order write (`pkg/outbox.Stage`); a relay
drains to NATS. Kitchen routes a ticket; floor marks the table occupied.

## Money

Money is integer **minor units** + currency (`pkg/money`); never floats. Subtotal
and each line carry `{minor, currency}` (default `INR`).

## Multi-tenancy

Tables `orders`, `order_lines` (+ `outbox`) carry `restaurant_id`/`tenant_id` with
**Postgres RLS** scoped to `current_setting('app.tenant_id')`, set per transaction
by `pkg/pg.WithTenant` from the JWT-derived restaurant id.

## Layout

```
cmd/server/main.go                 composition root
internal/
  domain/                          Order/Line, subtotal, tolerant TableKey (no infra)
  app/                             PlaceOrder/GetOrder/ListForTable/MarkBilled/Relocate
  ports/                           Repository + Tx interfaces
  adapters/
    pg/                            Postgres repo + RLS + outbox staging (orders + lines)
    grpc/                          Connect handler (proto <-> domain, error mapping)
migrations/                        goose SQL (orders, order_lines, outbox + RLS)
*_test.go                          table-driven unit tests (domain + app, in-memory fakes)
```

## Build / test / run

```bash
go test ./...                      # unit tests (domain + app), no DB needed

# container (build context = repo root so go.mod replaces resolve)
docker build -f services/ordering/Dockerfile -t restorna/ordering .
```

### Env

`PORT`, `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`, `OTEL_EXPORTER_OTLP_ENDPOINT`
(shared `config.Base`).

> NOTE: this service was authored without a Go toolchain in the environment, so
> `go build` / `go test` were **not** run here — compile + test on a machine with
> Go 1.22 and the generated proto (`gen/go/restorna/ordering/v1` + `orderingv1connect`)
> present.
