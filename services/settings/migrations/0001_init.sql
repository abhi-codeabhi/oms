-- +goose Up
-- +goose StatementBegin

-- definitions: the GLOBAL catalog of configurable keys. Not owner-scoped — every
-- tenant sees the same definitions — so NO RLS policy. Services self-register via
-- RegisterDefinitions (idempotent upsert by key).
CREATE TABLE definitions (
    key           TEXT PRIMARY KEY,             -- dotted namespace, e.g. "billing.gst_pct"
    title         TEXT NOT NULL DEFAULT '',
    description   TEXT NOT NULL DEFAULT '',
    type          INT  NOT NULL,                -- ValueType enum (1=INT 2=BOOL 3=STRING 4=DECIMAL 5=JSON 6=ENUM)
    default_type  INT  NOT NULL DEFAULT 0,      -- ValueType of the default value
    default_raw   TEXT NOT NULL DEFAULT '',     -- canonical string form of the default
    max_scope     INT  NOT NULL,                -- Scope enum (1=OWNER 2=BRAND 3=RESTAURANT) — deepest overridable level
    enum_options  TEXT[] NOT NULL DEFAULT '{}', -- allowed values when type=ENUM
    validation    TEXT NOT NULL DEFAULT '',     -- "min:0,max:100"
    editable_by   TEXT NOT NULL DEFAULT 'platform_admin', -- role ladder floor
    feature_gated BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- overrides: per-tenant values set at owner / brand / restaurant scope. Owner-
-- scoped (RLS by owner_id == app.tenant_id). The unused tenant columns are stored
-- as '' (empty string), never NULL, so the unique key + ON CONFLICT are total.
CREATE TABLE overrides (
    key           TEXT NOT NULL,
    owner_id      TEXT NOT NULL,
    brand_id      TEXT NOT NULL DEFAULT '',     -- set when scope >= BRAND
    restaurant_id TEXT NOT NULL DEFAULT '',     -- set when scope == RESTAURANT
    scope         INT  NOT NULL,                -- Scope enum (1=OWNER 2=BRAND 3=RESTAURANT)
    value_type    INT  NOT NULL,
    value_raw     TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (key, owner_id, brand_id, restaurant_id)
);
CREATE INDEX idx_overrides_owner ON overrides (owner_id);
CREATE INDEX idx_overrides_owner_brand ON overrides (owner_id, brand_id);
CREATE INDEX idx_overrides_owner_restaurant ON overrides (owner_id, restaurant_id);

-- Transactional outbox (pkg/outbox.Stage writes here; a relay drains to NATS).
CREATE TABLE outbox (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    tenant_id    TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT 'settings',
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    data         JSONB NOT NULL,
    published_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished ON outbox (occurred_at) WHERE published_at IS NULL;

-- ---- Row-Level Security (owner-scoped tables only) ------------------------
-- pkg/pg.WithTenant runs SELECT set_config('app.tenant_id', $owner, true) before
-- queries; these policies scope rows to that owner. definitions has NO policy (it
-- is global control-plane data). Platform admins connect with the BYPASSRLS role
-- for cross-tenant seeding & admin.

ALTER TABLE overrides ENABLE ROW LEVEL SECURITY;
ALTER TABLE overrides FORCE  ROW LEVEL SECURITY;
CREATE POLICY overrides_tenant_isolation ON overrides
    USING (owner_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (owner_id = current_setting('app.tenant_id', TRUE));

ALTER TABLE outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox FORCE  ROW LEVEL SECURITY;
CREATE POLICY outbox_tenant_isolation ON outbox
    USING (tenant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', TRUE));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS overrides;
DROP TABLE IF EXISTS definitions;
-- +goose StatementEnd
