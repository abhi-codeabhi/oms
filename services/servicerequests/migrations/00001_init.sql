-- +goose Up
-- +goose StatementBegin

-- Service requests: a guest-raised "call waiter / water / bill / cutlery" tied to
-- a table, scoped to one restaurant (the tenant key). Lifecycle state assigned ->
-- escalated -> done. RLS scopes every row to current_setting('app.tenant_id'),
-- set per transaction by pkg/pg.WithTenant from the JWT-derived restaurant.
CREATE TABLE requests (
    id            TEXT PRIMARY KEY,
    restaurant_id TEXT NOT NULL,
    type          TEXT NOT NULL,                 -- call|water|bill|cutlery
    table_no      INTEGER NOT NULL,
    state         TEXT NOT NULL,                 -- assigned|escalated|done
    assigned_to   TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    acked_at      TIMESTAMPTZ                    -- NULL until acknowledged
);
-- Open-request lookups + escalation sweeps scan by restaurant; created_at orders.
CREATE INDEX idx_requests_restaurant ON requests (restaurant_id, created_at);
CREATE INDEX idx_requests_state ON requests (restaurant_id, state);

-- Cooldowns: the last acknowledge time per table+type. A raise of the same
-- table+type within the cooldown window (settings: floor.call.cooldown_secs) is
-- rejected (FailedPrecondition). One row per (restaurant, table, type); upserted
-- on every acknowledge.
CREATE TABLE cooldowns (
    restaurant_id TEXT NOT NULL,
    table_no      INTEGER NOT NULL,
    type          TEXT NOT NULL,
    acked_at      TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (restaurant_id, table_no, type)
);

-- Transactional outbox (pkg/outbox.Stage writes here; a relay drains to NATS).
CREATE TABLE outbox (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    tenant_id    TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT 'servicerequests',
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    data         JSONB NOT NULL,
    published_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished ON outbox (occurred_at) WHERE published_at IS NULL;

-- Row-Level Security: scope every tenant table to app.tenant_id (the restaurant).
ALTER TABLE requests  ENABLE ROW LEVEL SECURITY;
ALTER TABLE cooldowns ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox    ENABLE ROW LEVEL SECURITY;

ALTER TABLE requests  FORCE ROW LEVEL SECURITY;
ALTER TABLE cooldowns FORCE ROW LEVEL SECURITY;
ALTER TABLE outbox    FORCE ROW LEVEL SECURITY;

CREATE POLICY requests_tenant_isolation ON requests
    USING (restaurant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (restaurant_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY cooldowns_tenant_isolation ON cooldowns
    USING (restaurant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (restaurant_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY outbox_tenant_isolation ON outbox
    USING (tenant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', TRUE));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS cooldowns;
DROP TABLE IF EXISTS requests;
-- +goose StatementEnd
