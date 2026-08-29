-- Persist the generation definition before streaming backend writes. Expected
-- chunk identity is sealed once parsing completes.
ALTER TABLE index_manifests ALTER COLUMN expected_chunk_count DROP NOT NULL;
ALTER TABLE index_manifests ALTER COLUMN expected_chunk_digest DROP NOT NULL;

-- Bind activation to the predecessor observed when this generation first
-- started. A delayed retry cannot adopt a newer winner and overwrite it.
ALTER TABLE index_manifests ADD COLUMN expected_active_generation_id TEXT NOT NULL DEFAULT '';

ALTER TABLE index_manifests ADD CONSTRAINT index_manifests_expected_identity_pair
    CHECK ((expected_chunk_count IS NULL AND expected_chunk_digest IS NULL)
        OR (expected_chunk_count IS NOT NULL AND expected_chunk_digest IS NOT NULL));
