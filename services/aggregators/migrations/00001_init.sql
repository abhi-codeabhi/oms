-- +goose Up
-- +goose StatementBegin

-- External (delivery-aggregator) orders ingested from Zomato/Swiggy via
-- connector-hub, scoped to one restaurant (the aggregators tenant key). Items are
-- stored as JSONB on the row — the service reads/writes whole orders and never
-- queries items in isolation. RLS scopes every row to
-- current_setting('app.tenant_id'), set per transaction by pkg/pg.WithTenant from
-- the JWT-derived (or event-derived) restaurant.
CREATE TABLE external_orders (
    id            TEXT PRIMARY KEY,
    restaurant_id TEXT NOT NULL,
    connector_id  TEXT NOT NULL,                      -- zomato|swiggy|mockagg
    external_ref  TEXT NOT NULL,                      -- the aggregator's order id
    status        TEXT NOT NULL DEFAULT 'received',   -- received|accepted|preparing|ready|dispatched|cancelled|rejected
    items         JSONB NOT NULL DEFAULT '[]'::jsonb, -- [{name,qty,price_minor,price_currency}]
    placed_at     TEXT NOT NULL DEFAULT '',           -- aggregator-reported placed_at (free-form)
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- (connector_id, external_ref) is globally unique: the same aggregator order is
-- ingested at most once (idempotent webhook ingestion, belt-and-braces alongside
-- the processed_events dedupe).
CREATE UNIQUE INDEX uq_external_orders_ref ON external_orders (connector_id, external_ref);
CREATE INDEX idx_external_orders_restaurant ON external_orders (restaurant_id, created_at);
CREATE INDEX idx_external_orders_status ON external_orders (restaurant_id, status);

-- Processed events for idempotent choreography (dedupe the normalized
-- aggregator-order event by its event id). Written in the SAME tx as the order
-- insert so a redelivery is a no-op (exactly-once effect).
CREATE TABLE processed_events (
    event_id      TEXT PRIMARY KEY,
    restaurant_id TEXT NOT NULL,
    processed_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Transactional outbox (pkg/outbox.Stage writes here; a relay drains to NATS).
CREATE TABLE outbox (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    tenant_id    TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT 'aggregators',
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    data         JSONB NOT NULL,
    published_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished ON outbox (occurred_at) WHERE published_at IS NULL;

-- Row-Level Security: scope every tenant table to app.tenant_id (the restaurant).
ALTER TABLE external_orders  ENABLE ROW LEVEL SECURITY;
ALTER TABLE processed_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox           ENABLE ROW LEVEL SECURITY;

ALTER TABLE external_orders  FORCE ROW LEVEL SECURITY;
ALTER TABLE processed_events FORCE ROW LEVEL SECURITY;
ALTER TABLE outbox           FORCE ROW LEVEL SECURITY;

CREATE POLICY external_orders_tenant_isolation ON external_orders
    USING (restaurant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (restaurant_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY processed_events_tenant_isolation ON processed_events
    USING (restaurant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (restaurant_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY outbox_tenant_isolation ON outbox
    USING (tenant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', TRUE));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS processed_events;
DROP TABLE IF EXISTS external_orders;
-- +goose StatementEnd
