-- +goose Up
-- Rotating refresh tokens, stored by SHA-256 hash (never plaintext). The role +
-- tenant scope are persisted so a Refresh re-mints an equivalent access token.
-- Cross-tenant table, no RLS; the optional scope columns capture which tenant a
-- scoped token targeted.
CREATE TABLE IF NOT EXISTS refresh_tokens (
    id            TEXT PRIMARY KEY,          -- rft_...
    user_id       TEXT NOT NULL,             -- usr_... or cst_... (customer)
    token_hash    TEXT NOT NULL UNIQUE,      -- sha256(raw token)
    role          INT  NOT NULL,             -- common.v1.Role
    owner_id      TEXT,
    brand_id      TEXT,
    restaurant_id TEXT,
    expires_at    TIMESTAMPTZ NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked       BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS refresh_tokens_user_idx    ON refresh_tokens (user_id);
CREATE INDEX IF NOT EXISTS refresh_tokens_expires_idx ON refresh_tokens (expires_at);

-- +goose Down
DROP TABLE IF EXISTS refresh_tokens;
