ALTER TABLE documents
    ADD COLUMN deletion_status TEXT NOT NULL DEFAULT 'active'
        CHECK (deletion_status IN ('active','pending'));

CREATE TABLE document_deletion_jobs (
    job_id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    document_id TEXT NOT NULL,
    object_prefix TEXT NOT NULL,
    object_keys TEXT[] NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','processing')),
    attempts INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_until TIMESTAMPTZ,
    claim_token TEXT NOT NULL DEFAULT '',
    qdrant_deleted_at TIMESTAMPTZ,
    elasticsearch_deleted_at TIMESTAMPTZ,
    objects_deleted_at TIMESTAMPTZ,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, document_id),
    FOREIGN KEY (tenant_id, document_id)
        REFERENCES documents (tenant_id, doc_id) ON DELETE CASCADE
);

CREATE INDEX document_deletion_jobs_claim_idx
    ON document_deletion_jobs (available_at, created_at)
    WHERE state='pending';
