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
}

// ListQuery filters document listing.
type ListQuery struct {
	TenantID    string
	Status      string
	Permissions []string // empty = no permission filter; else IN (...) set
	Search      string   // matches file_name or doc_id (ILIKE)
	Limit       int
	Offset      int
}

// Store is the persistence surface for the document registry.
type Store interface {
	Upsert(ctx context.Context, d Document) error
	Get(ctx context.Context, tenantID, docID string) (Document, bool, error)
	Delete(ctx context.Context, tenantID, docID string) error
	List(ctx context.Context, q ListQuery) ([]Document, int, error)
	UpsertStatus(ctx context.Context, tenantID, docID string, d Document) error
	ReconcileUpsert(ctx context.Context, tenantID, docID, permission string) error
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
			error, metadata, uploaded_by, created_at, updated_at, completed_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
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
			completed_at = EXCLUDED.completed_at`,
		d.TenantID, d.DocID, d.FileName, d.ObjectKey, d.FileHash, d.FileSize,
		d.ContentType, d.Permission, d.Status, d.Stage, d.ChunksDone, d.ChunksTotal,
		d.Error, d.Metadata, d.UploadedBy, d.CreatedAt, d.UpdatedAt, d.CompletedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert document: %w", err)
	}
	return nil
}

// Get returns the registry row for (tenant, doc_id).
func (s *PgStore) Get(ctx context.Context, tenantID, docID string) (Document, bool, error) {
	row := s.q.QueryRow(ctx, `
		SELECT tenant_id, doc_id, file_name, object_key, file_hash, file_size,
		       content_type, permission, status, stage, chunks_done, chunks_total,
		       error, metadata, uploaded_by, created_at, updated_at, completed_at
		FROM documents WHERE tenant_id=$1 AND doc_id=$2`, tenantID, docID)
	d, found, err := scanDocument(row)
	if err != nil {
		return Document{}, false, err
	}
	if !found {
		return Document{}, false, nil
	}
	return d, true, nil
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

	rows, err := s.q.Query(ctx, `
		SELECT tenant_id, doc_id, file_name, object_key, file_hash, file_size,
		       content_type, permission, status, stage, chunks_done, chunks_total,
		       error, metadata, uploaded_by, created_at, updated_at, completed_at
		FROM documents `+where+`
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
			completed_at = CASE WHEN $8::text = '' THEN completed_at ELSE $8::timestamptz END
		WHERE tenant_id = $1 AND doc_id = $2`,
		tenantID, docID, d.Status, d.Stage, d.ChunksDone, d.ChunksTotal, d.Error,
		completedAtParam(d.CompletedAt))
	if err != nil {
		return fmt.Errorf("upsert document status: %w", err)
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
	if q.Search != "" {
		args = append(args, "%"+strings.ToLower(q.Search)+"%")
		conds = append(conds, fmt.Sprintf("(lower(file_name) LIKE $%d OR lower(doc_id) LIKE $%d)", len(args), len(args)))
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

func scanDocument(row pgx.Row) (Document, bool, error) {
	var d Document
	var completedAt *time.Time
	var metadata map[string]string
	err := row.Scan(&d.TenantID, &d.DocID, &d.FileName, &d.ObjectKey, &d.FileHash, &d.FileSize,
		&d.ContentType, &d.Permission, &d.Status, &d.Stage, &d.ChunksDone, &d.ChunksTotal,
		&d.Error, &metadata, &d.UploadedBy, &d.CreatedAt, &d.UpdatedAt, &completedAt)
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
	return d, true, nil
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
