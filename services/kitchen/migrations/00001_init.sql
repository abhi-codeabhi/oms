-- +goose Up
-- +goose StatementBegin

-- Kitchen (KDS) tickets. Each ticket is a fired order on the cook display, scoped
-- to one restaurant (the KDS tenant key). Items are stored as JSONB on the row —
-- the kitchen reads/writes whole tickets and never queries items in isolation.
-- RLS scopes every row to current_setting('app.tenant_id'), set per transaction by
-- pkg/pg.WithTenant from the JWT-derived restaurant.
CREATE TABLE tickets (
    id            TEXT PRIMARY KEY,
    restaurant_id TEXT NOT NULL,
    order_id      TEXT NOT NULL,
    table_label   TEXT NOT NULL,
    items         JSONB NOT NULL DEFAULT '[]'::jsonb,  -- [{id,name,station,state}]
    served        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_tickets_restaurant ON tickets (restaurant_id, created_at);
CREATE INDEX idx_tickets_order ON tickets (order_id);

-- Processed events for idempotent choreography (dedupe ordering.order.placed by
-- the event id). Written in the SAME tx as the ticket insert so a redelivery is a
-- no-op (exactly-once effect).
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
    source       TEXT NOT NULL DEFAULT 'kitchen',
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    data         JSONB NOT NULL,
    published_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished ON outbox (occurred_at) WHERE published_at IS NULL;

-- Row-Level Security: scope every tenant table to app.tenant_id (the restaurant).
ALTER TABLE tickets          ENABLE ROW LEVEL SECURITY;
ALTER TABLE processed_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox           ENABLE ROW LEVEL SECURITY;

ALTER TABLE tickets          FORCE ROW LEVEL SECURITY;
ALTER TABLE processed_events FORCE ROW LEVEL SECURITY;
ALTER TABLE outbox           FORCE ROW LEVEL SECURITY;

CREATE POLICY tickets_tenant_isolation ON tickets
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
DROP TABLE IF EXISTS tickets;
-- +goose StatementEnd
