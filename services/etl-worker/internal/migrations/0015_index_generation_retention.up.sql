ALTER TABLE index_manifests
    ADD COLUMN retired_at TIMESTAMPTZ,
    ADD COLUMN retention_lease_until TIMESTAMPTZ,
    ADD COLUMN retention_claim_token TEXT NOT NULL DEFAULT '',
    ADD COLUMN qdrant_deleted_at TIMESTAMPTZ,
    ADD COLUMN elasticsearch_deleted_at TIMESTAMPTZ,
    ADD COLUMN retention_attempts INT NOT NULL DEFAULT 0 CHECK (retention_attempts >= 0),
    ADD COLUMN retention_last_error TEXT NOT NULL DEFAULT '';

-- Existing retired rows predate explicit rollback windows. Start their window
-- at migration time rather than treating an old activation timestamp as an
-- immediately-expired retirement.
UPDATE index_manifests SET retired_at=now() WHERE state='retired';

CREATE INDEX index_manifests_retention_claim_idx
    ON index_manifests (retired_at, created_at)
    WHERE state='retired';
