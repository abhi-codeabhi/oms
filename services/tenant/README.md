# tenant service

Control-plane service that owns the customer hierarchy **Owner → Brand → Restaurant
(outlet)** plus branding (logo assets), tax/locale profile, and active/inactive
state. Source of truth for "who exists" in tenant-land; `identity` authenticates,
this models the org.

Hexagonal (`domain` → `app` → `ports` → `adapters`), per `CONVENTIONS.md`.

## RPCs (`restorna.tenant.v1.TenantService`)

| RPC | Notes |
|-----|-------|
| `CreateOwner` | Create a billable owner account. |
| `GetOwner` | Fetch owner (RLS-scoped to the caller's owner). |
| `CreateBrand` | **Reserves `brands` quota** (or unlocks via `multi_brand` feature) before persist; emits `brand.created`. `ResourceExhausted` + upgrade hint when over limit. |
| `SetBrandLogo` | Attach a logo `Asset`. Raw-byte uploads go through the `BlobStore` port; this RPC accepts an already-uploaded asset ref. |
| `ListBrands` | Owner's brands, paginated (offset token). |
| `CreateRestaurant` | **Reserves `outlets` quota** before persist; resolves owner from the brand (never from the body); emits `outlet.provisioned`. `ResourceExhausted` when over limit. |
| `ListRestaurants` | A brand's outlets, paginated. |
| `SetRestaurantActive` | Toggle an outlet on/off. |

## Events emitted (outbox → NATS, CloudEvents envelope)

- `restorna.tenant.brand.created.v1` — `{brand_id, owner_id, name, primary_color, created_at}`
- `restorna.tenant.outlet.provisioned.v1` — `{restaurant_id, brand_id, owner_id, name, timezone, active, created_at}`

Staged in the **same transaction** as the write (`pkg/outbox.Stage`); a relay drains
to NATS.

## Dependencies (calls out)

- **EntitlementsService** (`restorna.entitlements.v1`) via the `ports.Entitlements`
  port: `ReserveQuota` for `brands` / `outlets`, `HasFeature` for `multi_brand`,
  `ReleaseQuota` to compensate a failed create. Injected; unit tests use a fake.
- **BlobStore** port for logo storage — cloud-agnostic. Ships a local-filesystem
  reference impl (`adapters/blob`); an S3/GCS impl satisfies the same small
  `objectStore` interface with no SDK leakage into the domain.

## Multi-tenancy

Tables `owners`, `brands`, `restaurants` (+ `outbox`) carry `owner_id`/`tenant_id`
with **Postgres RLS** scoped to `current_setting('app.tenant_id')`, set per
transaction by `pkg/pg.WithTenant` from the JWT-derived owner. A request body never
supplies the trusted owner id — it comes from the auth context (`pkg/tenancy`).

## Layout

```
cmd/server/main.go                 composition root
internal/
  domain/                          pure types + rules (no infra)
  app/                             use cases (depend on ports)
  ports/                           Repository, Entitlements, BlobStore interfaces
  adapters/
    pg/                            Postgres repo + RLS + outbox staging
    grpc/                          Connect handler (proto <-> domain, error mapping)
    entitlements/                  EntitlementsService client (port impl)
    blob/                          cloud-agnostic blob store (fs reference impl)
migrations/                        goose SQL (RLS policies)
*_test.go                          table-driven unit tests (domain + app, in-memory fakes)
```

## Build / test / run

```bash
go test ./...                      # unit tests (domain + app), no DB needed

# container (build context = repo root so go.mod replaces resolve)
docker build -f services/tenant/Dockerfile -t restorna/tenant .
```

### Env

`PORT`, `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`, `OTEL_EXPORTER_OTLP_ENDPOINT`
(shared base) plus `ENTITLEMENTS_URL`, `BLOB_DIR`, `BLOB_PUBLIC_BASE_URL`.
