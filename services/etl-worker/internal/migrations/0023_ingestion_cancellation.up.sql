ALTER TABLE ingestion_jobs DROP CONSTRAINT IF EXISTS ingestion_jobs_status_check;
ALTER TABLE ingestion_jobs ADD CONSTRAINT ingestion_jobs_status_check
    CHECK (status IN ('queued','published','processing','completed','failed','cancelled'));

ALTER TABLE documents DROP CONSTRAINT IF EXISTS documents_status_check;
ALTER TABLE documents ADD CONSTRAINT documents_status_check
    CHECK (status IN ('queued','processing','completed','failed','cancelled'));
