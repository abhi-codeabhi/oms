-- +goose Up
-- +goose StatementBegin

-- Ordering data plane: orders -> order_lines, plus the transactional outbox. Every
-- row carries restaurant_id as the tenant key. RLS scopes reads/writes to
-- current_setting('app.tenant_id') which pkg/pg.WithTenant sets per transaction
-- from the JWT-derived restaurant id. Money is integer minor units + currency.

CREATE TABLE orders (
    id             TEXT PRIMARY KEY,
    restaurant_id  TEXT NOT NULL,
    table_id       TEXT NOT NULL,
    subtotal_minor BIGINT NOT NULL DEFAULT 0,
    currency       TEXT NOT NULL DEFAULT 'INR',
    billed         BOOLEAN NOT NULL DEFAULT FALSE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_orders_restaurant ON orders (restaurant_id);
CREATE INDEX idx_orders_table ON orders (restaurant_id, table_id);
CREATE INDEX idx_orders_unbilled ON orders (restaurant_id) WHERE billed = FALSE;

CREATE TABLE order_lines (
    id               TEXT PRIMARY KEY,
    order_id         TEXT NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    restaurant_id    TEXT NOT NULL,
    menu_item_id     TEXT NOT NULL,
    name             TEXT NOT NULL DEFAULT '',
    qty              INTEGER NOT NULL,
    unit_price_minor BIGINT NOT NULL DEFAULT 0,
    currency         TEXT NOT NULL DEFAULT 'INR',
    station          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_order_lines_order ON order_lines (order_id);

-- Transactional outbox (pkg/outbox.Stage writes here; a relay drains to NATS).
CREATE TABLE outbox (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    tenant_id    TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT 'ordering',
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    data         JSONB NOT NULL,
    published_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished ON outbox (occurred_at) WHERE published_at IS NULL;

-- Row-Level Security: scope every tenant table to app.tenant_id (the restaurant).
ALTER TABLE orders      ENABLE ROW LEVEL SECURITY;
ALTER TABLE order_lines ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox      ENABLE ROW LEVEL SECURITY;

ALTER TABLE orders      FORCE ROW LEVEL SECURITY;
ALTER TABLE order_lines FORCE ROW LEVEL SECURITY;
ALTER TABLE outbox      FORCE ROW LEVEL SECURITY;

CREATE POLICY orders_tenant_isolation ON orders
    USING (restaurant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (restaurant_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY order_lines_tenant_isolation ON order_lines
    USING (restaurant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (restaurant_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY outbox_tenant_isolation ON outbox
    USING (tenant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', TRUE));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS order_lines;
DROP TABLE IF EXISTS orders;
-- +goose StatementEnd
