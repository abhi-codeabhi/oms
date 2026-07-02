-- +goose Up
-- +goose StatementBegin

-- Operational billing tables for the Restorna OMS, scoped to one restaurant (the
-- billing tenant key). Money is INTEGER MINOR UNITS (paise). RLS scopes every row
-- to current_setting('app.tenant_id'), set per transaction by pkg/pg.WithTenant
-- from the JWT-derived (or trusted event envelope) restaurant.

-- bills: the aggregated, settle-able dine-in bill for a table. Lines + payments
-- are JSONB on the row (billing reads/writes whole bills). order_ids are the
-- contributing unbilled orders that were aggregated + marked billed.
CREATE TABLE bills (
    id             TEXT PRIMARY KEY,                       -- bill_...
    restaurant_id  TEXT NOT NULL,
    table_label    TEXT NOT NULL,
    order_ids      TEXT[] NOT NULL DEFAULT '{}',
    lines          JSONB NOT NULL DEFAULT '[]'::jsonb,     -- [{id,name,category,price_minor}]
    discount_minor BIGINT NOT NULL DEFAULT 0,              -- accumulated flat discount
    payments       JSONB NOT NULL DEFAULT '[]'::jsonb,     -- [{id,method,amount_minor,ref,at}]
    paid           BOOLEAN NOT NULL DEFAULT FALSE,
    currency       TEXT NOT NULL DEFAULT 'INR',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_bills_restaurant ON bills (restaurant_id, created_at);
CREATE INDEX idx_bills_open ON bills (restaurant_id) WHERE paid = FALSE;

-- tabs: the EVENT-DRIVEN billing-board read model, one row per occupied table.
-- Maintained by the consumers (order.placed adds running total + counts;
-- raised(bill) marks asked; bill.opened attaches a bill -> bill_ready;
-- bill.finalized removes the row). Keyed by (restaurant_id, table_no).
CREATE TABLE tabs (
    restaurant_id    TEXT NOT NULL,
    table_no         INTEGER NOT NULL,
    order_count      INTEGER NOT NULL DEFAULT 0,
    item_count       INTEGER NOT NULL DEFAULT 0,
    running_minor    BIGINT NOT NULL DEFAULT 0,
    asked            BOOLEAN NOT NULL DEFAULT FALSE,
    bill_id          TEXT,                                 -- set once a bill is opened
    bill_total_minor BIGINT NOT NULL DEFAULT 0,
    currency         TEXT NOT NULL DEFAULT 'INR',
    PRIMARY KEY (restaurant_id, table_no)
);
CREATE INDEX idx_tabs_restaurant ON tabs (restaurant_id, table_no);

-- processed_events for idempotent choreography (dedupe consumed events by id).
-- Written in the SAME tx as the projection write so a redelivery is a no-op.
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
    source       TEXT NOT NULL DEFAULT 'billing',
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    data         JSONB NOT NULL,
    published_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished ON outbox (occurred_at) WHERE published_at IS NULL;

-- Row-Level Security: scope every tenant table to app.tenant_id (the restaurant).
ALTER TABLE bills            ENABLE ROW LEVEL SECURITY;
ALTER TABLE tabs             ENABLE ROW LEVEL SECURITY;
ALTER TABLE processed_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox           ENABLE ROW LEVEL SECURITY;

ALTER TABLE bills            FORCE ROW LEVEL SECURITY;
ALTER TABLE tabs             FORCE ROW LEVEL SECURITY;
ALTER TABLE processed_events FORCE ROW LEVEL SECURITY;
ALTER TABLE outbox           FORCE ROW LEVEL SECURITY;

CREATE POLICY bills_tenant_isolation ON bills
    USING (restaurant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (restaurant_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY tabs_tenant_isolation ON tabs
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
DROP TABLE IF EXISTS tabs;
DROP TABLE IF EXISTS bills;
-- +goose StatementEnd
