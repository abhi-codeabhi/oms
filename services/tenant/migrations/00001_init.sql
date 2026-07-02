-- +goose Up
-- +goose StatementBegin

-- Tenant hierarchy: owners -> brands -> restaurants. Every row carries owner_id as
-- the tenant key. RLS scopes reads/writes to current_setting('app.tenant_id') which
-- pkg/pg.WithTenant sets per transaction from the JWT-derived owner.

CREATE TABLE owners (
    id          TEXT PRIMARY KEY,
    owner_id    TEXT NOT NULL,            -- equals id; present so the RLS policy is uniform
    name        TEXT NOT NULL,
    legal_name  TEXT NOT NULL,
    country     TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE brands (
    id                TEXT PRIMARY KEY,
    owner_id          TEXT NOT NULL,
    name              TEXT NOT NULL,
    primary_color     TEXT NOT NULL DEFAULT '',
    logo_id           TEXT,
    logo_url          TEXT,
    logo_content_type TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_brands_owner ON brands (owner_id);

CREATE TABLE restaurants (
    id                TEXT PRIMARY KEY,
    brand_id          TEXT NOT NULL REFERENCES brands (id),
    owner_id          TEXT NOT NULL,
    name              TEXT NOT NULL,
    address           TEXT NOT NULL DEFAULT '',
    timezone          TEXT NOT NULL DEFAULT 'Asia/Kolkata',
    gstin             TEXT NOT NULL DEFAULT '',
    logo_id           TEXT,
    logo_url          TEXT,
    logo_content_type TEXT,
    active            BOOLEAN NOT NULL DEFAULT TRUE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_restaurants_brand ON restaurants (brand_id);
CREATE INDEX idx_restaurants_owner ON restaurants (owner_id);

-- Transactional outbox (pkg/outbox.Stage writes here; a relay drains to NATS).
CREATE TABLE outbox (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    tenant_id    TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT 'tenant',
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    data         JSONB NOT NULL,
    published_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished ON outbox (occurred_at) WHERE published_at IS NULL;

-- Row-Level Security: scope every tenant table to app.tenant_id (the owner).
ALTER TABLE owners       ENABLE ROW LEVEL SECURITY;
ALTER TABLE brands       ENABLE ROW LEVEL SECURITY;
ALTER TABLE restaurants  ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox       ENABLE ROW LEVEL SECURITY;

ALTER TABLE owners      FORCE ROW LEVEL SECURITY;
ALTER TABLE brands      FORCE ROW LEVEL SECURITY;
ALTER TABLE restaurants FORCE ROW LEVEL SECURITY;
ALTER TABLE outbox      FORCE ROW LEVEL SECURITY;

CREATE POLICY owners_tenant_isolation ON owners
    USING (owner_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (owner_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY brands_tenant_isolation ON brands
    USING (owner_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (owner_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY restaurants_tenant_isolation ON restaurants
    USING (owner_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (owner_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY outbox_tenant_isolation ON outbox
    USING (tenant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', TRUE));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS restaurants;
DROP TABLE IF EXISTS brands;
DROP TABLE IF EXISTS owners;
-- +goose StatementEnd
