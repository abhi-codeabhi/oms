# catalog service

Data-plane (OMS) service that owns the **menu**: categories + items, with a
**brand menu** and **per-outlet overrides** (price / availability). 86-ing toggles
an item's availability at the outlet. Ported from the proven Restorna Node catalog
domain (item/category model, dietary preference engine, availability rules).

Hexagonal (`domain` → `app` → `ports` → `adapters`), per `CONVENTIONS.md`.

## RPCs (`restorna.catalog.v1.CatalogService`)

| RPC | Notes |
|-----|-------|
| `UpsertCategory` | Create/update a course (Appetizers/Mains/Breads/Drinks). |
| `ListCategories` | Outlet's categories, ordered by `sort` then name. |
| `UpsertItem` | Create/update a brand item (category, `Money` price, veg, dietary `tags`, prep_minutes, station, image). New items default available; updates preserve availability. Emits `menu.published`. |
| `GetItem` | One item with **effective** (post-override) price/availability. Used by the order saga to resolve a line into name + station. |
| `GetMenu` | Customer menu: items with effective availability. `only_available` returns available items only; `prefs` evaluates each item and **flags dietary conflicts** (returned items carry an OK/reasons evaluation) rather than hiding them. |
| `ListAllItems` | Manager view: every item incl. unavailable, with overrides applied. |
| `SetAvailability` | **86 / un-86** an item at the outlet (writes a per-outlet availability override). Emits `item.86d` when it goes unavailable. |
| `SetOutletOverride` | Per-outlet price/availability override of a brand item; `clear=true` reverts to brand defaults. Emits `item.86d` if the override takes the item offline. |

The trusted outlet id (`restaurant_id`) ALWAYS comes from the JWT auth context
(`pkg/tenancy`), never the request body.

## Domain rules (ported from the Node demo)

- **Dietary engine** (`domain.Evaluate`): a preference lists flags to AVOID
  (`vegetarian` avoids meat/fish, `vegan` adds dairy/egg, `glutenfree`, `nutfree`,
  `eggless`, `pregnancy`, `lowsugar`, `mild`); an item conflicts if it carries any
  avoided dietary tag. Unknown prefs are ignored. Returns `{ok, reasons}`.
- **Availability / 86**: brand items default available; the outlet 86s via an
  override; `item.86d` is emitted only when an item goes **unavailable**.
- **Outlet override**: `Item.Effective(override)` yields the item as it appears at
  one outlet — override price (if set) and/or availability (if set), leaving the
  brand item untouched.
- **Money** is `int64` minor units + currency (`pkg/money`), never float.

## Events emitted (outbox → NATS, CloudEvents envelope)

- `restorna.catalog.item.86d.v1` — `{item_id, restaurant_id, name, station}`
- `restorna.catalog.menu.published.v1` — `{restaurant_id, version, item_count}`

Staged in the **same transaction** as the write (`pkg/outbox.Stage`); a relay
drains to NATS.

## Multi-tenancy

Tables `categories`, `items`, `item_overrides` (+ `outbox`) carry `restaurant_id`
with **Postgres RLS** scoped to `current_setting('app.tenant_id')`, set per
transaction by `pkg/pg.WithTenant` from the JWT-derived outlet. Brand items and
per-outlet overrides are both scoped to the outlet's tenant.

## Layout

```
cmd/server/main.go                 composition root
internal/
  domain/                          pure types + rules (no infra): items, categories,
                                   dietary engine, override application
  app/                             use cases (depend on ports)
  ports/                           Repository + Tx interfaces
  adapters/
    pg/                            Postgres repo + RLS + outbox staging (JSONB tags)
    grpc/                          Connect handler (proto <-> domain, error mapping)
migrations/                        goose SQL (RLS policies)
*_test.go                          table-driven unit tests (domain + app, in-memory fakes)
```

## Build / test / run

```bash
go test ./...                      # unit tests (domain + app), no DB needed

# container (build context = repo root so go.mod replaces resolve)
docker build -f services/catalog/Dockerfile -t restorna/catalog .
```

### Env

`PORT`, `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`, `OTEL_EXPORTER_OTLP_ENDPOINT`
(shared base via `pkg/config`).
