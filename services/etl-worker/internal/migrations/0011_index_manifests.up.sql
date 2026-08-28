-- A document version is the durable admitted ingestion job. This composite key
-- lets manifests prove that tenant/document/version identity belongs together.
CREATE UNIQUE INDEX ingestion_jobs_version_identity_key
    ON ingestion_jobs (tenant_id, doc_id, job_id);

CREATE TABLE index_manifests (
    generation_id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    document_id TEXT NOT NULL,
    document_version_id TEXT NOT NULL,
    chunker_version TEXT NOT NULL CHECK (chunker_version <> ''),
    embedding_model TEXT NOT NULL CHECK (embedding_model <> ''),
    vector_dimension INT NOT NULL CHECK (vector_dimension > 0),
    schema_version TEXT NOT NULL CHECK (schema_version <> ''),
    collection_version TEXT NOT NULL CHECK (collection_version <> ''),
    index_version TEXT NOT NULL CHECK (index_version <> ''),
    expected_chunk_count INT NOT NULL CHECK (expected_chunk_count >= 0),
    expected_chunk_digest TEXT NOT NULL CHECK (expected_chunk_digest <> ''),
    qdrant_count INT CHECK (qdrant_count >= 0),
    qdrant_digest TEXT,
    qdrant_observed_at TIMESTAMPTZ,
    elasticsearch_count INT CHECK (elasticsearch_count >= 0),
    elasticsearch_digest TEXT,
    elasticsearch_observed_at TIMESTAMPTZ,
    state TEXT NOT NULL DEFAULT 'building'
          CHECK (state IN ('building','ready','failed','active','retired')),
    attempts INT NOT NULL DEFAULT 1 CHECK (attempts > 0),
    last_error TEXT NOT NULL DEFAULT '',
    last_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    verified_at TIMESTAMPTZ,
    activated_at TIMESTAMPTZ,
    FOREIGN KEY (tenant_id, document_id, document_version_id)
        REFERENCES ingestion_jobs (tenant_id, doc_id, job_id) ON DELETE CASCADE,
    UNIQUE (tenant_id, document_version_id, generation_id)
);

CREATE UNIQUE INDEX index_manifests_one_active
    ON index_manifests (tenant_id, document_version_id) WHERE state = 'active';
CREATE INDEX index_manifests_reconcile_idx ON index_manifests (state, verified_at);
