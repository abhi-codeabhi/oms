# payments service

Integration-plane service that orchestrates **customer-facing money** over ANY
gateway (Razorpay / Paytm / PhonePe / UPI) through **connector-hub**. One internal
API; **idempotent intents**; signed webhooks normalize to captured/failed; refunds
+ reconciliation. `billing-oms` calls `CreateIntent` when a bill is settled online
and reconciles the bill from the events this service emits.

Hexagonal (`domain` → `app` → `ports` → `adapters`), per `CONVENTIONS.md`. Money is
always integer **minor units** + currency (never floats). Multi-tenant by
`restaurant_id` with Postgres **RLS**.

## RPCs (`restorna.payments.v1.PaymentsService`)

| RPC | Notes |
|-----|-------|
| `CreateIntent` | **Idempotent by `idempotency_key`.** Resolves the active provider via `ConnectorHubService.Resolve(CAPABILITY_PAYMENT, prefer_connector_id)`, instantiates the adapter (`pkg/connectors.New`), calls the gateway's `CreateIntent`, persists a `CREATED`→`PENDING` `Payment`, and returns the **provider handoff** map the customer app needs. A repeat key returns the existing payment (no double charge). |
| `Capture` | Auth+capture flows: confirms the intent via the provider, flips to `CAPTURED`, emits `payments.captured`. Idempotent on an already-captured payment. |
| `Refund` | Full/partial refund via the provider; records it, moves to `REFUNDED` when fully refunded; emits `payments.refunded`. Over-refund / currency mismatch → `FailedPrecondition`. |
| `GetPayment` | Fetch a payment by id (RLS-scoped to the caller's restaurant). |

## Choreography (NATS)

**Consumes** the normalized payment webhook events connector-hub publishes after it
verifies a provider webhook signature (via the connector's `VerifyWebhook`):

- `restorna.connector.payment.captured`
- `restorna.connector.payment.failed`

For each, it matches the `Payment` by `provider_ref`, flips status to
`CAPTURED`/`FAILED`, and **emits**:

- `restorna.payments.captured.v1` — `{payment_id, bill_id, restaurant_id, connector_id, provider_ref, amount_minor, currency, method, status, captured_at}`
- `restorna.payments.failed.v1` — `{payment_id, bill_id, restaurant_id, connector_id, provider_ref, amount_minor, currency, status, failed_at}`
- `restorna.payments.refunded.v1` — `{payment_id, bill_id, restaurant_id, provider_ref, refund_minor, refunded_total, currency, reason, status, refunded_at}`

`billing-oms` reconciles the table bill from these. Consumption is **idempotent**:
the webhook event id is marked processed in the **same transaction** as the status
flip (dedupe on `Event.ID`), so a redelivery is a no-op (exactly-once effect).

Emitted events are staged in the same tx as the write (`pkg/outbox.Stage`); a relay
drains the outbox to NATS.

## Dependencies (calls out)

- **ConnectorHubService** (`restorna.connector.v1`) via `ports.ConnectorHub`:
  `Resolve(CAPABILITY_PAYMENT, prefer_connector_id)` → provider id + decrypted
  per-tenant config. Injected; unit tests use a fake.
- **Provider adapters** via `ports.ProviderFactory` → `pkg/connectors.NewPayment(id, cfg)`
  → `pkg/connector.PaymentConnector` (Razorpay / Paytm / PhonePe / mock). The
  app/domain never import the connector SDK.

## Multi-tenancy

Tables `payments`, `processed_events` (+ `outbox`) carry `restaurant_id`/`tenant_id`
with **Postgres RLS** scoped to `current_setting('app.tenant_id')`, set per
transaction by `pkg/pg.WithTenant` from the JWT-derived restaurant (or the trusted
event envelope for webhooks). A request body never supplies the trusted tenant id —
it comes from the auth context (`pkg/tenancy`).

## Idempotency

- **CreateIntent** — unique `(restaurant_id, idempotency_key)`; a repeat call returns
  the existing payment + handoff without hitting the gateway again.
- **Webhooks** — `processed_events(event_id PK)` written in the same tx as the flip.
- **Status machine** — `MarkCaptured` / `MarkFailed` are idempotent on their target
  state so out-of-order / duplicated provider webhooks are safe.

## Config & runtime

Env (12-factor via `pkg/config`): `PORT`, `DATABASE_URL`, `NATS_URL`,
`JWT_PUBLIC_KEY`, `OTEL_EXPORTER_OTLP_ENDPOINT`, and `CONNECTORHUB_URL`
(default `http://connector-hub:8080`). `main.go` loads config → opens Postgres →
runs migrations → connects NATS → registers the Connect handler → starts the
webhook consumer + outbox relay → serves with graceful shutdown. `/healthz`
`/readyz` via `pkg/grpcx`.

## Layout

```
cmd/server/main.go                    composition root (grpc + webhook consumer + outbox relay)
internal/
  domain/                             Payment + status machine (pure, no infra)
  app/                                use cases (CreateIntent/Capture/Refund/GetPayment/OnWebhook)
  ports/                              Repository, ConnectorHub, ProviderFactory, PaymentProvider
  adapters/
    pg/                               Postgres repo + RLS + outbox staging + processed_events
    grpc/                             Connect handler (proto <-> domain, error mapping)
    connectorhub/                     ConnectorHubService client (ports.ConnectorHub)
    providers/                        pkg/connectors factory wrapper (ports.ProviderFactory)
    nats/                             payment-webhook consumer (choreography)
migrations/                           goose SQL (payments, processed_events, outbox, RLS)
*_test.go                             table-driven unit tests (fakes)
```

## Build / test

Generated Go (`gen/go/restorna/payments/v1` + `paymentsv1connect`, connector/v1,
common/v1) is produced by `make gen` (Buf) before building. This module was authored
without a local Go toolchain; run `go build ./...` and `go test ./...` from
`services/payments` (or `go work sync && go test ./...` at the repo root) on a
machine with Go 1.22 + the generated code present.
