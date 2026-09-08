-- Durable, tenant-scoped notification outbox for Knowledge Release Center
-- and other governance events. Payloads are JSON objects of low-sensitivity
-- identifiers only; document content, findings, prompts, and secrets must
-- never be written here.
CREATE TABLE governance_notification_outbox (
    event_id     TEXT PRIMARY KEY,
    dedupe_key   TEXT NOT NULL UNIQUE,
    tenant_id    TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    source       TEXT NOT NULL,
    source_id    TEXT NOT NULL,
    event_type   TEXT NOT NULL,
    payload      JSONB NOT NULL,
    attempts     INT NOT NULL DEFAULT 0,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_at   TIMESTAMPTZ,
    published_at TIMESTAMPTZ,
    last_error   TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT governance_notification_payload_object
        CHECK (jsonb_typeof(payload) = 'object')
);

CREATE INDEX governance_notification_outbox_pending_idx
    ON governance_notification_outbox (available_at, created_at)
    WHERE published_at IS NULL;

CREATE INDEX governance_notification_outbox_tenant_idx
    ON governance_notification_outbox (tenant_id, created_at DESC);
