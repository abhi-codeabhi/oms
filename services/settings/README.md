# settings

The **configurability core** — business rules as data. A setting is **defined**
once (key, type, default, scope ceiling, validation, who-can-edit) and
**overridden** per owner / brand / restaurant. Services read effective values via
`GetEffective` instead of hard-coding policy. Examples: `billing.gst_pct=5`,
`billing.rounding="nearest_1"`, `ordering.require_prepay=false`,
`floor.nudge.greet_secs=30`, `brand.theme.accent="#9E7C46"`.

Part of the Restorna control plane (M1). Hexagonal, Connect-Go, Postgres + RLS.

## RPCs (`restorna.settings.v1.SettingsService`)

| RPC | Purpose |
|-----|---------|
| `RegisterDefinitions` | Services self-register their setting definitions on boot — **idempotent upsert by key**. Each definition is validated (incl. its own default). |
| `ListDefinitions` | The definition catalog, optionally filtered by `namespace` (dot-boundary prefix). |
| `SetOverride` | Store a value at an owner/brand/restaurant scope. Enforces `max_scope`, `editable_by` (role from tenancy ctx) and runs `validation` (min/max, enum membership, type parse). Over-deep scope or wrong role → `PermissionDenied`; bad value → `InvalidArgument`. |
| `GetEffective` | Resolve keys for a `TenantRef` by precedence **restaurant > brand > owner > definition default**. Returns each `SettingValue` with its `source_scope`. **Hot path** — fronted by an in-process TTL cache. |

## Resolution & precedence

`GetEffective` walks the overrides that apply to the requested
`(owner, brand, restaurant)` tuple and picks the **deepest** match:

```
restaurant override  >  brand override  >  owner override  >  definition default
```

The returned `source_scope` records where the value came from (`SCOPE_RESTAURANT`,
…, or `SCOPE_UNSPECIFIED` when it fell back to the definition default). Pure
resolution lives in `domain.Resolve`.

## Definitions (the rules-as-data)

A `Definition` declares:

- **key** — dotted namespace (`billing.gst_pct`).
- **type** — `INT | BOOL | STRING | DECIMAL | JSON | ENUM`.
- **default** — canonical string value (validated against the definition itself).
- **max_scope** — the deepest level it may be overridden at (`OWNER` / `BRAND` /
  `RESTAURANT`). `SetOverride` rejects anything deeper.
- **enum_options** — allowed values when `type=ENUM`.
- **validation** — `min:/max:` bounds (numeric range, or string length).
- **editable_by** — role ladder floor: `platform_admin` ⊇ `owner`/`brand_admin` ⊇
  `manager`. A setting editable by `manager` is also editable by owners/admins.
- **feature_gated** — hidden unless an entitlement feature is on (surfaced to UIs).

Services self-register their definitions via `RegisterDefinitions` on boot; the
upsert is idempotent so redeploys are safe. A **starter set** is also seeded by
migration `0002_seed_definitions.sql`:

| key | type | default | max_scope | editable_by |
|-----|------|---------|-----------|-------------|
| `billing.gst_pct` | INT | `5` | restaurant | owner |
| `billing.service_charge_pct` | INT | `0` | restaurant | owner |
| `billing.rounding` | ENUM | `nearest_1` (`nearest_1`/`none`) | restaurant | owner |
| `billing.currency` | STRING | `INR` | owner | platform_admin |
| `ordering.require_prepay` | BOOL | `false` | restaurant | owner |
| `floor.nudge.greet_secs` | INT | `30` | restaurant | manager |
| `floor.nudge.checkin_secs` | INT | `300` | restaurant | manager |
| `floor.call.cooldown_secs` | INT | `60` | restaurant | manager |
| `brand.theme.accent` | STRING | `#9E7C46` | brand | owner |

## Caching (hot path)

`GetEffective` is fronted by a simple in-process **TTL cache** keyed by
`(owner, brand, restaurant, key)` (`SETTINGS_CACHE_TTL_SECS`, default 30s). A
`SetOverride` **immediately invalidates every cache entry for that owner**, so
local reads are consistent within the service.

Other services that cache settings locally **must invalidate on the event** —
they subscribe to **`restorna.settings.override.changed.v1`** and drop their
relevant entries when it fires.

## Events

| Event | When | Payload |
|-------|------|---------|
| `restorna.settings.override.changed.v1` | after a successful `SetOverride` | `key, owner_id, brand_id?, restaurant_id?, scope, raw` |

Emitted via the **transactional outbox** (`pkg/outbox.Stage`) in the same tx as
the override write; a relay drains it to NATS. Consumers dedupe on the event id.

## Multi-tenancy

- **definitions** are **global** control-plane data — no RLS; every tenant sees
  the same catalog.
- **overrides** + **outbox** are **owner-scoped** — RLS by `owner_id` via
  `current_setting('app.tenant_id')`, set per tx by `pkg/pg.WithTenant`. The owner
  id is taken from the JWT-derived `tenancy.Scope`, never the request body;
  brand/restaurant targeting from the request is always nested under that owner.

## Layout (hexagonal)

```
cmd/server/main.go                     composition root (repo + cache + role reader + relay)
internal/domain/                       pure model + rules (validation, precedence, scope/role checks)
internal/app/                          use cases over ports (register/list/set/get + cache)
internal/ports/                        repo + cache interfaces
internal/adapters/pg/                  Postgres repos (global defs; RLS overrides; outbox stage)
internal/adapters/cache/               in-process TTL cache (invalidated on SetOverride)
internal/adapters/grpc/                Connect handler + proto<->domain mapping + role from ctx
migrations/                            goose: 0001 schema+RLS+outbox, 0002 seed definitions
*_test.go                              table-driven unit tests (domain + app + cache, in-memory fakes)
```

## Config (env, 12-factor)

`PORT`, `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`, `OTEL_EXPORTER_OTLP_ENDPOINT`,
`SETTINGS_CACHE_TTL_SECS` (default `30`).

## Run

```bash
make run/settings              # from repo root, against docker-compose infra
go test ./...                  # unit tests (no DB/network)
go test -tags=integration ./...# adapter tests (Postgres testcontainer; CI)
```

## Build image

```bash
# build context is the repo root (sibling gen/ + pkg/ are needed by replaces)
docker build -f services/settings/Dockerfile -t restorna/settings .
```

Multi-stage, distroless static, non-root, reads `PORT`.
