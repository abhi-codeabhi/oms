# billing service (operational table billing)

Data-plane (OMS) service: **operational billing** — settling a dine-in **table**.
A guest orders several rounds across a meal; no money changes hands until the end.
When the waiter or billing agent opens the bill for a table, billing aggregates
**every unbilled order** (via `ordering`) into **ONE categorized final bill**,
resolves each line's dish name + course **category** (via `catalog`), reads the
tax config (GST / service charge / rounding / currency) from `settings`, computes
totals, persists the bill, marks the orders billed, and captures payment. This is
the diner-facing bill, distinct from `billing-saas` (what owners pay Restorna).

Ported from the proven Restorna Node billing + orchestration: `openBill` /
`computeTotals` (GST + service charge on the post-discount subtotal) /
`applyDiscount` / `recordPayment` (paid once payments cover the total), the
`openTableBill` aggregation + course **section** grouping, and the `openTabs`
billing board — here re-expressed as an **event-driven** read model.

Hexagonal (`domain` → `app` → `ports` → `adapters`), per `CONVENTIONS.md`.

## RPCs (`restorna.billing.v1.BillingService`)

| RPC | Notes |
|-----|-------|
| `OpenTabs` | The billing board: every occupied table with its running total / order+item counts / status (`bill_ready` > `asked` > `open`). Served from the **event-driven** `tabs` read model. |
| `OpenForTable` | Aggregate the table's **unbilled** orders into one bill. `ordering.ListForTable` → resolve each line's name+category via `catalog.GetItem` → expand qty into per-unit bill lines → read `billing.*` tax config from `settings.GetEffective` → compute subtotal/tax/total → persist → `ordering.MarkBilled` → emit `bill.opened`. Returns `Bill` + `Sections` (grouped by category in menu order). |
| `GetBill` | One bill with computed totals + sections. |
| `ListOpen` | Unpaid bills (the billing queue) with totals. |
| `ApplyDiscount` | Lower the total by a **coupon** (`promotions.Evaluate`, passed as `reason="coupon:CODE"` or the amount field) **or** a flat amount; recompute totals; emit `discount_applied`. |
| `TakePayment` | Record a `Payment`. Emits `payment.captured`; when payments cover the total the bill is finalized (`paid`) and `bill.finalized` is emitted. |

## Tax math (domain, ported from `bill.js`)

- Money is **integer minor units** (paise). Subtotal = Σ line prices.
- Discount is applied first; **GST** and **service charge** are both computed on the
  **post-discount** subtotal (the taxable base). Total = taxable + service charge + tax.
- Discount is clamped so the taxable base never goes negative.
- `billing.rounding` rounds the grand total to a whole currency unit:
  `nearest_1` / `up_1` / `down_1` / `none` (default).
- Sections group the bill's lines by course in the conventional running order:
  **Appetizers → Mains → Breads → Sides → Drinks → Desserts → Other** (unknown last).

## The billing board (`tabs`) — event-driven read model

Maintained by the `adapters/nats` consumers, **not** derived from a live query:

| Event consumed | Effect on the tab |
|----------------|-------------------|
| `restorna.ordering.order.placed.v1` | add running total + order/item counts (creates the tab on the first order). |
| `restorna.servicerequests.raised.v1` (`type=bill`) | mark the tab **asked**. |
| `restorna.billing.bill.opened.v1` | attach the bill id + total, flip to **bill_ready**. |
| `restorna.billing.bill.finalized.v1` | **remove** the tab from the board. |

Status precedence: `bill_ready` (open bill) **>** `asked` **>** `open`. Consumers
are **idempotent** — `pkg/eventbus/nats` dedupes on `Event.ID` in process and the
app records the event id in `processed_events` in the **same transaction** as the
projection write, so a redelivery is a no-op.

## Events emitted (outbox → NATS, CloudEvents envelope)

- `restorna.billing.bill.opened.v1` — `{bill_id, table, order_ids, total_minor, currency}`
- `restorna.billing.bill.discount_applied.v1` — `{bill_id, table, minor, reason}`
- `restorna.billing.payment.captured.v1` — `{bill_id, payment_id, method, amount_minor, currency}`
- `restorna.billing.bill.finalized.v1` — `{bill_id, table, order_ids, total_minor, currency}`

Staged in the **same transaction** as the write (`pkg/outbox.Stage`); a relay
drains to NATS. The board consumer also subscribes to this service's own
`bill.opened` / `bill.finalized` to keep the projection event-sourced.

## Dependencies (calls out)

- **OrderingService** (`ports.Orders`) — `ListForTable` (unbilled) + `MarkBilled`.
- **CatalogService** (`ports.Menu`) — `GetItem` to resolve a line's name + course category.
- **SettingsService** (`ports.Settings`) — `GetEffective` for `billing.gst_pct`,
  `billing.service_charge_pct`, `billing.rounding`, `billing.currency`.
- **PromotionsService** (`ports.Promotions`) — `Evaluate` a coupon for `ApplyDiscount`.

All injected; unit tests use in-memory fakes. Endpoints from
`ORDERING_URL` / `CATALOG_URL` / `SETTINGS_URL` / `PROMOTIONS_URL`.

## Multi-tenancy

Billing is **per-outlet**: the tenant key is `restaurant_id`. Tables `bills`,
`tabs`, `processed_events` (+ `outbox`) carry `restaurant_id`/`tenant_id` with
**Postgres RLS** scoped to `current_setting('app.tenant_id')`, set per transaction
by `pkg/pg.WithTenant`. RPC callers' scope comes from the JWT (`pkg/tenancy`); the
event consumers derive it from the trusted event envelope/payload — a request body
never supplies the trusted tenant id.

## Layout

```
cmd/server/main.go                 composition root (grpc + board consumer + outbox relay)
internal/
  domain/                          pure Bill aggregate + tax/total math + Sections + Tab read-model
  app/                             use cases (OpenForTable, ApplyDiscount, TakePayment) + projection handlers
  ports/                           Repository, Orders, Menu, Settings, Promotions interfaces
  adapters/
    pg/                            Postgres repo + RLS + outbox/processed-event staging (bills, tabs)
    grpc/                          Connect handler (proto <-> domain, error mapping)
    clients/                       ordering/catalog/settings/promotions Connect clients
    nats/                          billing-board consumers (idempotent projection)
migrations/                        goose SQL (bills, payments(JSONB), tabs read-model, processed_events, outbox, RLS)
*_test.go                          table-driven unit tests (domain + app, in-memory fakes)
```

## Build / test / run

```bash
go test ./...                      # unit tests (domain + app), no DB needed

# container (build context = repo root so go.mod replaces resolve)
docker build -f services/billing/Dockerfile -t restorna/billing .
```

> Note: this repo has no Go toolchain in the authoring sandbox, so `go build` /
> `go test` were **not** run here — compile + the unit suite are intended to be run
> on the user's machine (`go work sync && go test ./services/billing/...`). The
> generated proto packages (`gen/go/restorna/billing/v1` + `billingv1connect`,
> `ordering`/`catalog`/`settings`/`promotions` clients, `common/v1`) must be
> produced by `buf generate` first.

### Env

`PORT`, `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`, `OTEL_EXPORTER_OTLP_ENDPOINT`
(shared base) plus `ORDERING_URL`, `CATALOG_URL`, `SETTINGS_URL`, `PROMOTIONS_URL`.
