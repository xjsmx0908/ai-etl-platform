package publicationrelease

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"ai-etl-pipeline/internal/db"
)

type PostgresStore struct {
	q db.Querier
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
