# aggregators service

Integration-plane service: syncs the **menu out** to delivery aggregators
(Zomato/Swiggy) and ingests their **orders in**, normalizing each into an
`ExternalOrder` and **forwarding it to `ordering`** so it flows to the kitchen like
any dine-in order. It never talks to Zomato/Swiggy directly for menu push — it
resolves the active connector via **connector-hub** and calls the
`pkg/connectors` `AggregatorConnector` adapter (zomato/swiggy/mockagg).

Hexagonal (`domain` → `app` → `ports` → `adapters`), per `CONVENTIONS.md`.

## RPCs (`restorna.aggregators.v1.AggregatorsService`)

| RPC | Notes |
|-----|-------|
| `PushMenu` | Fetch the current menu from **`CatalogService.ListAllItems`**, serialize it, **resolve** the active aggregator via **`ConnectorHubService.Resolve`** (capability = aggregator, optional `connector_id` preference), and call the adapter's `PushMenu`. Returns `{ok, items}` (items accepted). |
| `ListExternalOrders` | List persisted `ExternalOrder`s, optionally filtered by `connector_id` and/or `status`. |
| `AckExternalOrder` | Accept / reject / update an order's status upstream. Persists the status transition. |

## Choreography (events consumed)

- **`restorna.connector.aggregator.order.received`** — the normalized event
  **connector-hub publishes** when a Zomato/Swiggy webhook arrives (the connector
  verifies the signature and normalizes to this shape:
  `{connector_id, external_ref, restaurant_id, status, currency, placed_at, items[]}`).
  The `adapters/nats` consumer decodes it, scopes the request to the restaurant
  (from the trusted event envelope, never a request body), and calls the app's
  `OnAggregatorOrder`, which:
  1. **persists an `ExternalOrder`**,
  2. **stages `restorna.aggregators.order.received.v1`** + marks the event id
     processed — all in **one transaction**, then
  3. **forwards** the order to **`OrderingService.PlaceOrder`** at a synthetic
     table **`AGG-<external_ref>`** so it hits the kitchen exactly like a dine-in
     order.

  **Idempotent** twice over: `pkg/eventbus/nats` dedupes on `Event.ID` in process,
  and the app dedupes on both the event id (`processed_events`) and the unique
  `(connector_id, external_ref)` — a redelivery (same event, or the same order via
  a different event) neither re-persists nor re-forwards to ordering.

## Events emitted (outbox → NATS, CloudEvents envelope)

- `restorna.aggregators.order.received.v1` —
  `{external_order_id, restaurant_id, connector_id, external_ref, status, table}`
  (staged in the **same transaction** as the `ExternalOrder` insert;
  `pkg/outbox.Stage` writes it, a relay drains to NATS).

## Dependencies (calls out)

- **ConnectorHubService** (`restorna.connector.v1`) — `Resolve` picks the active
  aggregator connector + returns its decrypted config; the service then
  instantiates the `pkg/connectors` adapter and calls `PushMenu`. `CONNECTORHUB_URL`.
- **CatalogService** (`restorna.catalog.v1`) — `ListAllItems` for the menu push.
  `CATALOG_URL`.
- **OrderingService** (`restorna.ordering.v1`) — `PlaceOrder` to forward ingested
  orders into the OMS. `ORDERING_URL`.

## Provider adapters (`pkg/connectors`)

`zomato`, `swiggy`, and `mockagg` implement `connector.AggregatorConnector`
(`PushMenu` + signed-webhook `VerifyWebhook` → normalized aggregator-order event).
`mockagg` is dependency-free for local dev (no signature check, no network).

## Multi-tenancy

Per-outlet: the tenant key is `restaurant_id`. Tables `external_orders`,
`processed_events` (+ `outbox`) carry `restaurant_id`/`tenant_id` with **Postgres
RLS** scoped to `current_setting('app.tenant_id')`, set per transaction by
`pkg/pg.WithTenant`. RPC callers' scope comes from the JWT (`pkg/tenancy`); the
event consumer derives it from the trusted event envelope/payload.

## Layout

```
cmd/server/main.go                 composition root (grpc + consumer goroutine + outbox relay)
internal/
  domain/                          pure ExternalOrder aggregate + status lifecycle (no infra)
  app/                             use cases (PushMenu, Ack, List) + OnAggregatorOrder handler
  ports/                           Repository, Catalog, ConnectorHub, Ordering interfaces
  adapters/
    pg/                            Postgres repo + RLS + outbox/processed-event staging
    grpc/                          Connect handler (proto <-> domain, error mapping)
    clients/                       CatalogService, OrderingService, ConnectorHubService clients
    nats/                          aggregator.order.received consumer (idempotent choreography)
migrations/                        goose SQL (external_orders, processed_events, outbox, RLS)
*_test.go                          table-driven unit tests (domain + app, in-memory fakes)
```

## Build / test / run

```bash
go test ./...                      # unit tests (domain + app), no DB needed

# container (build context = repo root so go.mod replaces resolve)
docker build -f services/aggregators/Dockerfile -t restorna/aggregators .
```

### Env

`PORT`, `DATABASE_URL`, `NATS_URL`, `JWT_PUBLIC_KEY`, `OTEL_EXPORTER_OTLP_ENDPOINT`
(shared base) plus `CONNECTORHUB_URL`, `CATALOG_URL`, `ORDERING_URL`.
