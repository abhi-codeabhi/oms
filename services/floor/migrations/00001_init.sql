-- +goose Up
-- +goose StatementBegin

-- Floor plan. ONE row per restaurant (the floor's tenant key is per-outlet). The
-- table set is stored as JSONB on the row — the floor is read/written wholesale and
-- never queried per-table in SQL. Each stored table carries its seat/order/waiter
-- plus the nudge timestamps (seated_at/greeted_at/last_served_at/last_checkin_at,
-- epoch ms). The LIVE cooking/ready/billing status is DERIVED at read time from
-- kitchen tickets + open bills and is never persisted, so it can't go stale.
-- RLS scopes every row to current_setting('app.tenant_id'), set per transaction by
-- pkg/pg.WithTenant from the JWT-derived restaurant.
CREATE TABLE floors (
    restaurant_id TEXT PRIMARY KEY,
    tables        JSONB NOT NULL DEFAULT '[]'::jsonb,  -- [{n,status,order,waiter_id,seated_at,greeted_at,last_served_at,last_checkin_at}]
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Processed events for idempotent choreography (dedupe ordering.order.placed and
-- kitchen.ticket.served by the event id). Written in the SAME tx as the floor
-- upsert so a redelivery is a no-op (exactly-once effect).
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
    source       TEXT NOT NULL DEFAULT 'floor',
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    data         JSONB NOT NULL,
    published_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished ON outbox (occurred_at) WHERE published_at IS NULL;

-- Row-Level Security: scope every tenant table to app.tenant_id (the restaurant).
ALTER TABLE floors           ENABLE ROW LEVEL SECURITY;
ALTER TABLE processed_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox           ENABLE ROW LEVEL SECURITY;

ALTER TABLE floors           FORCE ROW LEVEL SECURITY;
ALTER TABLE processed_events FORCE ROW LEVEL SECURITY;
ALTER TABLE outbox           FORCE ROW LEVEL SECURITY;

CREATE POLICY floors_tenant_isolation ON floors
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
DROP TABLE IF EXISTS floors;
-- +goose StatementEnd
