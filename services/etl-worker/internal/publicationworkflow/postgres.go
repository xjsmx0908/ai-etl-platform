package publicationworkflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"ai-etl-pipeline/internal/audit"
	"ai-etl-pipeline/internal/db"
)

var ErrCandidateStale = errors.New("publication workflow: exact candidate is stale")

type CacheInvalidator interface {
	InvalidateSemanticCache(context.Context) error
}

// PostgresPublication hides candidate resolution, approval-time revalidation,
// release CAS, document publication, and success audit behind one seam.
type PostgresPublication struct {
	q     db.Querier
	cache CacheInvalidator
}

func NewPostgresPublication(q db.Querier, cache CacheInvalidator) *PostgresPublication {
	return &PostgresPublication{q: q, cache: cache}
}

func (p *PostgresPublication) CurrentCandidate(ctx context.Context, tenantID, documentID string) (Candidate, bool, error) {
	if p == nil || p.q == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(documentID) == "" {
		return Candidate{}, false, fmt.Errorf("publication candidate store is not configured")
	}
	var candidate Candidate
	err := p.q.QueryRow(ctx, `SELECT r.document_id,r.current_version_id,m.generation_id,
		m.expected_chunk_count,m.expected_chunk_digest,r.revision
		FROM document_releases r
		JOIN index_manifests m ON m.tenant_id=r.tenant_id
			AND m.document_id=r.document_id AND m.document_version_id=r.current_version_id
		WHERE r.tenant_id=$1 AND r.document_id=$2 AND r.resolution_status='resolved'
			AND (r.published_version_id IS NULL OR r.published_version_id<>r.current_version_id)
			AND m.state='active' AND m.expected_chunk_count > 0
			AND m.expected_chunk_digest IS NOT NULL AND m.expected_chunk_digest <> ''
			AND m.last_reconcile_error=''
			AND m.qdrant_count=m.expected_chunk_count
			AND m.qdrant_digest=m.expected_chunk_digest
			AND m.elasticsearch_count=m.expected_chunk_count
			AND m.elasticsearch_digest=m.expected_chunk_digest`, tenantID, documentID).Scan(
		&candidate.DocumentID, &candidate.DocumentVersionID, &candidate.GenerationID,
		&candidate.ExpectedChunkCount, &candidate.ExpectedChunkDigest, &candidate.ReleaseRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Candidate{}, false, nil
	}
	if err != nil {
		return Candidate{}, false, fmt.Errorf("load exact publication candidate: %w", err)
	}
	return candidate, true, nil
}

func (p *PostgresPublication) Publish(ctx context.Context, actor Actor, candidate Candidate, idempotencyKey string) error {
	if !strings.EqualFold(strings.TrimSpace(actor.Role), "admin") {
		return ErrAdminRequired
	}
	if p == nil || p.q == nil {
		return fmt.Errorf("publication store is not configured")
	}
	if strings.TrimSpace(actor.TenantID) == "" || !validCandidate(candidate) || strings.TrimSpace(idempotencyKey) == "" {
		return fmt.Errorf("%w: incomplete exact candidate", ErrCandidateStale)
	}
	tx, err := p.q.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin exact candidate publication: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	requestHash := publicationRequestHash(actor.TenantID, candidate)
	var currentVersion, publishedVersion, publishedGeneration, resolution, lastKey, lastRequestHash string
	var revision int64
	err = tx.QueryRow(ctx, `SELECT COALESCE(current_version_id,''),COALESCE(published_version_id,''),
		COALESCE(published_generation_id,''),revision,resolution_status,last_publication_idempotency_key,
		last_publication_request_hash
		FROM document_releases WHERE tenant_id=$1 AND document_id=$2 FOR UPDATE`,
		actor.TenantID, candidate.DocumentID).Scan(&currentVersion, &publishedVersion,
		&publishedGeneration, &revision, &resolution, &lastKey, &lastRequestHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrCandidateStale
	}
	if err != nil {
		return fmt.Errorf("lock document release: %w", err)
	}
	if lastKey == idempotencyKey {
		if currentVersion != candidate.DocumentVersionID || publishedVersion != candidate.DocumentVersionID ||
			publishedGeneration != candidate.GenerationID || revision != candidate.ReleaseRevision+1 ||
			lastRequestHash != requestHash {
			return fmt.Errorf("%w: idempotency key does not match candidate", ErrCandidateStale)
		}
		return nil
	}
	if currentVersion != candidate.DocumentVersionID || revision != candidate.ReleaseRevision || resolution != "resolved" {
		return ErrCandidateStale
	}

	var lockedGenerationID string
	err = tx.QueryRow(ctx, `SELECT generation_id FROM index_manifests
		WHERE tenant_id=$1 AND document_id=$2 AND document_version_id=$3 AND generation_id=$4
			AND expected_chunk_count=$5 AND expected_chunk_digest=$6 AND state='active'
			AND last_reconcile_error='' AND qdrant_count=expected_chunk_count
			AND qdrant_digest=expected_chunk_digest AND elasticsearch_count=expected_chunk_count
			AND elasticsearch_digest=expected_chunk_digest FOR SHARE`, actor.TenantID, candidate.DocumentID,
		candidate.DocumentVersionID, candidate.GenerationID, candidate.ExpectedChunkCount,
		candidate.ExpectedChunkDigest).Scan(&lockedGenerationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrCandidateStale
	}
	if err != nil {
		return fmt.Errorf("revalidate exact publication candidate: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE documents SET publication_status='published',updated_at=now()
		WHERE tenant_id=$1 AND doc_id=$2 AND status='completed' AND doc_status='active'
			AND deletion_status='active'
			AND publication_status IN ('draft','published') AND knowledge_space_id<>'' AND knowledge_space_id<>'user-uploads'
			AND owner<>'' AND effective_date IS NOT NULL`, actor.TenantID, candidate.DocumentID)
	if err != nil {
		return fmt.Errorf("publish governed document: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrCandidateStale
	}
	tag, err = tx.Exec(ctx, `UPDATE document_releases SET published_version_id=$3,
		published_generation_id=$4,revision=revision+1,last_error='',
		last_publication_idempotency_key=$6,last_publication_request_hash=$7,updated_at=now()
		WHERE tenant_id=$1 AND document_id=$2 AND current_version_id=$3
			AND revision=$5 AND resolution_status='resolved'`, actor.TenantID, candidate.DocumentID,
		candidate.DocumentVersionID, candidate.GenerationID, candidate.ReleaseRevision, idempotencyKey, requestHash)
	if err != nil {
		return fmt.Errorf("advance approved document release: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrCandidateStale
	}
	if _, err = tx.Exec(ctx, `UPDATE index_manifests SET state='retired',retired_at=now()
		WHERE tenant_id=$1 AND document_id=$2 AND generation_id<>$3 AND state='active'`,
		actor.TenantID, candidate.DocumentID, candidate.GenerationID); err != nil {
		return fmt.Errorf("retire superseded document generations: %w", err)
	}
	if err := audit.New(tx).Record(ctx, audit.Entry{
		TenantID: actor.TenantID, ActorUserID: actor.UserID, ActorRole: actor.Role,
		Action: "document.publication.update", ResourceType: "document", ResourceID: candidate.DocumentID,
		Result: audit.ResultSuccess, Detail: map[string]any{
			"publication_status": "published", "idempotency_key": idempotencyKey,
			"document_version_id": candidate.DocumentVersionID, "generation_id": candidate.GenerationID,
			"expected_chunk_count": candidate.ExpectedChunkCount, "expected_chunk_digest": candidate.ExpectedChunkDigest,
			"release_revision": candidate.ReleaseRevision,
			"source":           "document_publication_agent",
		},
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit exact candidate publication: %w", err)
	}
	if p.cache != nil {
		if err := p.cache.InvalidateSemanticCache(ctx); err != nil {
			slog.Warn("semantic cache flush failed after governed publication", "doc_id", candidate.DocumentID, "error", err)
		}
	}
	return nil
}

func publicationRequestHash(tenantID string, candidate Candidate) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%q\n%q\n%q\n%q\n%d\n%q\n%d\n", tenantID, candidate.DocumentID,
		candidate.DocumentVersionID, candidate.GenerationID, candidate.ExpectedChunkCount,
		candidate.ExpectedChunkDigest, candidate.ReleaseRevision)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

var _ CandidateReader = (*PostgresPublication)(nil)
var _ Publisher = (*PostgresPublication)(nil)
