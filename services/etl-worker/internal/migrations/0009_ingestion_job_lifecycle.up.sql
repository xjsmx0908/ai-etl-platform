-- The worker claims durable jobs with a renewable processing lease. A terminal
-- status makes repeated Kafka delivery safe to acknowledge without replaying
-- parsing, embedding, or index writes.
ALTER TABLE ingestion_jobs
    DROP CONSTRAINT ingestion_jobs_status_check,
    ADD CONSTRAINT ingestion_jobs_status_check
        CHECK (status IN ('queued','published','processing','completed','failed')),
    ADD COLUMN processing_started_at TIMESTAMPTZ,
    ADD COLUMN lease_until           TIMESTAMPTZ,
    ADD COLUMN completed_at          TIMESTAMPTZ,
    ADD COLUMN error                 TEXT NOT NULL DEFAULT '';

CREATE INDEX ingestion_jobs_recoverable_idx
    ON ingestion_jobs (lease_until)
    WHERE status = 'processing';
