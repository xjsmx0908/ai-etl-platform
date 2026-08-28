CREATE TABLE index_manifests (
    generation_id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    document_id TEXT NOT NULL,
    document_version_id TEXT NOT NULL,
    chunker_version TEXT NOT NULL DEFAULT '',
    embedding_model TEXT NOT NULL DEFAULT '',
    vector_dimension INT NOT NULL DEFAULT 0,
    schema_version TEXT NOT NULL DEFAULT '',
    collection_version TEXT NOT NULL DEFAULT '',
    index_version TEXT NOT NULL DEFAULT '',
    expected_chunk_count INT NOT NULL CHECK (expected_chunk_count >= 0),
    expected_chunk_digest TEXT NOT NULL,
    qdrant_count INT NOT NULL DEFAULT 0,
    qdrant_digest TEXT NOT NULL DEFAULT '',
    elasticsearch_count INT NOT NULL DEFAULT 0,
    elasticsearch_digest TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'building' CHECK (state IN ('building','ready','failed','active','retired')),
    attempts INT NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    last_attempt_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    verified_at TIMESTAMPTZ,
    activated_at TIMESTAMPTZ,
    UNIQUE (tenant_id, document_version_id, generation_id)
);
CREATE UNIQUE INDEX index_manifests_one_active
    ON index_manifests (tenant_id, document_version_id) WHERE state = 'active';
CREATE INDEX index_manifests_reconcile_idx ON index_manifests (state, verified_at);
