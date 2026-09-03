-- Durable admission records the authoritative job and the exact task snapshot
-- before Kafka publication. The outbox is intentionally append-oriented: a
-- relay may publish an event more than once, while consumers deduplicate by
-- event_id/job_id.
CREATE TABLE ingestion_jobs (
    job_id       TEXT PRIMARY KEY,
    event_id     TEXT NOT NULL UNIQUE,
    tenant_id    TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    doc_id            TEXT NOT NULL,
    request_signature TEXT NOT NULL,
    task              JSONB NOT NULL,
    status       TEXT NOT NULL DEFAULT 'queued'
                 CHECK (status IN ('queued','published','completed','failed','cancelled')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, doc_id) REFERENCES documents(tenant_id, doc_id) ON DELETE CASCADE
);
CREATE INDEX ingestion_jobs_tenant_doc_idx ON ingestion_jobs (tenant_id, doc_id);

CREATE TABLE ingestion_outbox (
    event_id      TEXT PRIMARY KEY,
    job_id        TEXT NOT NULL REFERENCES ingestion_jobs(job_id) ON DELETE CASCADE,
    tenant_id     TEXT NOT NULL,
    doc_id        TEXT NOT NULL,
    task          JSONB NOT NULL,
    attempts      INT NOT NULL DEFAULT 0,
    available_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_at    TIMESTAMPTZ,
    published_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ingestion_outbox_pending_idx
    ON ingestion_outbox (available_at, created_at)
    WHERE published_at IS NULL;
