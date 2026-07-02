# connector-hub service

The **plug-and-play integration framework** (ARCHITECTURE.md §6). It is the
connector *registry + per-tenant configuration + capability routing + inbound
webhook ingestion* for the integration plane. Connectors (payment / aggregator /
crm / erp / notification) declare a `Manifest`; owners **install** and configure
them per tenant with **credentials encrypted at rest**, gated by **entitlements**
(a plan unlocks which connectors are available). payments / aggregators /
notifications call `Resolve` to pick the right provider and get its decrypted
config.

Hexagonal (`domain` → `app` → `ports` → `adapters`), per `CONVENTIONS.md`.

## RPCs (`restorna.connector.v1.ConnectorHubService`)

| RPC | Notes |
|-----|-------|
| `ListAvailable` | Marketplace of connector manifests from `connectors.All()`, **FILTERED by entitlements** — a connector shows only if a capability it declares is unlocked by the owner's plan (`HasFeature("payments"/"aggregators"/…)`). |
| `Install` | **Reserves the `connectors` quota** (`ResourceExhausted` + upgrade hint when over limit), splits config into public/secret via the manifest schema, **ENCRYPTS secret values** (AES-256-GCM under `CONNECTOR_KEK`), and stores one `Installation` per tenant. Releases the reservation if persistence fails. |
| `UpdateInstallation` | Toggle `enabled` and merge config. Supplied secret keys are re-encrypted; omitted secrets are preserved. |
| `ListInstallations` | Owner's installations, optional capability filter. **Secrets are write-only — never echoed** (only public config is returned). |
| `Resolve` | *(internal)* Returns the active (enabled) connector for a capability + its **DECRYPTED** config; prefers `prefer_connector_id`, skips disabled installs. Used by payments/aggregators/notifications. |
| `IngestWebhook` | *(public edge)* Looks up the connector by id, hands the raw body + headers to the adapter's **`VerifyWebhook`** to authenticate (rejects tampered/forged signatures) + normalize, then **publishes** the resulting CloudEvent to NATS and returns `event_type`. |

## Encryption at rest (envelope encryption)

Secret connector config never persists in plaintext. `domain.Envelope`
(pure, stdlib AES-256-GCM) seals the secret config map (`nonce‖ciphertext‖tag`)
under a **Key-Encryption-Key** decoded from `CONNECTOR_KEK` (base64 / hex / raw 32
bytes). The `secret_config` column is `BYTEA` ciphertext; `public_config` is JSONB
plaintext (the only part ever returned). Tampering is detected by the GCM auth tag
(a flipped byte fails decryption). The design is envelope-ready: swapping to
per-installation data keys wrapped by the KEK is additive and does not change the
`Crypto` port.

## Connectors (built by the connectors agent)

The concrete adapters + registry live in **`pkg/connectors`**; this service codes
against `connectors.All() []connector.Manifest` and
`connectors.New(id, cfg) (connector.Connector, error)` through the
`adapters/registry` port impl. Webhook verification delegates to the connector's
`VerifyWebhook(ctx, body, sig)` (the `pkg/connector` SDK contract); the registry
extracts the provider signature from the request headers.

## Events

- `restorna.connector.installation.created.v1` — `{installation_id, owner_id, restaurant_id, connector_id, test_mode, installed_at}` (outbox → NATS on install).
- Inbound provider webhooks are normalized by the connector into a CloudEvent
  (e.g. `restorna.payments.captured.v1`) and **published directly** to NATS by
  `IngestWebhook` (not part of a DB tx, so it bypasses the outbox relay).

## Multi-tenancy

`installations` (+ `outbox`) carry `owner_id`/`tenant_id` with **Postgres RLS**
scoped to `current_setting('app.tenant_id')`, set per transaction by
`pkg/pg.WithTenant` from the JWT-derived owner. Installs are unique per
`(owner, connector, restaurant scope)`. A request body never supplies the trusted
owner id — it comes from the auth context (`pkg/tenancy`).

## Layout

```
cmd/server/main.go                 composition root
internal/
  domain/                          Installation + envelope encryption (pure)
  app/                             use cases (depend on ports)
  ports/                           Repository, Entitlements, Crypto, EventBus, Connectors
  adapters/
    pg/                            Postgres repo + RLS + outbox staging (encrypted column)
    grpc/                          Connect handler (proto <-> domain, secrets write-only)
    entitlements/                  EntitlementsService client (port impl)
    crypto/                        AES-256-GCM envelope (KEK from CONNECTOR_KEK)
    nats/                          webhook event publisher
    registry/                     pkg/connectors wrapper (manifests + VerifyWebhook)
migrations/                        goose SQL (installations, encrypted secret_config, RLS)
*_test.go                          table-driven unit tests (domain + app, in-memory fakes)
```

## Build / test / run

```bash
go test ./...                      # unit tests (domain + app), no DB needed

# container (build context = repo root so go.mod replaces resolve)
docker build -f services/connectorhub/Dockerfile -t restorna/connectorhub .
```

> Note: this repo has no Go toolchain wired in the authoring sandbox, so
> `go build` / `go test` were **not** run here — compile + test on a machine with
> Go 1.22 and the generated `gen/go` (buf) + `pkg/connectors` present.

### Env

`PORT`, `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`, `OTEL_EXPORTER_OTLP_ENDPOINT`
(shared base) plus `ENTITLEMENTS_URL` and **`CONNECTOR_KEK`** (required — a
32-byte AES-256 key as base64/hex/raw; the service refuses to start without it).
