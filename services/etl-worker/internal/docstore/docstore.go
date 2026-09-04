// Package docstore persists the document metadata registry in PostgreSQL. One
// row per (tenant_id, doc_id). It is written through by the upload path and the
// worker (status updates) so the registry is the durable inventory that survives
// the 7-day Redis task-status TTL. Qdrant/ES/MinIO remain the search and object
// stores; the registry is metadata only.
package docstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"ai-etl-pipeline/internal/db"
)

// Status values mirror model.TaskStatus so the registry can be filtered the same
// way task status is.
const (
	StatusQueued     = "queued"
	StatusProcessing = "processing"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	StatusCancelled  = "cancelled"
)

// DocStatus values describe a document's lifecycle as a knowledge source. This
// is a different axis from Status above (the ETL processing state): a document
// can be Status=completed and DocStatus=superseded, meaning it ingested fine but
// must no longer be used as evidence.
const (
	DocStatusActive     = "active"
	DocStatusSuperseded = "superseded"
	DocStatusArchived   = "archived"
)

// ErrNotFound is returned by Get when no row matches (tenant, doc_id).
var ErrNotFound = errors.New("docstore: not found")

// Document is one row of the documents registry.
type Document struct {
	TenantID    string
	DocID       string
	FileName    string
	ObjectKey   string
	FileHash    string
	FileSize    int64
	ContentType string
	Permission  string
	Status      string
	Stage       string
	ChunksDone  int
	ChunksTotal int
	Error       string
	Metadata    map[string]string
	UploadedBy  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt time.Time

	// Controlled-document governance (migration 0004).
	DocStatus         string    // active | superseded | archived
	EffectiveDate     time.Time // zero = not tracked
	Supersedes        string    // doc_id this version replaces; empty = none
	Owner             string    // accountable owner, distinct from UploadedBy
	KnowledgeSpaceID  string    // governed tenant-local knowledge space
	PublicationStatus string    // draft | published | retired
	DeletionStatus    string    // active | pending
}

// Governance is the subset of a document's governance state that retrieval needs
// to decide whether a candidate may still be used as evidence, and whether two
// candidates disagree. Kept separate from Document so the post-retrieval lookup
// stays a narrow projection rather than a full row fetch per candidate.
type Governance struct {
	DocID         string
	DocStatus     string
	EffectiveDate time.Time
	Supersedes    string
	FileName      string
}

// IsRetired reports whether a document must be excluded from answer evidence.
// An empty DocStatus is treated as active so rows predating migration 0004 (and
// stores that do not track governance) keep working unchanged.
func (g Governance) IsRetired() bool {
	return g.DocStatus == DocStatusSuperseded || g.DocStatus == DocStatusArchived
}

// ListQuery filters document listing.
type ListQuery struct {
	TenantID          string
	Status            string
	Permissions       []string // empty = no permission filter; else IN (...) set
	KnowledgeSpaceIDs []string
	Search            string // matches file_name or doc_id (ILIKE)
	Limit             int
	Offset            int
}

// Store is the persistence surface for the document registry.
type Store interface {
	Upsert(ctx context.Context, d Document) error
	Get(ctx context.Context, tenantID, docID string) (Document, bool, error)
	Delete(ctx context.Context, tenantID, docID string) error
	List(ctx context.Context, q ListQuery) ([]Document, int, error)
	UpsertStatus(ctx context.Context, tenantID, docID string, d Document) error
	ReconcileUpsert(ctx context.Context, tenantID, docID, permission string) error
	// GetByHash returns an existing document with identical content, used by the
	// upload path to detect exact duplicates instead of indexing the same bytes
	// under a second doc_id (which would pollute the retrieval candidate set).
	GetByHash(ctx context.Context, tenantID, knowledgeSpaceID, fileHash string) (Document, bool, error)
	// GovernanceByDocIDs batch-loads governance state for retrieval candidates.
	// Missing doc_ids are simply absent from the map.
	GovernanceByDocIDs(ctx context.Context, tenantID string, docIDs []string) (map[string]Governance, error)
}

// GovernanceUpdater edits controlled-document metadata without touching the
// uploaded object, ETL state, or publication release.
type GovernanceUpdater interface {
	UpdateGovernance(context.Context, string, string, string, time.Time, string, string) error
}

// PgStore implements Store on PostgreSQL.
type PgStore struct {
	q db.Querier
}

// New returns a Store backed by the given querier.
func New(q db.Querier) *PgStore {
	return &PgStore{q: q}
}

var _ Store = (*PgStore)(nil)

// Upsert inserts a document row, or replaces it on conflict of (tenant_id,
// doc_id). A re-upload with the same doc_id is an update, matching the
// upload-upsert replacement semantics.
func (s *PgStore) Upsert(ctx context.Context, d Document) error {
	_, err := s.q.Exec(ctx, `
		INSERT INTO documents (
			tenant_id, doc_id, file_name, object_key, file_hash, file_size,
			content_type, permission, status, stage, chunks_done, chunks_total,
			error, metadata, uploaded_by, created_at, updated_at, completed_at,
			doc_status, effective_date, supersedes, owner,
			knowledge_space_id, publication_status
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,
			COALESCE(NULLIF($19,''),'active'),$20,$21,$22,
			COALESCE(NULLIF($23,''),'user-uploads'),COALESCE(NULLIF($24,''),'draft'))
		ON CONFLICT (tenant_id, doc_id) DO UPDATE SET
			file_name = EXCLUDED.file_name,
			object_key = EXCLUDED.object_key,
			file_hash = EXCLUDED.file_hash,
			file_size = EXCLUDED.file_size,
			content_type = EXCLUDED.content_type,
			permission = EXCLUDED.permission,
			status = EXCLUDED.status,
			stage = EXCLUDED.stage,
			chunks_done = EXCLUDED.chunks_done,
			chunks_total = EXCLUDED.chunks_total,
			error = EXCLUDED.error,
			metadata = EXCLUDED.metadata,
			uploaded_by = EXCLUDED.uploaded_by,
			updated_at = now(),
			completed_at = EXCLUDED.completed_at,
			-- Governance fields are only overwritten when the caller supplied one,
			-- so a plain re-upload cannot silently wipe an owner or an effective
			-- date set through the governance path.
			doc_status = CASE WHEN $19::text = '' THEN documents.doc_status ELSE $19::text END,
			effective_date = COALESCE($20::date, documents.effective_date),
			supersedes = CASE WHEN $21::text = '' THEN documents.supersedes ELSE $21::text END,
			owner = CASE WHEN $22::text = '' THEN documents.owner ELSE $22::text END,
			knowledge_space_id = CASE WHEN $23::text = '' THEN documents.knowledge_space_id ELSE $23::text END,
			publication_status = CASE WHEN $24::text = '' THEN documents.publication_status ELSE $24::text END`,
		d.TenantID, d.DocID, d.FileName, d.ObjectKey, d.FileHash, d.FileSize,
		d.ContentType, d.Permission, d.Status, d.Stage, d.ChunksDone, d.ChunksTotal,
		d.Error, d.Metadata, d.UploadedBy, d.CreatedAt, d.UpdatedAt, d.CompletedAt,
		d.DocStatus, dateParam(d.EffectiveDate), d.Supersedes, d.Owner,
		d.KnowledgeSpaceID, d.PublicationStatus,
	)
	if err != nil {
		return fmt.Errorf("upsert document: %w", err)
	}
	return nil
}

// Get returns the registry row for (tenant, doc_id).
func (s *PgStore) Get(ctx context.Context, tenantID, docID string) (Document, bool, error) {
	row := s.q.QueryRow(ctx,
		"SELECT "+documentColumns+" FROM documents WHERE tenant_id=$1 AND doc_id=$2",
		tenantID, docID)
	d, found, err := scanDocument(row)
	if err != nil {
		return Document{}, false, err
	}
	if !found {
		return Document{}, false, nil
	}
	return d, true, nil
}

// GetByHash returns the newest document with the same content hash, if any. Only
// completed documents count as duplicates: a queued or failed row may never
// produce searchable chunks, so treating it as the canonical copy would silently
// drop the upload.
func (s *PgStore) GetByHash(ctx context.Context, tenantID, knowledgeSpaceID, fileHash string) (Document, bool, error) {
	if strings.TrimSpace(fileHash) == "" {
		return Document{}, false, nil
	}
	row := s.q.QueryRow(ctx,
		"SELECT "+documentColumns+` FROM documents
		 WHERE tenant_id=$1 AND knowledge_space_id=$2 AND file_hash=$3 AND status='completed' AND deletion_status='active'
		 ORDER BY created_at DESC LIMIT 1`, tenantID, knowledgeSpaceID, fileHash)
	return scanDocument(row)
}

// GovernanceByDocIDs loads governance state for the given doc_ids in one query,
// avoiding an N+1 lookup on the query hot path.
func (s *PgStore) GovernanceByDocIDs(ctx context.Context, tenantID string, docIDs []string) (map[string]Governance, error) {
	out := map[string]Governance{}
	if len(docIDs) == 0 {
		return out, nil
	}
	rows, err := s.q.Query(ctx, `
		SELECT doc_id, doc_status, effective_date, supersedes, file_name
		FROM documents WHERE tenant_id=$1 AND doc_id = ANY($2::text[])`,
		tenantID, docIDs)
	if err != nil {
		return nil, fmt.Errorf("load document governance: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var g Governance
		var effective *time.Time
		if err := rows.Scan(&g.DocID, &g.DocStatus, &effective, &g.Supersedes, &g.FileName); err != nil {
			return nil, fmt.Errorf("scan document governance: %w", err)
		}
		if effective != nil {
			g.EffectiveDate = *effective
		}
		out[g.DocID] = g
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate document governance: %w", err)
	}
	return out, nil
}

// Delete removes the registry row for (tenant, doc_id).
func (s *PgStore) Delete(ctx context.Context, tenantID, docID string) error {
	_, err := s.q.Exec(ctx,
		"DELETE FROM documents WHERE tenant_id=$1 AND doc_id=$2", tenantID, docID)
	if err != nil {
		return fmt.Errorf("delete document: %w", err)
	}
	return nil
}

// List returns documents matching the query filters, newest first, plus the
// total count before pagination.
func (s *PgStore) List(ctx context.Context, q ListQuery) ([]Document, int, error) {
	where, args := listWhere(q)

	rows, err := s.q.Query(ctx,
		"SELECT "+documentColumns+" FROM documents "+where+`
		ORDER BY created_at DESC
		LIMIT $`+fmt.Sprint(len(args)+1)+` OFFSET $`+fmt.Sprint(len(args)+2),
		append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list documents: %w", err)
	}
	defer rows.Close()

	docs := []Document{}
	for rows.Next() {
		d, _, err := scanDocument(rows)
		if err != nil {
			return nil, 0, err
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate documents: %w", err)
	}

	var total int
	if err := s.q.QueryRow(ctx,
		"SELECT count(*) FROM documents "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count documents: %w", err)
	}
	return docs, total, nil
}

// ReconcileUpsert backfills a registry row for a document found in Qdrant but
// missing from the registry. It is non-destructive: existing rows are left
// untouched (ON CONFLICT DO NOTHING).
func (s *PgStore) ReconcileUpsert(ctx context.Context, tenantID, docID, permission string) error {
	_, err := s.q.Exec(ctx, `
		INSERT INTO documents (tenant_id, doc_id, permission, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'completed', now(), now())
		ON CONFLICT (tenant_id, doc_id) DO NOTHING`,
		tenantID, docID, permission)
	if err != nil {
		return fmt.Errorf("reconcile upsert: %w", err)
	}
	return nil
}

// UpsertStatus updates only the ingestion-progress columns. Called by the worker
// after each checkpoint so the registry mirrors the durable task status.
func (s *PgStore) UpsertStatus(ctx context.Context, tenantID, docID string, d Document) error {
	_, err := s.q.Exec(ctx, `
		UPDATE documents SET
			status = $3, stage = $4, chunks_done = $5, chunks_total = $6,
			error = $7, updated_at = now(),
			completed_at = CASE WHEN $8::text = '' THEN completed_at ELSE $8::timestamptz END,
			publication_status = CASE
				WHEN $3 = 'completed' AND knowledge_space_id = 'user-uploads' THEN 'published'
				ELSE publication_status
			END
		WHERE tenant_id = $1 AND doc_id = $2`,
		tenantID, docID, d.Status, d.Stage, d.ChunksDone, d.ChunksTotal, d.Error,
		completedAtParam(d.CompletedAt))
	if err != nil {
		return fmt.Errorf("upsert document status: %w", err)
	}
	return nil
}

// UpdatePublication changes evidence eligibility without re-uploading content.
// Publishing is allowed only after successful ingestion and for an active
// controlled document.
func (s *PgStore) UpdatePublication(ctx context.Context, tenantID, docID, status string) error {
	tag, err := s.q.Exec(ctx, `UPDATE documents SET publication_status=$3, updated_at=now()
		WHERE tenant_id=$1 AND doc_id=$2
		  AND ($3 <> 'published' OR (status='completed' AND doc_status='active' AND publication_status='draft'))`,
		tenantID, docID, status)
	if err != nil {
		return fmt.Errorf("update publication: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PgStore) UpdateGovernance(ctx context.Context, tenantID, docID, owner string, effectiveDate time.Time, docStatus, supersedes string) error {
	if docStatus != "" && docStatus != DocStatusActive && docStatus != DocStatusSuperseded && docStatus != DocStatusArchived {
		return ErrNotFound
	}
	tag, err := s.q.Exec(ctx, `UPDATE documents SET owner=CASE WHEN $3<>'' THEN $3 ELSE owner END,effective_date=COALESCE($4::date,effective_date),doc_status=COALESCE(NULLIF($5,''),doc_status),supersedes=CASE WHEN $6<>'' THEN $6 ELSE supersedes END,updated_at=now()
		WHERE tenant_id=$1 AND doc_id=$2`, tenantID, docID, strings.TrimSpace(owner), dateParam(effectiveDate), docStatus, strings.TrimSpace(supersedes))
	if err != nil {
		return fmt.Errorf("update governance: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func listWhere(q ListQuery) (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(cond, len(args)))
	}
	add("tenant_id = $%d", q.TenantID)
	if q.Status != "" {
		add("status = $%d", q.Status)
	}
	if len(q.Permissions) > 0 {
		args = append(args, q.Permissions)
		conds = append(conds, fmt.Sprintf("permission = ANY($%d::text[])", len(args)))
	}
	if len(q.KnowledgeSpaceIDs) > 0 {
		args = append(args, q.KnowledgeSpaceIDs)
		conds = append(conds, fmt.Sprintf("knowledge_space_id = ANY($%d::text[])", len(args)))
	}
	if q.Search != "" {
		args = append(args, "%"+strings.ToLower(q.Search)+"%")
		conds = append(conds, fmt.Sprintf("(lower(file_name) LIKE $%d OR lower(doc_id) LIKE $%d)", len(args), len(args)))
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

// documentColumns is the single column list every full-row read shares, so
// scanDocument's argument order can never drift from one of the queries.
const documentColumns = `tenant_id, doc_id, file_name, object_key, file_hash, file_size,
	content_type, permission, status, stage, chunks_done, chunks_total,
	error, metadata, uploaded_by, created_at, updated_at, completed_at,
	doc_status, effective_date, supersedes, owner, knowledge_space_id, publication_status, deletion_status`

func scanDocument(row pgx.Row) (Document, bool, error) {
	var d Document
	var completedAt, effectiveDate *time.Time
	var metadata map[string]string
	err := row.Scan(&d.TenantID, &d.DocID, &d.FileName, &d.ObjectKey, &d.FileHash, &d.FileSize,
		&d.ContentType, &d.Permission, &d.Status, &d.Stage, &d.ChunksDone, &d.ChunksTotal,
		&d.Error, &metadata, &d.UploadedBy, &d.CreatedAt, &d.UpdatedAt, &completedAt,
		&d.DocStatus, &effectiveDate, &d.Supersedes, &d.Owner, &d.KnowledgeSpaceID, &d.PublicationStatus, &d.DeletionStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return Document{}, false, nil
	}
	if err != nil {
		return Document{}, false, fmt.Errorf("scan document: %w", err)
	}
	d.Metadata = metadata
	if completedAt != nil {
		d.CompletedAt = *completedAt
	}
	if effectiveDate != nil {
		d.EffectiveDate = *effectiveDate
	}
	return d, true, nil
}

// dateParam turns a zero time into a nil DATE parameter so "not tracked" is
// stored as SQL NULL rather than year zero.
func dateParam(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format("2006-01-02")
}

// completedAtParam turns a zero time into an empty string so the SQL CASE treats
// it as "leave unchanged" (worker progress updates must not clobber a completed
// timestamp with a zero value).
func completedAtParam(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
