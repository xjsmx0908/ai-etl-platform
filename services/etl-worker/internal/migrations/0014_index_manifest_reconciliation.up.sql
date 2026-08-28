ALTER TABLE index_manifests
    ADD COLUMN last_reconciled_at TIMESTAMPTZ,
    ADD COLUMN reconcile_lease_until TIMESTAMPTZ,
    ADD COLUMN reconcile_claim_token TEXT NOT NULL DEFAULT '',
    ADD COLUMN repair_attempts INT NOT NULL DEFAULT 0 CHECK (repair_attempts >= 0),
    ADD COLUMN last_reconcile_error TEXT NOT NULL DEFAULT '';

CREATE INDEX index_manifests_reconciliation_claim_idx
    ON index_manifests (last_reconciled_at NULLS FIRST, created_at)
    WHERE state = 'active';
