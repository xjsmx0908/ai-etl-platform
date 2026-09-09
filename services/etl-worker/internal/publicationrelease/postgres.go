package publicationrelease

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"ai-etl-pipeline/internal/db"
	"ai-etl-pipeline/internal/indexmanifest"
)

type PostgresStore struct {
	q db.Querier
}

// ResolveVisibility applies the published-release read policy to backend and
// cached candidates. A candidate is visible only when it exactly matches the
// tenant's resolved published version/generation and that manifest remains
// active and healthy. Missing or incomplete authority fails closed.
func (s *PostgresStore) ResolveVisibility(ctx context.Context, tenantID string, refs []indexmanifest.GenerationReference) ([]bool, error) {
	visible := make([]bool, len(refs))
	if len(refs) == 0 {
		return visible, nil
	}
	if s == nil || s.q == nil || strings.TrimSpace(tenantID) == "" {
		return nil, ErrInvalid
	}
	documentIDs := make([]string, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref.DocumentID) == "" {
			continue
		}
		if _, ok := seen[ref.DocumentID]; ok {
			continue
		}
		seen[ref.DocumentID] = struct{}{}
		documentIDs = append(documentIDs, ref.DocumentID)
	}
	if len(documentIDs) == 0 {
		return visible, nil
	}
	type identity struct{ versionID, generationID string }
	published := make(map[string]identity, len(documentIDs))
	rows, err := s.q.Query(ctx, `SELECT r.document_id,r.published_version_id,r.published_generation_id
		FROM document_releases r
		JOIN index_manifests m ON m.tenant_id=r.tenant_id
			AND m.document_id=r.document_id
			AND m.document_version_id=r.published_version_id
			AND m.generation_id=r.published_generation_id
		WHERE r.tenant_id=$1 AND r.document_id=ANY($2)
			AND r.resolution_status='resolved'
			AND r.published_version_id IS NOT NULL
			AND r.published_generation_id IS NOT NULL
			AND m.state='active' AND m.last_reconcile_error=''
			AND m.expected_chunk_count > 0
			AND m.expected_chunk_digest IS NOT NULL AND m.expected_chunk_digest<>''
			AND m.qdrant_count=m.expected_chunk_count
			AND m.qdrant_digest=m.expected_chunk_digest
			AND m.elasticsearch_count=m.expected_chunk_count
			AND m.elasticsearch_digest=m.expected_chunk_digest`, tenantID, documentIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve published release visibility: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var documentID, versionID, generationID string
		if err := rows.Scan(&documentID, &versionID, &generationID); err != nil {
			return nil, fmt.Errorf("scan published release visibility: %w", err)
		}
		published[documentID] = identity{versionID: versionID, generationID: generationID}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate published release visibility: %w", err)
	}
	legacyIDs := make([]string, 0, len(documentIDs))
	for _, documentID := range documentIDs {
		identity, ok := published[documentID]
		if ok && identity.versionID != "" && identity.generationID != "" {
			continue
		}
		legacyIDs = append(legacyIDs, documentID)
	}
	legacyPublished := map[string]struct{}{}
	if len(legacyIDs) > 0 {
		legacyRows, err := s.q.Query(ctx, `SELECT doc_id FROM documents
			WHERE tenant_id=$1 AND doc_id=ANY($2)
			  AND publication_status='published'
			  AND deletion_status='active'`, tenantID, legacyIDs)
		if err != nil {
			return nil, fmt.Errorf("resolve published legacy visibility: %w", err)
		}
		defer legacyRows.Close()
		for legacyRows.Next() {
			var documentID string
			if err := legacyRows.Scan(&documentID); err != nil {
				return nil, fmt.Errorf("scan published legacy visibility: %w", err)
			}
			legacyPublished[documentID] = struct{}{}
		}
		if err := legacyRows.Err(); err != nil {
			return nil, fmt.Errorf("iterate published legacy visibility: %w", err)
		}
	}
	for i, ref := range refs {
		identity, ok := published[ref.DocumentID]
		if ok && ref.DocumentVersionID != "" && ref.GenerationID != "" &&
			identity.versionID == ref.DocumentVersionID && identity.generationID == ref.GenerationID {
			visible[i] = true
			continue
		}
		if _, ok := legacyPublished[ref.DocumentID]; ok && strings.TrimSpace(ref.DocumentVersionID) == "" && strings.TrimSpace(ref.GenerationID) == "" {
			visible[i] = true
		}
	}
	return visible, nil
}

func NewPostgresStore(q db.Querier) *PostgresStore {
	return &PostgresStore{q: q}
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, documentID string) (Release, bool, error) {
	if s == nil || s.q == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(documentID) == "" {
		return Release{}, false, ErrInvalid
	}
	row := s.q.QueryRow(ctx, `SELECT tenant_id,document_id,COALESCE(current_version_id,''),
		COALESCE(published_version_id,''),COALESCE(published_generation_id,''),
		revision,resolution_status,last_error,updated_at
		FROM document_releases WHERE tenant_id=$1 AND document_id=$2`, tenantID, documentID)
	release, err := scanRelease(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Release{}, false, nil
	}
	if err != nil {
		return Release{}, false, fmt.Errorf("load document release: %w", err)
	}
	return release, true, nil
}

// RecordCurrent records an admitted document version. A replacement advances
// the revision but deliberately preserves the last independently approved
// release until a later compare-and-set publication.
func (s *PostgresStore) RecordCurrent(ctx context.Context, version VersionIdentity) (Release, error) {
	if s == nil || s.q == nil || invalidVersion(version) {
		return Release{}, ErrInvalid
	}
	row := s.q.QueryRow(ctx, `INSERT INTO document_releases
		(tenant_id,document_id,current_version_id,revision,resolution_status,last_error)
		VALUES ($1,$2,$3,1,'resolved','')
		ON CONFLICT (tenant_id,document_id) DO UPDATE SET
			current_version_id=EXCLUDED.current_version_id,
			revision=CASE
				WHEN document_releases.current_version_id IS DISTINCT FROM EXCLUDED.current_version_id
				THEN document_releases.revision+1 ELSE document_releases.revision END,
			resolution_status='resolved',last_error='',updated_at=now()
		RETURNING tenant_id,document_id,current_version_id,
			COALESCE(published_version_id,''),COALESCE(published_generation_id,''),
			revision,resolution_status,last_error,updated_at`,
		version.TenantID, version.DocumentID, version.VersionID)
	release, err := scanRelease(row)
	if err != nil {
		return Release{}, fmt.Errorf("record current document release: %w", err)
	}
	return release, nil
}

// Publish atomically advances the approved release only when both the reviewed
// current version and its revision still match. Any intervening replacement or
// publication makes the candidate stale.
func (s *PostgresStore) Publish(ctx context.Context, candidate Candidate) (Release, error) {
	if s == nil || s.q == nil || invalidVersion(candidate.VersionIdentity) ||
		strings.TrimSpace(candidate.GenerationID) == "" || candidate.ExpectedRevision <= 0 {
		return Release{}, ErrInvalid
	}
	row := s.q.QueryRow(ctx, `UPDATE document_releases SET
		published_version_id=$3,published_generation_id=$4,
		revision=revision+1,last_error='',updated_at=now()
		WHERE tenant_id=$1 AND document_id=$2 AND current_version_id=$3
			AND revision=$5 AND resolution_status='resolved'
		RETURNING tenant_id,document_id,current_version_id,
			published_version_id,published_generation_id,
			revision,resolution_status,last_error,updated_at`,
		candidate.TenantID, candidate.DocumentID, candidate.VersionID,
		candidate.GenerationID, candidate.ExpectedRevision)
	release, err := scanRelease(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Release{}, ErrConflict
	}
	if err != nil {
		return Release{}, fmt.Errorf("publish document release: %w", err)
	}
	return release, nil
}

// PublishAutomatic advances the release for a document whose knowledge-space
// policy permits automatic publication. It derives the generation from the
// healthy active manifest instead of accepting mutable caller input. Replaying
// the same completed version is idempotent.
func (s *PostgresStore) PublishAutomatic(ctx context.Context, version VersionIdentity) (Release, error) {
	if s == nil || s.q == nil || invalidVersion(version) {
		return Release{}, ErrInvalid
	}
	row := s.q.QueryRow(ctx, `WITH candidate AS (
		SELECT m.generation_id
		FROM index_manifests m
		WHERE m.tenant_id=$1 AND m.document_id=$2 AND m.document_version_id=$3
			AND m.state='active' AND m.last_reconcile_error=''
			AND m.expected_chunk_count > 0
			AND m.expected_chunk_digest IS NOT NULL AND m.expected_chunk_digest<>''
			AND m.qdrant_count=m.expected_chunk_count
			AND m.qdrant_digest=m.expected_chunk_digest
			AND m.elasticsearch_count=m.expected_chunk_count
			AND m.elasticsearch_digest=m.expected_chunk_digest
	)
	UPDATE document_releases r SET
		published_version_id=$3,published_generation_id=c.generation_id,
		revision=CASE WHEN r.published_version_id=$3
			AND r.published_generation_id=c.generation_id THEN r.revision ELSE r.revision+1 END,
		last_error='',updated_at=now()
	FROM candidate c
	WHERE r.tenant_id=$1 AND r.document_id=$2 AND r.current_version_id=$3
		AND EXISTS (SELECT 1 FROM documents d WHERE d.tenant_id=r.tenant_id AND d.doc_id=r.document_id AND d.deletion_status='active')
		AND r.resolution_status='resolved'
	RETURNING r.tenant_id,r.document_id,r.current_version_id,
		COALESCE(r.published_version_id,''),COALESCE(r.published_generation_id,''),
		r.revision,r.resolution_status,r.last_error,r.updated_at`, version.TenantID,
		version.DocumentID, version.VersionID)
	release, err := scanRelease(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Release{}, ErrConflict
	}
	if err != nil {
		return Release{}, fmt.Errorf("automatically publish document release: %w", err)
	}
	return release, nil
}

func invalidVersion(version VersionIdentity) bool {
	return strings.TrimSpace(version.TenantID) == "" ||
		strings.TrimSpace(version.DocumentID) == "" ||
		strings.TrimSpace(version.VersionID) == ""
}

type releaseScanner interface {
	Scan(dest ...any) error
}

func scanRelease(row releaseScanner) (Release, error) {
	var release Release
	err := row.Scan(&release.TenantID, &release.DocumentID, &release.CurrentVersionID,
		&release.PublishedVersionID, &release.PublishedGenerationID,
		&release.Revision, &release.ResolutionStatus, &release.LastError, &release.UpdatedAt)
	return release, err
}
