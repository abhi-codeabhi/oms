-- +goose Up
-- +goose StatementBegin

-- Transactional outbox (pkg/outbox). Domain events are inserted here in the SAME
-- transaction as the staff change; the relay (pkg/outbox.Relay) drains
-- unpublished rows to NATS. This is an internal infra table (no RLS) read by the
-- trusted relay across tenants; tenant_id is retained on the envelope for routing.
CREATE TABLE IF NOT EXISTS outbox (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    tenant_id    TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT '',
    occurred_at  TIMESTAMPTZ NOT NULL,
    data         JSONB NOT NULL,
    published_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS outbox_unpublished_idx ON outbox (occurred_at) WHERE published_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS outbox;
-- +goose StatementEnd
