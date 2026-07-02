-- +goose Up
-- +goose StatementBegin

-- onboarding_states is the saga ledger: one row per owner being onboarded. It
-- records which steps have completed and the ids each step produced so a retried
-- RPC is idempotent and a crashed saga is resumable. Multi-tenant by owner_id
-- with Postgres Row-Level Security scoped by app.tenant_id (set per transaction
-- by pkg/pg.WithTenant from the JWT-derived / saga owner id).
CREATE TABLE IF NOT EXISTS onboarding_states (
    id          TEXT PRIMARY KEY,            -- onb_...
    owner_id    TEXT NOT NULL,               -- own_... (tenant key, set at ACCOUNT)
    user_id     TEXT NOT NULL DEFAULT '',    -- usr_... owner login
    brand_id    TEXT NOT NULL DEFAULT '',    -- brnd_...
    logo_url    TEXT NOT NULL DEFAULT '',
    outlet_id   TEXT NOT NULL DEFAULT '',    -- out_...
    plan_id     TEXT NOT NULL DEFAULT '',
    completed   INTEGER[] NOT NULL DEFAULT '{}',  -- ordered Step enum values
    done        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS onboarding_states_owner_idx ON onboarding_states (owner_id);

-- Enforce tenant isolation at the database. owner_id is the tenant key;
-- app.tenant_id is set per transaction. FORCE so the table owner is not exempt.
--
-- Onboarding is a control-plane saga driven by the platform console: a saga is
-- looked up by its onb_ id BEFORE the owner is necessarily in the caller's scope,
-- so the policy scopes rows by owner_id WHEN app.tenant_id is set, and otherwise
-- (unset/empty) permits the trusted platform/relay path. Once an owner is in
-- scope they see only their own saga.
ALTER TABLE onboarding_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE onboarding_states FORCE ROW LEVEL SECURITY;

CREATE POLICY onboarding_states_tenant_isolation ON onboarding_states
    USING (
        coalesce(current_setting('app.tenant_id', true), '') = ''
        OR owner_id = current_setting('app.tenant_id', true)
    )
    WITH CHECK (
        coalesce(current_setting('app.tenant_id', true), '') = ''
        OR owner_id = current_setting('app.tenant_id', true)
    );

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS onboarding_states;
-- +goose StatementEnd
