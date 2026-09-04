-- Durable business records for managed-document pre-review and approval.
-- Agent runs remain an execution/audit correlation; these rows own the
-- long-lived release-center state and are bound to an exact candidate.
CREATE TABLE release_center_reviews (
    review_id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    document_id TEXT NOT NULL,
    document_version_id TEXT NOT NULL,
    generation_id TEXT NOT NULL,
    release_revision BIGINT NOT NULL CHECK (release_revision > 0),
    agent_run_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('completed','failed','expired')),
    recommendation TEXT NOT NULL CHECK (recommendation <> ''),
    risk_level TEXT NOT NULL CHECK (risk_level IN ('low','medium','high','critical')),
    summary TEXT NOT NULL DEFAULT '',
    findings JSONB NOT NULL DEFAULT '[]'::jsonb,
    model TEXT NOT NULL DEFAULT '',
    prompt_version TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ,
    FOREIGN KEY (tenant_id, document_id)
        REFERENCES documents (tenant_id, doc_id) ON DELETE CASCADE,
    UNIQUE (tenant_id, review_id)
);

CREATE INDEX release_center_reviews_candidate_idx
    ON release_center_reviews (tenant_id, document_id, document_version_id,
                               generation_id, release_revision, created_at DESC);

CREATE TABLE release_center_requests (
    request_id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    document_id TEXT NOT NULL,
    document_version_id TEXT NOT NULL,
    generation_id TEXT NOT NULL,
    expected_chunk_count INT NOT NULL CHECK (expected_chunk_count > 0),
    expected_chunk_digest TEXT NOT NULL CHECK (expected_chunk_digest <> ''),
    release_revision BIGINT NOT NULL CHECK (release_revision > 0),
    review_id TEXT NOT NULL,
    required_approvals INT NOT NULL CHECK (required_approvals BETWEEN 1 AND 2),
    state TEXT NOT NULL CHECK (state IN ('approval_pending','manual_exception','needs_info','rejected','published')),
    requested_by TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, document_id)
        REFERENCES documents (tenant_id, doc_id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, review_id)
        REFERENCES release_center_reviews (tenant_id, review_id),
    FOREIGN KEY (tenant_id, document_version_id, generation_id)
        REFERENCES index_manifests (tenant_id, document_version_id, generation_id),
    UNIQUE (tenant_id, request_id),
    UNIQUE (tenant_id, document_id, document_version_id, generation_id, release_revision)
);

CREATE INDEX release_center_requests_queue_idx
    ON release_center_requests (tenant_id, state, updated_at DESC);
CREATE INDEX release_center_requests_document_idx
    ON release_center_requests (tenant_id, document_id, updated_at DESC);

CREATE TABLE release_center_decisions (
    decision_id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    request_id TEXT NOT NULL,
    decided_by TEXT NOT NULL,
    decision TEXT NOT NULL CHECK (decision IN ('approved','rejected')),
    reason TEXT NOT NULL DEFAULT '',
    decided_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, request_id)
        REFERENCES release_center_requests (tenant_id, request_id) ON DELETE CASCADE,
    UNIQUE (tenant_id, request_id, decided_by)
);

CREATE INDEX release_center_decisions_request_idx
    ON release_center_decisions (tenant_id, request_id, decided_at);
