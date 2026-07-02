-- +goose Up
-- +goose StatementBegin

-- Promotions: coupons (percent|flat discounts) configured per outlet. Every row
-- carries restaurant_id as the tenant key, and coupons are keyed by code WITHIN a
-- restaurant. RLS scopes reads/writes to current_setting('app.tenant_id') which
-- pkg/pg.WithTenant sets per transaction from the JWT-derived outlet (restaurant_id).

CREATE TABLE coupons (
    restaurant_id   TEXT NOT NULL,
    code            TEXT NOT NULL,                  -- upper-cased; unique per outlet
    type            TEXT NOT NULL,                  -- 'percent' | 'flat'
    value           BIGINT NOT NULL,                -- percent (0-100) or flat minor units
    min_order_minor BIGINT NOT NULL DEFAULT 0,      -- minimum subtotal before applying
    currency        TEXT NOT NULL DEFAULT 'INR',
    category        TEXT NOT NULL DEFAULT '',        -- optional category restriction ('' = any)
    active          BOOLEAN NOT NULL DEFAULT TRUE,
    starts_at       TIMESTAMPTZ,                    -- optional window start (NULL = no lower bound)
    ends_at         TIMESTAMPTZ,                    -- optional window end (NULL = no expiry)
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (restaurant_id, code)
);
CREATE INDEX idx_coupons_restaurant ON coupons (restaurant_id);

-- Transactional outbox (pkg/outbox.Stage writes here; a relay drains to NATS).
CREATE TABLE outbox (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    tenant_id    TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT 'promotions',
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    data         JSONB NOT NULL,
    published_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished ON outbox (occurred_at) WHERE published_at IS NULL;

-- Row-Level Security: scope every tenant table to app.tenant_id (the outlet).
ALTER TABLE coupons ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox  ENABLE ROW LEVEL SECURITY;

ALTER TABLE coupons FORCE ROW LEVEL SECURITY;
ALTER TABLE outbox  FORCE ROW LEVEL SECURITY;

CREATE POLICY coupons_tenant_isolation ON coupons
    USING (restaurant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (restaurant_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY outbox_tenant_isolation ON outbox
    USING (tenant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', TRUE));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS coupons;
-- +goose StatementEnd
