-- +goose Up
-- Pending OTP verifications. TTL via expires_at; attempt limit via attempts.
-- Cross-tenant table (scoped by realm + address), no RLS.
CREATE TABLE IF NOT EXISTS otp_challenges (
    id          TEXT PRIMARY KEY,            -- otp_...
    channel     INT  NOT NULL,               -- Channel enum (1=EMAIL, 2=PHONE)
    address     TEXT NOT NULL,
    realm       INT  NOT NULL,               -- Realm enum
    code        TEXT NOT NULL,               -- expected code (delivered out of band)
    attempts    INT  NOT NULL DEFAULT 0,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    consumed    BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS otp_challenges_expires_idx ON otp_challenges (expires_at);
CREATE INDEX IF NOT EXISTS otp_challenges_addr_idx    ON otp_challenges (realm, address);

-- +goose Down
DROP TABLE IF EXISTS otp_challenges;
