-- +goose Up
-- +goose StatementBegin

-- Catalog: brand menu (categories + items) with per-outlet overrides. Every row
-- carries restaurant_id as the tenant key. RLS scopes reads/writes to
-- current_setting('app.tenant_id') which pkg/pg.WithTenant sets per transaction
-- from the JWT-derived outlet (restaurant_id).

CREATE TABLE categories (
    id            TEXT PRIMARY KEY,
    restaurant_id TEXT NOT NULL,
    name          TEXT NOT NULL,
    sort          INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_categories_restaurant ON categories (restaurant_id);

CREATE TABLE items (
    id                 TEXT PRIMARY KEY,
    restaurant_id      TEXT NOT NULL,
    category_id        TEXT NOT NULL DEFAULT '',
    name               TEXT NOT NULL,
    description        TEXT NOT NULL DEFAULT '',
    price_minor        BIGINT NOT NULL,
    currency           TEXT NOT NULL DEFAULT 'INR',
    veg                BOOLEAN NOT NULL DEFAULT FALSE,
    tags               JSONB NOT NULL DEFAULT '{}'::jsonb,  -- dietary flags 0/1
    prep_minutes       INTEGER NOT NULL DEFAULT 0,
    station            TEXT NOT NULL DEFAULT '',            -- grill|tandoor|cold
    available          BOOLEAN NOT NULL DEFAULT TRUE,       -- brand default availability
    image_id           TEXT,
    image_url          TEXT,
    image_content_type TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_items_restaurant ON items (restaurant_id);
CREATE INDEX idx_items_category ON items (category_id);

-- Per-outlet override of a brand item's price/availability. has_avail marks
-- whether `available` is meaningful (86 sets it true). Unique per (item, outlet).
CREATE TABLE item_overrides (
    item_id       TEXT NOT NULL,
    restaurant_id TEXT NOT NULL,
    price_minor   BIGINT,            -- NULL = inherit brand price
    currency      TEXT,
    available     BOOLEAN NOT NULL DEFAULT TRUE,
    has_avail     BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (item_id, restaurant_id)
);
CREATE INDEX idx_item_overrides_restaurant ON item_overrides (restaurant_id);

-- Transactional outbox (pkg/outbox.Stage writes here; a relay drains to NATS).
CREATE TABLE outbox (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    tenant_id    TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT 'catalog',
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    data         JSONB NOT NULL,
    published_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished ON outbox (occurred_at) WHERE published_at IS NULL;

-- Row-Level Security: scope every tenant table to app.tenant_id (the outlet).
ALTER TABLE categories     ENABLE ROW LEVEL SECURITY;
ALTER TABLE items          ENABLE ROW LEVEL SECURITY;
ALTER TABLE item_overrides ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox         ENABLE ROW LEVEL SECURITY;

ALTER TABLE categories     FORCE ROW LEVEL SECURITY;
ALTER TABLE items          FORCE ROW LEVEL SECURITY;
ALTER TABLE item_overrides FORCE ROW LEVEL SECURITY;
ALTER TABLE outbox         FORCE ROW LEVEL SECURITY;

CREATE POLICY categories_tenant_isolation ON categories
    USING (restaurant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (restaurant_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY items_tenant_isolation ON items
    USING (restaurant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (restaurant_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY item_overrides_tenant_isolation ON item_overrides
    USING (restaurant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (restaurant_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY outbox_tenant_isolation ON outbox
    USING (tenant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', TRUE));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS item_overrides;
DROP TABLE IF EXISTS items;
DROP TABLE IF EXISTS categories;
-- +goose StatementEnd
