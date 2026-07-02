-- +goose Up
-- +goose StatementBegin

-- plans: the catalog of quota/feature bundles. Global control-plane data (not
-- owner-scoped), so no RLS policy — every tenant reads the same plans.
CREATE TABLE plans (
    id         TEXT PRIMARY KEY,                 -- "free" | "growth" | "pro" | "enterprise" | custom
    name       TEXT NOT NULL DEFAULT '',
    quotas     JSONB NOT NULL DEFAULT '{}',      -- {"outlets": 3, "staff.waiter": 10, ...}  -1 = unlimited
    features   JSONB NOT NULL DEFAULT '{}',      -- {"aggregators": true, ...}
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- entitlements: an owner's plan assignment + per-owner overrides. Owner-scoped
-- (RLS by owner_id == app.tenant_id).
CREATE TABLE entitlements (
    owner_id          TEXT PRIMARY KEY,
    plan_id           TEXT NOT NULL REFERENCES plans(id),
    quota_overrides   JSONB NOT NULL DEFAULT '{}',
    feature_overrides JSONB NOT NULL DEFAULT '{}',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- usage_counters: the authoritative current usage per (owner, key). Reserve/
-- Release mutate `used` under FOR UPDATE so concurrent reservers serialise.
CREATE TABLE usage_counters (
    owner_id   TEXT NOT NULL,
    key        TEXT NOT NULL,                    -- "outlets", "staff.waiter", ...
    used       BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_id, key),
    CONSTRAINT usage_nonneg CHECK (used >= 0)
);

-- reservations: the idempotency ledger. A reservation_id appears at most once;
-- replays are detected by its presence. The stored delta is authoritative on
-- release so a mismatched caller delta cannot corrupt the counter.
CREATE TABLE reservations (
    reservation_id TEXT PRIMARY KEY,
    owner_id       TEXT NOT NULL,
    key            TEXT NOT NULL,
    delta          BIGINT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX reservations_owner_key_idx ON reservations (owner_id, key);

-- ---- Row-Level Security (owner-scoped tables) ----------------------------
-- pkg/pg.WithTenant runs SELECT set_config('app.tenant_id', $owner, true) before
-- queries; these policies scope rows to that owner. Platform admins connect with
-- the table owner / BYPASSRLS role for cross-tenant seeding & admin.

ALTER TABLE entitlements ENABLE ROW LEVEL SECURITY;
ALTER TABLE entitlements FORCE ROW LEVEL SECURITY;
CREATE POLICY entitlements_tenant_isolation ON entitlements
    USING (owner_id = current_setting('app.tenant_id', true))
    WITH CHECK (owner_id = current_setting('app.tenant_id', true));

ALTER TABLE usage_counters ENABLE ROW LEVEL SECURITY;
ALTER TABLE usage_counters FORCE ROW LEVEL SECURITY;
CREATE POLICY usage_counters_tenant_isolation ON usage_counters
    USING (owner_id = current_setting('app.tenant_id', true))
    WITH CHECK (owner_id = current_setting('app.tenant_id', true));

ALTER TABLE reservations ENABLE ROW LEVEL SECURITY;
ALTER TABLE reservations FORCE ROW LEVEL SECURITY;
CREATE POLICY reservations_tenant_isolation ON reservations
    USING (owner_id = current_setting('app.tenant_id', true))
    WITH CHECK (owner_id = current_setting('app.tenant_id', true));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS reservations;
DROP TABLE IF EXISTS usage_counters;
DROP TABLE IF EXISTS entitlements;
DROP TABLE IF EXISTS plans;
-- +goose StatementEnd
