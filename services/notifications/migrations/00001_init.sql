-- +goose Up
-- +goose StatementBegin

-- Notifications data plane: messages (outbound notifications + delivery lifecycle),
-- templates (owner/brand-configurable copy, per channel) and processed_events (event
-- dedupe for idempotent delivery-status webhooks). Every tenant row carries owner_id
-- as the tenant key; RLS scopes reads/writes to current_setting('app.tenant_id')
-- which pkg/pg.WithTenant sets per transaction from the JWT-derived owner.

CREATE TABLE messages (
    id               TEXT PRIMARY KEY,
    owner_id         TEXT NOT NULL,
    restaurant_id    TEXT,
    channel          TEXT NOT NULL,
    recipient        TEXT NOT NULL,
    template_id      TEXT NOT NULL,
    vars             JSONB NOT NULL DEFAULT '{}'::jsonb,
    subject          TEXT NOT NULL DEFAULT '',
    body             TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL DEFAULT 'queued',
    provider_id      TEXT,
    provider_ref     TEXT,
    idempotency_key  TEXT,
    error            TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_messages_owner ON messages (owner_id);
-- Idempotency: at most one message per (owner, key) so Send never double-dispatches.
CREATE UNIQUE INDEX idx_messages_idem ON messages (owner_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL;
-- Delivery-status webhooks correlate on (provider_id, provider_ref).
CREATE INDEX idx_messages_provider_ref ON messages (provider_id, provider_ref)
    WHERE provider_ref IS NOT NULL;

CREATE TABLE templates (
    owner_id    TEXT NOT NULL,
    id          TEXT NOT NULL,
    channel     TEXT NOT NULL,
    subject     TEXT NOT NULL DEFAULT '',
    body        TEXT NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (owner_id, id)
);

-- Event dedupe for delivery-status webhooks (exactly-once effect). Not tenant-scoped
-- (a provider callback isn't tied to a JWT); the empty-tenant path writes it.
CREATE TABLE processed_events (
    event_id     TEXT PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Transactional outbox (pkg/outbox.Stage writes here; a relay drains to NATS).
CREATE TABLE outbox (
    id           TEXT PRIMARY KEY,
    type         TEXT NOT NULL,
    tenant_id    TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT 'notifications',
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    data         JSONB NOT NULL,
    published_at TIMESTAMPTZ
);
CREATE INDEX idx_outbox_unpublished ON outbox (occurred_at) WHERE published_at IS NULL;

-- Row-Level Security: scope tenant tables to app.tenant_id (the owner). The empty
-- tenant (webhook / relay context) matches the platform default owner + NULL setting
-- for the cross-tenant provider-ref lookup.
ALTER TABLE messages  ENABLE ROW LEVEL SECURITY;
ALTER TABLE templates ENABLE ROW LEVEL SECURITY;
ALTER TABLE outbox    ENABLE ROW LEVEL SECURITY;

ALTER TABLE messages  FORCE ROW LEVEL SECURITY;
ALTER TABLE templates FORCE ROW LEVEL SECURITY;
ALTER TABLE outbox    FORCE ROW LEVEL SECURITY;

-- messages/outbox: the empty tenant setting ('') sees all rows so the delivery-status
-- webhook path (FindByProviderRef) and outbox relay can operate cross-tenant, while a
-- concrete tenant is scoped to its own rows.
CREATE POLICY messages_tenant_isolation ON messages
    USING (current_setting('app.tenant_id', TRUE) IS NULL
        OR current_setting('app.tenant_id', TRUE) = ''
        OR owner_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (owner_id = current_setting('app.tenant_id', TRUE)
        OR current_setting('app.tenant_id', TRUE) = '');

CREATE POLICY templates_tenant_isolation ON templates
    USING (owner_id = current_setting('app.tenant_id', TRUE)
        OR current_setting('app.tenant_id', TRUE) = '')
    WITH CHECK (owner_id = current_setting('app.tenant_id', TRUE)
        OR current_setting('app.tenant_id', TRUE) = '');

CREATE POLICY outbox_tenant_isolation ON outbox
    USING (current_setting('app.tenant_id', TRUE) IS NULL
        OR current_setting('app.tenant_id', TRUE) = ''
        OR tenant_id = current_setting('app.tenant_id', TRUE))
    WITH CHECK (TRUE);

-- Built-in platform default templates so identity OTP + staff invites work out of the
-- box (owner overrides shadow these; see repo.GetTemplate fallback).
INSERT INTO templates (owner_id, id, channel, subject, body) VALUES
    ('own_platform', 'otp',           'sms',   '',                  'Your Restorna verification code is {{code}}. It expires in {{ttl}} minutes.'),
    ('own_platform', 'otp',           'email', 'Your Restorna code', 'Your verification code is {{code}}. It expires in {{ttl}} minutes.'),
    ('own_platform', 'staff_invite',  'sms',   '',                  'You have been invited to join {{brand}} on Restorna. Accept: {{link}}'),
    ('own_platform', 'staff_invite',  'email', 'You are invited to {{brand}}', 'Hi {{name}}, {{inviter}} invited you to join {{brand}} on Restorna. Accept here: {{link}}'),
    ('own_platform', 'receipt',       'email', 'Your receipt from {{restaurant}}', 'Thanks for dining at {{restaurant}}. Your bill total was {{amount}}. {{link}}');

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS outbox;
DROP TABLE IF EXISTS processed_events;
DROP TABLE IF EXISTS templates;
DROP TABLE IF EXISTS messages;
-- +goose StatementEnd
