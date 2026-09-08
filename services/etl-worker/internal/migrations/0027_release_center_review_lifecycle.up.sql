-- Indexes for review-report expiry, automatic rereview pickup, and retention
-- cleanup. The reviews table already allows status=expired and expires_at.
CREATE INDEX IF NOT EXISTS release_center_reviews_due_idx
    ON release_center_reviews (expires_at)
    WHERE expires_at IS NOT NULL AND status IN ('completed','failed');

CREATE INDEX IF NOT EXISTS release_center_reviews_purge_idx
    ON release_center_reviews (expires_at)
    WHERE status = 'expired';
