-- Controlled-document governance fields. NOTE: doc_status is the *lifecycle* of
-- the document as a knowledge source (is it still authoritative?), which is a
-- different axis from the existing `status` column (the ETL processing state:
-- queued/processing/completed/failed). Do not conflate the two.
--
-- Retrieval filters superseded/archived documents out of the candidate set after
-- search, because chunk payloads in Qdrant/ES are written once at ingest time and
-- are never updated in place — marking a document obsolete must not require
-- re-indexing it.
ALTER TABLE documents
    ADD COLUMN doc_status     TEXT NOT NULL DEFAULT 'active'
                              CHECK (doc_status IN ('active','superseded','archived')),
    -- When this version takes effect. NULL means "not tracked"; used to order
    -- two documents covering the same topic when disclosing a conflict.
    ADD COLUMN effective_date DATE,
    -- doc_id of the document this one replaces (same tenant). Empty = replaces
    -- nothing. Two documents in the same result set linked by this chain are
    -- reported as a conflict rather than silently ranked by similarity.
    ADD COLUMN supersedes     TEXT NOT NULL DEFAULT '',
    -- Accountable owner (business role or user), distinct from uploaded_by which
    -- records who performed the upload.
    ADD COLUMN owner          TEXT NOT NULL DEFAULT '';

CREATE INDEX documents_tenant_docstatus_idx ON documents (tenant_id, doc_status);

-- Exact-duplicate detection on upload. Deliberately NOT unique: the same bytes
-- may legitimately exist under several doc_ids (e.g. a shared appendix), so this
-- index supports detection, not a hard constraint.
CREATE INDEX documents_tenant_hash_idx ON documents (tenant_id, file_hash);
