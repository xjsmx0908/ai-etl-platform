-- Records which release a publication replaced, so that "what changed since the
-- last published version" is answerable instead of guessed.
--
-- Neither existing source can stand in for it. `Publish` overwrites
-- published_version_id in place, so the replaced value is gone the moment the
-- new one lands; and `index_manifests` cannot be read as a history of
-- publications because `Activate` retires generations that were never published
-- (it is the rebuild path, not the release path), while `Publish` retires
-- across versions. Both kinds of retired row sit in the same table, so
-- "the retired generation" does not mean "the previous release".
--
-- The two columns age differently, which is why they are not collapsed into one:
--   * previous_version_id is a historical fact. It survives the generation being
--     reclaimed, so a diff can still name the version it could not compare
--     against rather than reporting "no previous version".
--   * previous_generation_id points at data that can actually be reclaimed.
--     Retention deletes a retired manifest row once both projections are gone
--     (indexmanifest.FinishRetention), and a plain foreign key would make that
--     delete fail, so this one is ON DELETE SET NULL.
ALTER TABLE document_releases
    ADD COLUMN previous_version_id TEXT,
    ADD COLUMN previous_generation_id TEXT;

-- A generation without a version would be unnameable in a report, and the pair is
-- written together, so one implies the other. The reverse is deliberately not
-- enforced: retention nulls the generation while the version stays.
ALTER TABLE document_releases
    ADD CONSTRAINT document_releases_previous_version_requires_generation
    CHECK (previous_generation_id IS NULL OR previous_version_id IS NOT NULL);

ALTER TABLE document_releases
    ADD CONSTRAINT document_releases_previous_generation_fk
    FOREIGN KEY (previous_generation_id)
    REFERENCES index_manifests (generation_id)
    ON DELETE SET NULL;

-- The referencing side of a foreign key is not indexed automatically, and every
-- reclaimed generation triggers this one.
CREATE INDEX document_releases_previous_generation_idx
    ON document_releases (previous_generation_id)
    WHERE previous_generation_id IS NOT NULL;
