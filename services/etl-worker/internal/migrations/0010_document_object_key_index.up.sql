-- Orphan collection proves that a versioned object is absent from the current
-- document catalog before deletion. Keep that repeated lookup index-backed.
CREATE INDEX documents_object_key_idx
    ON documents (object_key)
    WHERE object_key <> '';

-- Every admitted job keeps its immutable object protected. Terminal versions
-- are retained until a later generation-retention policy explicitly owns them.
CREATE INDEX ingestion_jobs_object_key_idx
    ON ingestion_jobs ((task->>'file_path'));
