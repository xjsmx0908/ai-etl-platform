ALTER TABLE knowledge_spaces
    ADD COLUMN IF NOT EXISTS purpose TEXT NOT NULL DEFAULT '';

ALTER TABLE release_center_reviews
    ADD COLUMN IF NOT EXISTS space_fit TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS knowledge_usable TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS kind_label TEXT NOT NULL DEFAULT '';

ALTER TABLE release_center_reviews
    DROP CONSTRAINT IF EXISTS release_center_reviews_space_fit_check;
ALTER TABLE release_center_reviews
    ADD CONSTRAINT release_center_reviews_space_fit_check
    CHECK (space_fit IN ('', 'match', 'mismatch', 'uncertain'));

ALTER TABLE release_center_reviews
    DROP CONSTRAINT IF EXISTS release_center_reviews_knowledge_usable_check;
ALTER TABLE release_center_reviews
    ADD CONSTRAINT release_center_reviews_knowledge_usable_check
    CHECK (knowledge_usable IN ('', 'usable', 'not_knowledge', 'incomplete'));
