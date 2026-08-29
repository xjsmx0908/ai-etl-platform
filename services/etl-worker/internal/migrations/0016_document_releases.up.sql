-- One PostgreSQL row owns the current admitted version and the single release
-- governance has approved for query visibility. Legacy publication state is
-- backfilled only when both the source object and active generation identify
-- one exact durable ingestion version.
CREATE TABLE document_releases (
    tenant_id TEXT NOT NULL,
    document_id TEXT NOT NULL,
    current_version_id TEXT,
    published_version_id TEXT,
    published_generation_id TEXT,
    revision BIGINT NOT NULL DEFAULT 1 CHECK (revision > 0),
    resolution_status TEXT NOT NULL DEFAULT 'unresolved'
        CHECK (resolution_status IN ('resolved','unresolved')),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, document_id),
    FOREIGN KEY (tenant_id, document_id)
        REFERENCES documents (tenant_id, doc_id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_id, document_id, current_version_id)
        REFERENCES ingestion_jobs (tenant_id, doc_id, job_id),
    FOREIGN KEY (tenant_id, document_id, published_version_id)
        REFERENCES ingestion_jobs (tenant_id, doc_id, job_id),
    FOREIGN KEY (tenant_id, published_version_id, published_generation_id)
        REFERENCES index_manifests (tenant_id, document_version_id, generation_id),
    CONSTRAINT document_releases_published_identity_pair CHECK (
        (published_version_id IS NULL) = (published_generation_id IS NULL)
    ),
    CONSTRAINT document_releases_resolved_current CHECK (
        resolution_status <> 'resolved' OR current_version_id IS NOT NULL
    )
);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM documents d
        JOIN ingestion_jobs j
          ON j.tenant_id=d.tenant_id AND j.doc_id=d.doc_id
         AND j.task->>'file_path'=d.object_key
        GROUP BY d.tenant_id,d.doc_id
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'ambiguous current document version';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM documents d
        JOIN index_manifests m
          ON m.tenant_id=d.tenant_id AND m.document_id=d.doc_id
         AND m.state='active'
        WHERE d.publication_status='published'
        GROUP BY d.tenant_id,d.doc_id
        HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'ambiguous published generation';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM documents d
        LEFT JOIN ingestion_jobs j
          ON j.tenant_id=d.tenant_id AND j.doc_id=d.doc_id
         AND j.task->>'file_path'=d.object_key
        LEFT JOIN index_manifests m
          ON m.tenant_id=d.tenant_id AND m.document_id=d.doc_id
         AND m.state='active'
        WHERE d.publication_status='published'
        GROUP BY d.tenant_id,d.doc_id
        HAVING count(j.job_id) = 1 AND count(m.generation_id) = 1
           AND max(j.job_id) IS DISTINCT FROM max(m.document_version_id)
    ) THEN
        RAISE EXCEPTION 'ambiguous published generation';
    END IF;
END $$;

INSERT INTO document_releases (
    tenant_id,document_id,current_version_id,
    published_version_id,published_generation_id,
    resolution_status,last_error
)
SELECT d.tenant_id,d.doc_id,current_job.job_id,
       CASE WHEN d.publication_status='published'
                  AND current_job.job_id=active_manifest.document_version_id
            THEN active_manifest.document_version_id END,
       CASE WHEN d.publication_status='published'
                  AND current_job.job_id=active_manifest.document_version_id
            THEN active_manifest.generation_id END,
       CASE
           WHEN current_job.job_id IS NOT NULL
            AND (d.publication_status <> 'published'
                 OR current_job.job_id=active_manifest.document_version_id)
           THEN 'resolved' ELSE 'unresolved'
       END,
       CASE
           WHEN d.publication_status='published'
            AND (current_job.job_id IS NULL
                 OR active_manifest.document_version_id IS NULL
                 OR current_job.job_id IS DISTINCT FROM active_manifest.document_version_id)
           THEN 'legacy published identity unavailable'
           WHEN current_job.job_id IS NULL THEN 'legacy version identity unavailable'
           ELSE ''
       END
FROM documents d
LEFT JOIN LATERAL (
    SELECT j.job_id
    FROM ingestion_jobs j
    WHERE j.tenant_id=d.tenant_id AND j.doc_id=d.doc_id
      AND j.task->>'file_path'=d.object_key
    LIMIT 1
) current_job ON TRUE
LEFT JOIN LATERAL (
    SELECT m.document_version_id,m.generation_id
    FROM index_manifests m
    WHERE m.tenant_id=d.tenant_id AND m.document_id=d.doc_id
      AND m.state='active'
    LIMIT 1
) active_manifest ON TRUE;

CREATE INDEX document_releases_published_lookup_idx
    ON document_releases (tenant_id, document_id, published_version_id, published_generation_id)
    WHERE published_version_id IS NOT NULL;
