-- Query retrieval resolves a bounded batch of candidate documents by tenant.
-- Keep the read gate indexed independently of lifecycle/reconciliation scans.
CREATE INDEX index_manifests_visibility_lookup_idx
    ON index_manifests (tenant_id, document_id);
