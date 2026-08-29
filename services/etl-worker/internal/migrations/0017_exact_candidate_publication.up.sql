-- The last successful governance command makes response-loss retries
-- distinguishable from a stale approval. The key is tenant-scoped and may be
-- used for only one document release.
ALTER TABLE document_releases
    ADD COLUMN last_publication_idempotency_key TEXT NOT NULL DEFAULT '',
    ADD COLUMN last_publication_request_hash TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX document_releases_publication_idempotency_key
    ON document_releases (tenant_id, last_publication_idempotency_key)
    WHERE last_publication_idempotency_key <> '';
