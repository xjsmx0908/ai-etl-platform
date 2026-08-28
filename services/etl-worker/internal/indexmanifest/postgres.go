package indexmanifest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"ai-etl-pipeline/internal/db"

	"github.com/jackc/pgx/v5"
)

type Store interface {
	Begin(context.Context, Manifest) (Manifest, error)
	SealExpected(context.Context, string, int, string) (Manifest, error)
	Ensure(context.Context, Manifest) error
	ActiveGeneration(context.Context, VersionIdentity) (string, bool, error)
	Observe(context.Context, string, Backend, BackendObservation) error
	Fail(context.Context, string, string) error
	Retry(context.Context, string) error
	MarkReady(context.Context, string) error
	Activate(context.Context, ActivationTarget) error
}

type PostgresStore struct{ q db.Querier }

func NewPostgresStore(q db.Querier) *PostgresStore { return &PostgresStore{q: q} }

var _ Store = (*PostgresStore)(nil)

func (s *PostgresStore) Begin(ctx context.Context, manifest Manifest) (Manifest, error) {
	if err := validateUnsealedBuildDefinition(manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.CreatedAt.IsZero() {
		manifest.CreatedAt = time.Now().UTC()
	}
	active, _, err := s.ActiveGeneration(ctx, VersionIdentity{
		TenantID: manifest.TenantID, DocumentID: manifest.DocumentID,
		DocumentVersionID: manifest.DocumentVersionID,
	})
	if err != nil {
		return Manifest{}, err
	}
	manifest.ExpectedActiveGenerationID = active
	_, err = s.q.Exec(ctx, `INSERT INTO index_manifests (
generation_id,tenant_id,document_id,document_version_id,chunker_version,embedding_model,vector_dimension,schema_version,collection_version,index_version,expected_active_generation_id,state,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'building',$12)
ON CONFLICT (generation_id) DO NOTHING`, manifest.GenerationID, manifest.TenantID,
		manifest.DocumentID, manifest.DocumentVersionID, manifest.ChunkerVersion,
		manifest.EmbeddingModel, manifest.VectorDimension, manifest.SchemaVersion,
		manifest.CollectionVersion, manifest.IndexVersion, manifest.ExpectedActiveGenerationID,
		manifest.CreatedAt)
	if err != nil {
		return Manifest{}, fmt.Errorf("begin index manifest: %w", err)
	}
	existing, err := scanManifest(s.q.QueryRow(ctx, manifestSelect+" WHERE generation_id=$1", manifest.GenerationID))
	if err != nil {
		return Manifest{}, fmt.Errorf("load begun index manifest: %w", err)
	}
	if !sameUnsealedBuildDefinition(existing, manifest) {
		return Manifest{}, ErrConflict
	}
	return existing, nil
}

func (s *PostgresStore) SealExpected(ctx context.Context, generationID string, count int, digest string) (Manifest, error) {
	if generationID == "" || count <= 0 || digest == "" {
		return Manifest{}, ErrInvalidManifest
	}
	_, err := s.q.Exec(ctx, `UPDATE index_manifests
SET expected_chunk_count=$2, expected_chunk_digest=$3
WHERE generation_id=$1 AND state='building' AND expected_chunk_count IS NULL`, generationID, count, digest)
	if err != nil {
		return Manifest{}, fmt.Errorf("seal expected generation identity: %w", err)
	}
	manifest, err := scanManifest(s.q.QueryRow(ctx, manifestSelect+" WHERE generation_id=$1", generationID))
	if err != nil {
		return Manifest{}, fmt.Errorf("load sealed index manifest: %w", err)
	}
	if !manifest.ExpectedSealed || manifest.ExpectedChunkCount != count || manifest.ExpectedChunkDigest != digest {
		return Manifest{}, ErrConflict
	}
	return manifest, nil
}

func (s *PostgresStore) ActiveGeneration(ctx context.Context, version VersionIdentity) (string, bool, error) {
	if version.TenantID == "" || version.DocumentID == "" || version.DocumentVersionID == "" {
		return "", false, ErrInvalidManifest
	}
	var generationID string
	err := s.q.QueryRow(ctx, `SELECT generation_id FROM index_manifests
WHERE tenant_id=$1 AND document_id=$2 AND document_version_id=$3 AND state='active'`,
		version.TenantID, version.DocumentID, version.DocumentVersionID).Scan(&generationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("load active generation: %w", err)
	}
	return generationID, true, nil
}

// ResolveVisibility applies the active-generation read policy to a batch of
// candidates. A document with no manifest remains legacy-compatible; once any
// manifest exists, only its active generation is visible.
func (s *PostgresStore) ResolveVisibility(ctx context.Context, tenantID string, refs []GenerationReference) ([]bool, error) {
	visible := make([]bool, len(refs))
	if len(refs) == 0 {
		return visible, nil
	}
	if tenantID == "" {
		return nil, ErrInvalidManifest
	}
	docIDs := make([]string, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref.DocumentID == "" {
			continue
		}
		if _, ok := seen[ref.DocumentID]; !ok {
			seen[ref.DocumentID] = struct{}{}
			docIDs = append(docIDs, ref.DocumentID)
		}
	}
	type manifestRef struct {
		version, generation string
		state               ManifestState
	}
	manifests := make(map[string][]manifestRef)
	if len(docIDs) > 0 {
		rows, err := s.q.Query(ctx, `SELECT document_id,document_version_id,generation_id,state FROM index_manifests
WHERE tenant_id=$1 AND document_id = ANY($2)`, tenantID, docIDs)
		if err != nil {
			return nil, fmt.Errorf("resolve index visibility: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var docID, versionID, generationID string
			var state ManifestState
			if err := rows.Scan(&docID, &versionID, &generationID, &state); err != nil {
				return nil, fmt.Errorf("scan index visibility: %w", err)
			}
			manifests[docID] = append(manifests[docID], manifestRef{version: versionID, generation: generationID, state: state})
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("iterate index visibility: %w", err)
		}
	}
	for i, ref := range refs {
		if ref.DocumentID == "" || (ref.GenerationID == "") != (ref.DocumentVersionID == "") {
			continue
		}
		entries, managed := manifests[ref.DocumentID]
		if !managed {
			visible[i] = ref.GenerationID == ""
			continue
		}
		for _, entry := range entries {
			if entry.state == StateActive && entry.version == ref.DocumentVersionID && entry.generation == ref.GenerationID {
				visible[i] = true
				break
			}
		}
	}
	return visible, nil
}

func (s *PostgresStore) Ensure(ctx context.Context, manifest Manifest) error {
	if err := validateBuildDefinition(manifest); err != nil {
		return err
	}
	if manifest.State == "" {
		manifest.State = StateBuilding
	}
	if manifest.State != StateBuilding {
		return ErrInvalidManifest
	}
	if manifest.CreatedAt.IsZero() {
		manifest.CreatedAt = time.Now().UTC()
	}
	_, err := s.q.Exec(ctx, `INSERT INTO index_manifests (
generation_id,tenant_id,document_id,document_version_id,chunker_version,embedding_model,vector_dimension,schema_version,collection_version,index_version,expected_chunk_count,expected_chunk_digest,state,created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (generation_id) DO NOTHING`, manifest.GenerationID, manifest.TenantID,
		manifest.DocumentID, manifest.DocumentVersionID, manifest.ChunkerVersion,
		manifest.EmbeddingModel, manifest.VectorDimension, manifest.SchemaVersion,
		manifest.CollectionVersion, manifest.IndexVersion, manifest.ExpectedChunkCount,
		manifest.ExpectedChunkDigest, manifest.State, manifest.CreatedAt)
	if err != nil {
		return fmt.Errorf("ensure index manifest: %w", err)
	}
	existing, err := scanImmutableManifest(s.q.QueryRow(ctx, immutableManifestSelect+" WHERE generation_id=$1", manifest.GenerationID))
	if err != nil {
		return fmt.Errorf("load ensured index manifest: %w", err)
	}
	if !sameBuildDefinition(existing, manifest) {
		return ErrConflict
	}
	return nil
}

func (s *PostgresStore) Observe(ctx context.Context, generationID string, backend Backend, observation BackendObservation) error {
	if generationID == "" || (backend != BackendQdrant && backend != BackendElasticsearch) || observation.Count < 0 || observation.Digest == "" {
		return ErrInvalidManifest
	}
	countColumn, digestColumn, timeColumn := "qdrant_count", "qdrant_digest", "qdrant_observed_at"
	if backend == BackendElasticsearch {
		countColumn, digestColumn, timeColumn = "elasticsearch_count", "elasticsearch_digest", "elasticsearch_observed_at"
	}
	tag, err := s.q.Exec(ctx, "UPDATE index_manifests SET "+countColumn+"=$2, "+digestColumn+"=$3, "+timeColumn+"=now() WHERE generation_id=$1 AND state='building'", generationID, observation.Count, observation.Digest)
	if err != nil {
		return fmt.Errorf("observe %s manifest: %w", backend, err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (s *PostgresStore) Fail(ctx context.Context, generationID, reason string) error {
	if generationID == "" || reason == "" {
		return ErrInvalidManifest
	}
	tag, err := s.q.Exec(ctx, `UPDATE index_manifests SET state='failed', last_error=$2,
last_attempt_at=now() WHERE generation_id=$1 AND state='building'`, generationID, reason)
	if err != nil {
		return fmt.Errorf("fail index manifest: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (s *PostgresStore) Retry(ctx context.Context, generationID string) error {
	if generationID == "" {
		return ErrInvalidManifest
	}
	tag, err := s.q.Exec(ctx, `UPDATE index_manifests SET state='building', attempts=attempts+1,
last_error='', last_attempt_at=now(), qdrant_count=NULL, qdrant_digest=NULL,
qdrant_observed_at=NULL, elasticsearch_count=NULL, elasticsearch_digest=NULL,
elasticsearch_observed_at=NULL, verified_at=NULL
WHERE generation_id=$1 AND state='failed'`, generationID)
	if err != nil {
		return fmt.Errorf("retry index manifest: %w", err)
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
WHERE generation_id=$1 AND state='building' AND qdrant_count=expected_chunk_count
AND elasticsearch_count=expected_chunk_count AND qdrant_digest=expected_chunk_digest
AND elasticsearch_digest=expected_chunk_digest`, generationID)
	if err != nil {
		return fmt.Errorf("mark manifest ready: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotReady
	}
	return nil
}

func (s *PostgresStore) Activate(ctx context.Context, target ActivationTarget) error {
	if target.Version.TenantID == "" || target.Version.DocumentID == "" || target.Version.DocumentVersionID == "" || target.GenerationID == "" || target.GenerationID == target.ExpectedActiveGenerationID {
		return ErrInvalidManifest
	}
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin manifest activation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	lockID := target.Version.TenantID + "/" + target.Version.DocumentVersionID
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockID); err != nil {
		return fmt.Errorf("lock manifest activation: %w", err)
	}
	var current string
	err = tx.QueryRow(ctx, `SELECT generation_id FROM index_manifests
WHERE tenant_id=$1 AND document_id=$2 AND document_version_id=$3 AND state='active'`,
		target.Version.TenantID, target.Version.DocumentID, target.Version.DocumentVersionID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		current = ""
	} else if err != nil {
		return fmt.Errorf("load active manifest: %w", err)
	}
	if current != target.ExpectedActiveGenerationID {
		return ErrConflict
	}
	if current != "" {
		tag, err := tx.Exec(ctx, `UPDATE index_manifests SET state='retired'
WHERE tenant_id=$1 AND document_id=$2 AND document_version_id=$3
AND generation_id=$4 AND state='active'`, target.Version.TenantID, target.Version.DocumentID,
			target.Version.DocumentVersionID, current)
		if err != nil {
			return fmt.Errorf("retire previous manifest: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE index_manifests SET state='active', activated_at=now()
WHERE tenant_id=$1 AND document_id=$2 AND document_version_id=$3
AND generation_id=$4 AND state='ready'`, target.Version.TenantID, target.Version.DocumentID,
		target.Version.DocumentVersionID, target.GenerationID)
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

func validateBuildDefinition(manifest Manifest) error {
	if err := validateUnsealedBuildDefinition(manifest); err != nil ||
		manifest.ExpectedChunkCount < 0 || manifest.ExpectedChunkDigest == "" {
		return ErrInvalidManifest
	}
	return nil
}

func validateUnsealedBuildDefinition(manifest Manifest) error {
	if manifest.GenerationID == "" || manifest.TenantID == "" || manifest.DocumentID == "" ||
		manifest.DocumentVersionID == "" || manifest.ChunkerVersion == "" ||
		manifest.EmbeddingModel == "" || manifest.VectorDimension <= 0 ||
		manifest.SchemaVersion == "" || manifest.CollectionVersion == "" ||
		manifest.IndexVersion == "" {
		return ErrInvalidManifest
	}
	return nil
}

const manifestSelect = `SELECT generation_id,tenant_id,document_id,
document_version_id,chunker_version,embedding_model,vector_dimension,schema_version,
collection_version,index_version,expected_active_generation_id,expected_chunk_count,expected_chunk_digest,state
FROM index_manifests`

func scanManifest(row pgx.Row) (Manifest, error) {
	var manifest Manifest
	var expectedCount sql.NullInt64
	var expectedDigest sql.NullString
	err := row.Scan(&manifest.GenerationID, &manifest.TenantID, &manifest.DocumentID,
		&manifest.DocumentVersionID, &manifest.ChunkerVersion, &manifest.EmbeddingModel,
		&manifest.VectorDimension, &manifest.SchemaVersion, &manifest.CollectionVersion,
		&manifest.IndexVersion, &manifest.ExpectedActiveGenerationID, &expectedCount,
		&expectedDigest, &manifest.State)
	if expectedCount.Valid && expectedDigest.Valid {
		manifest.ExpectedChunkCount = int(expectedCount.Int64)
		manifest.ExpectedChunkDigest = expectedDigest.String
		manifest.ExpectedSealed = true
	}
	return manifest, err
}

func sameUnsealedBuildDefinition(a, b Manifest) bool {
	return a.GenerationID == b.GenerationID && a.TenantID == b.TenantID &&
		a.DocumentID == b.DocumentID && a.DocumentVersionID == b.DocumentVersionID &&
		a.ChunkerVersion == b.ChunkerVersion && a.EmbeddingModel == b.EmbeddingModel &&
		a.VectorDimension == b.VectorDimension && a.SchemaVersion == b.SchemaVersion &&
		a.CollectionVersion == b.CollectionVersion && a.IndexVersion == b.IndexVersion
}

const immutableManifestSelect = `SELECT generation_id,tenant_id,document_id,
document_version_id,chunker_version,embedding_model,vector_dimension,schema_version,
collection_version,index_version,expected_chunk_count,expected_chunk_digest,state
FROM index_manifests`

func scanImmutableManifest(row pgx.Row) (Manifest, error) {
	var manifest Manifest
	err := row.Scan(&manifest.GenerationID, &manifest.TenantID, &manifest.DocumentID,
		&manifest.DocumentVersionID, &manifest.ChunkerVersion, &manifest.EmbeddingModel,
		&manifest.VectorDimension, &manifest.SchemaVersion, &manifest.CollectionVersion,
		&manifest.IndexVersion, &manifest.ExpectedChunkCount, &manifest.ExpectedChunkDigest, &manifest.State)
	return manifest, err
}

func sameBuildDefinition(a, b Manifest) bool {
	return a.GenerationID == b.GenerationID && a.TenantID == b.TenantID &&
		a.DocumentID == b.DocumentID && a.DocumentVersionID == b.DocumentVersionID &&
		a.ChunkerVersion == b.ChunkerVersion && a.EmbeddingModel == b.EmbeddingModel &&
		a.VectorDimension == b.VectorDimension && a.SchemaVersion == b.SchemaVersion &&
		a.CollectionVersion == b.CollectionVersion && a.IndexVersion == b.IndexVersion &&
		a.ExpectedChunkCount == b.ExpectedChunkCount && a.ExpectedChunkDigest == b.ExpectedChunkDigest
}
