-- +goose Up
-- +goose StatementBegin

-- Connector installations: a connector configured for a tenant scope. Every row
-- carries owner_id as the tenant key. RLS scopes reads/writes to
-- current_setting('app.tenant_id') which pkg/pg.WithTenant sets per transaction
-- from the JWT-derived owner.
--
-- Secret config values are held ENCRYPTED (envelope encryption / AES-256-GCM under
-- the CONNECTOR_KEK) in the secret_config BYTEA column — the DB never sees
-- plaintext credentials. Non-secret config is stored as JSONB in public_config and
-- is the only part ever echoed back to clients.

CREATE TABLE installations (
    id             TEXT PRIMARY KEY,
    owner_id       TEXT NOT NULL,
    restaurant_id  TEXT,                          -- NULL = owner/brand-level install
    connector_id   TEXT NOT NULL,                 -- "razorpay" | "zomato" | ...
    enabled        BOOLEAN NOT NULL DEFAULT TRUE,
    test_mode      BOOLEAN NOT NULL DEFAULT FALSE,
    public_config  JSONB NOT NULL DEFAULT '{}'::jsonb,  -- non-secret keys (echoed)
    secret_config  BYTEA,                          -- AES-GCM ciphertext (never echoed)
    installed_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_installations_owner ON installations (owner_id);
CREATE INDEX idx_installations_connector ON installations (owner_id, connector_id);

-- One installation of a connector per (owner, restaurant scope): keeps the
-- "connectors" quota correct and Resolve deterministic. coalesce collapses NULL
-- restaurant_id to '' so owner-level installs are unique too.
CREATE UNIQUE INDEX uq_installations_scope
    ON installations (owner_id, connector_id, coalesce(restaurant_id, ''));

-- Transactional outbox (pkg/outbox.Stage writes here; a relay drains to NATS).
CREATE TABLE outbox (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    tenant_id    TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT 'connectorhub',
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    data         JSONB NOT NULL,
    published_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished ON outbox (occurred_at) WHERE published_at IS NULL;

-- Row-Level Security: scope every tenant table to app.tenant_id (the owner).
ALTER TABLE installations ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox        ENABLE ROW LEVEL SECURITY;
ALTER TABLE installations FORCE ROW LEVEL SECURITY;
ALTER TABLE outbox        FORCE ROW LEVEL SECURITY;

CREATE POLICY installations_tenant_isolation ON installations
    USING (owner_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (owner_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY outbox_tenant_isolation ON outbox
    USING (tenant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', TRUE));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS installations;
-- +goose StatementEnd
