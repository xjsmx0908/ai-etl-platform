-- The retention purge must not be gated on status='expired'.
--
-- A review row only reaches status='expired' through ExpireDueReviews, which
-- walks release_center_reviews by joining the request that currently points at
-- the row (q.review_id = rv.review_id). A review that is superseded before its
-- TTL elapses -- the request is repointed at a newer review, which is exactly
-- what happens when a transient Agent failure is followed by a successful
-- re-review -- is therefore never marked expired, and a purge gated on that
-- status could never delete it. RELEASE_REVIEW_RETENTION silently did not apply
-- to those rows.
--
-- The purge now keys off expires_at plus "no request references this row",
-- which is the invariant the retention window was always meant to express. The
-- partial index has to cover that predicate instead of the old one.
DROP INDEX IF EXISTS release_center_reviews_purge_idx;

CREATE INDEX IF NOT EXISTS release_center_reviews_purge_idx
    ON release_center_reviews (expires_at)
    WHERE expires_at IS NOT NULL;
