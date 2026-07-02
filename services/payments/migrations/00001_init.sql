-- +goose Up
-- +goose StatementBegin

-- Payments: one online-money attempt per bill, orchestrated over a resolved
-- connector. Tenant key is restaurant_id (payments are per-outlet). Money is always
-- integer minor units + currency. RLS scopes reads/writes to
-- current_setting('app.tenant_id') which pkg/pg.WithTenant sets per transaction
-- from the JWT-derived restaurant (or the trusted event envelope for webhooks).

CREATE TABLE payments (
    id              TEXT PRIMARY KEY,          -- pay_...
    restaurant_id   TEXT NOT NULL,             -- tenant key
    bill_id         TEXT NOT NULL,
    amount_minor    BIGINT NOT NULL,           -- minor units (paise)
    currency        TEXT NOT NULL,
    connector_id    TEXT NOT NULL,             -- resolved provider (razorpay|paytm|phonepe|mock)
    provider_ref    TEXT NOT NULL DEFAULT '',  -- gateway order/txn id (webhook match key)
    status          TEXT NOT NULL,             -- CREATED|PENDING|CAPTURED|FAILED|REFUNDED
    method          TEXT NOT NULL DEFAULT '',  -- upi|card|wallet|netbanking
    refunded_minor  BIGINT NOT NULL DEFAULT 0,
    idempotency_key TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Idempotency: one payment per (restaurant, idempotency_key). A repeat CreateIntent
-- with the same key returns the existing payment (no double charge).
CREATE UNIQUE INDEX idx_payments_idem ON payments (restaurant_id, idempotency_key);
-- Webhooks match by the gateway ref; unique so a normalized event resolves 1 row.
CREATE UNIQUE INDEX idx_payments_provider_ref ON payments (provider_ref)
    WHERE provider_ref <> '';
CREATE INDEX idx_payments_bill ON payments (bill_id);
CREATE INDEX idx_payments_restaurant ON payments (restaurant_id);

-- Processed events for idempotent webhook choreography (dedupe the connector-hub
-- payment webhook by its event id). Written in the SAME tx as the status flip so a
-- redelivery is a no-op (exactly-once effect).
CREATE TABLE processed_events (
    event_id      TEXT PRIMARY KEY,
    restaurant_id TEXT NOT NULL,
    processed_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Transactional outbox (pkg/outbox.Stage writes here; a relay drains to NATS).
CREATE TABLE outbox (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    tenant_id    TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT 'payments',
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    data         JSONB NOT NULL,
    published_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished ON outbox (occurred_at) WHERE published_at IS NULL;

-- Row-Level Security: scope every tenant table to app.tenant_id (the restaurant).
ALTER TABLE payments         ENABLE ROW LEVEL SECURITY;
ALTER TABLE processed_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox           ENABLE ROW LEVEL SECURITY;

ALTER TABLE payments         FORCE ROW LEVEL SECURITY;
ALTER TABLE processed_events FORCE ROW LEVEL SECURITY;
ALTER TABLE outbox           FORCE ROW LEVEL SECURITY;

CREATE POLICY payments_tenant_isolation ON payments
    USING (restaurant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (restaurant_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY processed_events_tenant_isolation ON processed_events
    USING (restaurant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (restaurant_id = current_setting('app.tenant_id', TRUE));

CREATE POLICY outbox_tenant_isolation ON outbox
    USING (tenant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', TRUE));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS processed_events;
DROP TABLE IF EXISTS payments;
-- +goose StatementEnd
