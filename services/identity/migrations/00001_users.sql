-- +goose Up
-- Identity is a CROSS-TENANT control-plane service: users are NOT bound to an
-- owner/brand/restaurant. No tenant_id column, no RLS — rows are partitioned
-- logically by realm (1=PLATFORM, 2=TENANT). Tenant scope is attached only at
-- token issuance time, never stored on the user.
CREATE TABLE IF NOT EXISTS users (
    id           TEXT PRIMARY KEY,            -- usr_...
    email        TEXT,
    phone        TEXT,
    display_name TEXT NOT NULL DEFAULT '',
    realm        INT  NOT NULL,               -- Realm enum
    active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A given address (email or phone) is unique within a realm.
CREATE UNIQUE INDEX IF NOT EXISTS users_realm_email_uq
    ON users (realm, email) WHERE email IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS users_realm_phone_uq
    ON users (realm, phone) WHERE phone IS NOT NULL;

-- +goose Down
DROP TABLE IF EXISTS users;
