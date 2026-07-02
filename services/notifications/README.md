# notifications service

Integration-plane service that sends **SMS / WhatsApp / email / push** over any
provider (Twilio / MSG91 / … via connector-hub) behind **one API**. Templated,
idempotent, async with delivery status. `identity` (OTP), `staff` (invites),
`onboarding`, and `billing` (receipts) call this.

Hexagonal (`domain` → `app` → `ports` → `adapters`), per `CONVENTIONS.md`.

## RPCs (`restorna.notifications.v1.NotificationsService`)

| RPC | Notes |
|-----|-------|
| `Send` | **Idempotent** by `idempotency_key` (a repeat returns the prior message, no re-dispatch). Renders the template with `vars`, **resolves** the tenant's provider for `CAPABILITY_NOTIFICATION` via `ConnectorHubService.Resolve`, instantiates the adapter via `pkg/connectors.NewNotification`, calls `Send`, and persists a `Message` **QUEUED → SENT** with the provider ref. Falls back to the built-in **`lognotify`** mock when no provider is installed. |
| `GetStatus` | Fetch a message + its current `DeliveryStatus` (RLS-scoped to the caller's owner). |
| `UpsertTemplate` | Create/replace owner/brand-configurable copy, per channel (`{{var}}` placeholders). |
| `ListTemplates` | The owner's templates, sorted by id. |

## Templates & rendering

Templates are **owner-scoped copy** keyed by `id` + `channel` (e.g. `otp`,
`staff_invite`, `receipt`). Bodies/subjects carry `{{var}}` placeholders rendered
with the `Send` vars (whitespace tolerated: `{{ name }}` == `{{name}}`; unknown vars
render empty so no raw token leaks). The migration seeds **platform default
templates** under `own_platform`; `GetTemplate` falls back to them so identity OTP and
staff invites work out of the box before an owner customizes anything. An owner
`UpsertTemplate` with the same id shadows the default.

## Provider resolution & the dev fallback

`Send` asks `ConnectorHubService.Resolve(CAPABILITY_NOTIFICATION)` for the tenant's
active provider + decrypted config, then builds the adapter with
`pkg/connectors.NewNotification(connectorID, cfg)`. When **no provider is installed**
(Resolve returns not-installed) — or a provider is installed but misconfigured — the
service falls back to the built-in **`lognotify`** mock connector, which logs the
message and returns a synthetic ref. This is what lets OTP / invites "work" in dev
with zero credentials.

## Delivery-status webhooks (consumed)

connector-hub ingests a provider's delivery-report webhook, verifies it via the
connector, and publishes a normalized status event. The NATS consumer subscribes to:

- `restorna.notifications.status.v1` (twilio / msg91 delivery reports)
- `restorna.notifications.delivery.updated.v1` (lognotify mock)

…locates the message by `(provider_id, provider_ref)`, and advances its
`DeliveryStatus` (`QUEUED/SENT → DELIVERED/FAILED`), ignoring out-of-order
regressions. Consumers **dedupe on the CloudEvent id** (`processed_events`) for
exactly-once effect.

## Events emitted (outbox → NATS, CloudEvents envelope)

- `restorna.notifications.message.sent.v1` — on successful provider acceptance
- `restorna.notifications.message.failed.v1` — on provider send failure
- `restorna.notifications.message.updated.v1` — on a delivery-status advance

Staged in the **same transaction** as the write (`pkg/outbox.Stage`); a relay drains
to NATS.

## Multi-tenancy

Tables `messages`, `templates` (+ `outbox`) carry `owner_id`/`tenant_id` with
**Postgres RLS** scoped to `current_setting('app.tenant_id')`, set per transaction by
`pkg/pg.WithTenant` from the JWT-derived owner. The trusted owner id comes from the
auth context (`pkg/tenancy`), never the request body. The delivery-status webhook
path resolves a message by provider ref under the empty-tenant admin scope (a
provider callback is not tied to a JWT); `processed_events` is not tenant-scoped.

## Layout

```
cmd/server/main.go                 composition root (server + relay + consumer)
internal/
  domain/                          Message, Template, render (no infra)
  app/                             use cases (Send/GetStatus/UpsertTemplate/List, delivery status)
  ports/                           Repository, ConnectorHub, ProviderFactory, NotificationSender
  adapters/
    pg/                            Postgres repo + RLS + outbox staging + processed_events
    grpc/                          Connect handler (proto <-> domain, error mapping)
    connectorhub/                  ConnectorHubService client (Resolve) -> ports.ConnectorHub
    providers/                     pkg/connectors factory -> ports.ProviderFactory (+ lognotify fallback)
    nats/                          delivery-status webhook consumer
migrations/                        goose SQL (messages, templates, processed_events, RLS, default copy)
*_test.go                          table-driven unit tests (domain + app, in-memory fakes)
```

## Build / test / run

```bash
go test ./...                      # unit tests (domain + app), no DB needed

# container (build context = repo root so go.mod replaces resolve)
docker build -f services/notifications/Dockerfile -t restorna/notifications .
```

### Env

`PORT`, `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`, `OTEL_EXPORTER_OTLP_ENDPOINT`
(shared base) plus `CONNECTORHUB_URL` (default `http://connectorhub:8080`).
