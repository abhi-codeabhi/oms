# promotions

Coupons and scheduled discounts for the Restorna OMS data plane. Billing/customer
surfaces call `Evaluate` to get the best discount for a cart context; owners/managers
configure coupons via `UpsertCoupon` / `ListCoupons` / `ToggleCoupon`.

Hexagonal (CONVENTIONS.md): `internal/{domain,app,ports,adapters/{pg,grpc}}`, goose
migrations, table-driven unit tests on domain + app. Coupons are keyed by `code`
within a `restaurant_id` (the tenant), scoped by Postgres RLS. Money is integer minor
units. Ported from the proven Restorna Node promotions service (coupon model +
engine.js discount evaluation).

## RPCs (`restorna.promotions.v1.PromotionsService`)

| RPC | Description |
|-----|-------------|
| `UpsertCoupon(Coupon) -> Coupon` | Create or replace a coupon (keyed by code within the outlet). Validates type (`percent`/`flat`), value > 0 (percent 1–100), optional `min_order`, optional `category` restriction, optional `starts_at`/`ends_at` RFC3339 window. |
| `ListCoupons() -> []Coupon` | All coupons configured for the caller's restaurant. |
| `ToggleCoupon(code, active) -> Coupon` | Flip a coupon active/inactive. `NotFound` if the code does not exist. |
| `Evaluate(subtotal, coupon_code, category) -> (discount Money, applied string)` | Return the best (largest) applicable discount plus the winning coupon's code. |

The trusted tenant (`restaurant_id`) always comes from the JWT-derived auth context
(`tenancy.From`), never the request body.

## Discount engine (`Evaluate`)

Ported from the Node demo (`domain/coupon.js` `applyCoupon` + `domain/engine.js`
`evaluate`). For each candidate coupon the engine validates, in order:

1. **active** — inactive coupons never apply;
2. **time window** — outside `[starts_at, ends_at]` it does not apply (`not_started` / `expired`);
3. **category** — a category-restricted coupon only applies when the cart category matches;
4. **min_order** — the subtotal must meet `min_order`;
5. **discount** — `percent` => `round(subtotal * value / 100)`, `flat` => `value`; both **capped at the subtotal**.

Selection is **best-of / non-stacking**: a single largest discount wins. When
`coupon_code` is supplied only that coupon is considered; otherwise every coupon is
evaluated and the best applicable one is returned. When a discount is granted a
`restorna.promotions.promo.applied.v1` event is staged (best-effort).

## Events

| Type | When |
|------|------|
| `restorna.promotions.coupon.upserted.v1` | A coupon is created/updated (staged in the upsert tx). |
| `restorna.promotions.promo.applied.v1` | `Evaluate` granted a non-zero discount. |

Staged via the transactional outbox (`pkg/outbox`); a relay drains to NATS.

## Config (env, 12-factor)

`PORT`, `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`, `OTEL_EXPORTER_OTLP_ENDPOINT`
(see `pkg/config.Base`).

## Build & test (on a machine with the Go toolchain)

```
go test ./...                     # unit tests (domain + app, in-memory fakes)
go build ./...                    # compile
docker build -f services/promotions/Dockerfile -t restorna/promotions .   # context = repo root
```
