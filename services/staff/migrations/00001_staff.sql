-- +goose Up
-- +goose StatementBegin

-- staff_members is the per-outlet roster. Multi-tenant by owner_id/restaurant_id
-- with Postgres Row-Level Security scoped by app.tenant_id (set per transaction
-- by pkg/pg.WithTenant from the JWT-derived owner id).
CREATE TABLE IF NOT EXISTS staff_members (
    id            TEXT PRIMARY KEY,
    owner_id      TEXT NOT NULL,
    brand_id      TEXT NOT NULL DEFAULT '',
    restaurant_id TEXT NOT NULL,
    name          TEXT NOT NULL,
    email         TEXT NOT NULL DEFAULT '',
    phone         TEXT NOT NULL DEFAULT '',
    role          INTEGER NOT NULL,
    active        BOOLEAN NOT NULL DEFAULT TRUE,
    user_id       TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS staff_members_restaurant_idx ON staff_members (restaurant_id);
CREATE INDEX IF NOT EXISTS staff_members_owner_idx ON staff_members (owner_id);

-- Enforce tenant isolation at the database. The owner id is the tenant key;
-- app.tenant_id is set per transaction. FORCE so the table owner is not exempt.
ALTER TABLE staff_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE staff_members FORCE ROW LEVEL SECURITY;

CREATE POLICY staff_members_tenant_isolation ON staff_members
    USING (owner_id = current_setting('app.tenant_id', true))
    WITH CHECK (owner_id = current_setting('app.tenant_id', true));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS staff_members;
-- +goose StatementEnd
