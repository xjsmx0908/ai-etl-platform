package indexmanifest

import (
	"context"
	"fmt"
	"time"

	"ai-etl-pipeline/internal/db"
)

// Store is the narrow persistence seam for generation manifests.
type Store interface {
	Create(context.Context, Manifest) error
	Observe(context.Context, string, Backend, BackendObservation) error
	MarkReady(context.Context, string) error
	Fail(context.Context, string, string) error
	Activate(context.Context, string, string, string) error
}

type PostgresStore struct{ q db.Querier }

func NewPostgresStore(q db.Querier) *PostgresStore { return &PostgresStore{q: q} }

var _ Store = (*PostgresStore)(nil)

func (s *PostgresStore) Create(ctx context.Context, m Manifest) error {
	if m.GenerationID == "" || m.TenantID == "" || m.DocumentID == "" || m.DocumentVersionID == "" || m.ExpectedChunkCount < 0 || m.ExpectedChunkDigest == "" {
		return ErrInvalidManifest
	}
	if m.State == "" {
		m.State = StateBuilding
	}
	if m.State != StateBuilding {
		return ErrInvalidManifest
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	_, err := s.q.Exec(ctx, `INSERT INTO index_manifests (
generation_id,tenant_id,document_id,document_version_id,chunker_version,embedding_model,vector_dimension,schema_version,collection_version,index_version,expected_chunk_count,expected_chunk_digest,state,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,COALESCE($14,now()))`, m.GenerationID, m.TenantID, m.DocumentID, m.DocumentVersionID, m.ChunkerVersion, m.EmbeddingModel, m.VectorDimension, m.SchemaVersion, m.CollectionVersion, m.IndexVersion, m.ExpectedChunkCount, m.ExpectedChunkDigest, m.State, m.CreatedAt)
	if err != nil {
		return fmt.Errorf("create index manifest: %w", err)
	}
	return nil
}

func (s *PostgresStore) Observe(ctx context.Context, generationID string, backend Backend, observation BackendObservation) error {
	if generationID == "" || (backend != BackendQdrant && backend != BackendElasticsearch) || observation.Count < 0 || observation.Digest == "" {
		return ErrInvalidManifest
	}
	column, digest := "qdrant_count", "qdrant_digest"
	if backend == BackendElasticsearch {
		column, digest = "elasticsearch_count", "elasticsearch_digest"
	}
	tag, err := s.q.Exec(ctx, "UPDATE index_manifests SET "+column+"=$2, "+digest+"=$3, verified_at=now() WHERE generation_id=$1 AND state='building'", generationID, observation.Count, observation.Digest)
	if err != nil {
		return fmt.Errorf("observe %s manifest: %w", backend, err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (s *PostgresStore) MarkReady(ctx context.Context, generationID string) error {
	if generationID == "" {
		return ErrInvalidManifest
	}
	tag, err := s.q.Exec(ctx, `UPDATE index_manifests SET state='ready', verified_at=now(), last_error=''
WHERE generation_id=$1 AND state='building' AND qdrant_count=expected_chunk_count AND elasticsearch_count=expected_chunk_count
AND qdrant_digest=expected_chunk_digest AND elasticsearch_digest=expected_chunk_digest`, generationID)
	if err != nil {
		return fmt.Errorf("mark manifest ready: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotReady
	}
	return nil
}

func (s *PostgresStore) Fail(ctx context.Context, generationID, reason string) error {
	if generationID == "" || reason == "" {
		return ErrInvalidManifest
	}
	tag, err := s.q.Exec(ctx, `UPDATE index_manifests SET state='failed', attempts=attempts+1, last_error=$2, last_attempt_at=now() WHERE generation_id=$1 AND state IN ('building','failed')`, generationID, reason)
	if err != nil {
		return fmt.Errorf("fail index manifest: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (s *PostgresStore) Activate(ctx context.Context, tenantID, documentVersionID, generationID string) error {
	if tenantID == "" || documentVersionID == "" || generationID == "" {
		return ErrInvalidManifest
	}
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin manifest activation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, tenantID+"/"+documentVersionID); err != nil {
		return fmt.Errorf("lock manifest activation: %w", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE index_manifests SET state='retired' WHERE tenant_id=$1 AND document_version_id=$2 AND state='active'`, tenantID, documentVersionID); err != nil {
		return fmt.Errorf("retire previous manifest: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE index_manifests SET state='active', activated_at=now() WHERE tenant_id=$1 AND document_version_id=$2 AND generation_id=$3 AND state='ready'`, tenantID, documentVersionID, generationID)
	if err != nil {
		return fmt.Errorf("activate manifest: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit manifest activation: %w", err)
	}
	return nil
}
