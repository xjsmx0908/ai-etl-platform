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
var _ ReconciliationStore = (*PostgresStore)(nil)
var _ RollbackStore = (*PostgresStore)(nil)
var _ RetentionStore = (*PostgresStore)(nil)

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
		reconcileError      string
	}
	manifests := make(map[string][]manifestRef)
	if len(docIDs) > 0 {
		rows, err := s.q.Query(ctx, `SELECT document_id,document_version_id,generation_id,state,last_reconcile_error FROM index_manifests
WHERE tenant_id=$1 AND document_id = ANY($2)`, tenantID, docIDs)
		if err != nil {
			return nil, fmt.Errorf("resolve index visibility: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var docID, versionID, generationID string
			var state ManifestState
			var reconcileError string
			if err := rows.Scan(&docID, &versionID, &generationID, &state, &reconcileError); err != nil {
				return nil, fmt.Errorf("scan index visibility: %w", err)
			}
			manifests[docID] = append(manifests[docID], manifestRef{version: versionID, generation: generationID, state: state, reconcileError: reconcileError})
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
			if entry.state == StateActive && entry.reconcileError == "" && entry.version == ref.DocumentVersionID && entry.generation == ref.GenerationID {
				visible[i] = true
				break
			}
		}
	}
	return visible, nil
}

func (s *PostgresStore) ClaimReconciliation(ctx context.Context, claim ReconciliationClaim) ([]Manifest, error) {
	if claim.Limit <= 0 {
		claim.Limit = 100
	}
	if claim.Lease <= 0 {
		claim.Lease = time.Minute
	}
	if claim.Token == "" {
		return nil, ErrInvalidManifest
	}
	rows, err := s.q.Query(ctx, `WITH candidates AS (
	SELECT generation_id FROM index_manifests
	WHERE state='active' AND (reconcile_lease_until IS NULL OR reconcile_lease_until <= now())
	ORDER BY last_reconciled_at NULLS FIRST, created_at
	FOR UPDATE SKIP LOCKED LIMIT $1
)
UPDATE index_manifests AS m SET reconcile_lease_until=now()+$2::interval,reconcile_claim_token=$3
FROM candidates AS c WHERE m.generation_id=c.generation_id
RETURNING m.generation_id,m.tenant_id,m.document_id,m.document_version_id,
m.chunker_version,m.embedding_model,m.vector_dimension,m.schema_version,
m.collection_version,m.index_version,m.expected_active_generation_id,
m.expected_chunk_count,m.expected_chunk_digest,m.state`, claim.Limit, claim.Lease.String(), claim.Token)
	if err != nil {
		return nil, fmt.Errorf("claim manifest reconciliation: %w", err)
	}
	defer rows.Close()
	manifests := make([]Manifest, 0, claim.Limit)
	for rows.Next() {
		manifest, err := scanManifest(rows)
		if err != nil {
			return nil, fmt.Errorf("scan claimed manifest: %w", err)
		}
		manifest.ReconcileClaimToken = claim.Token
		manifests = append(manifests, manifest)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claimed manifests: %w", err)
	}
	return manifests, nil
}

func (s *PostgresStore) FinishReconciliation(ctx context.Context, result ReconciliationResult, maxRepairs int) (RepairDisposition, error) {
	m := result.Manifest
	if m.GenerationID == "" || m.TenantID == "" || m.DocumentID == "" || m.DocumentVersionID == "" || m.ReconcileClaimToken == "" {
		return "", ErrInvalidManifest
	}
	if maxRepairs <= 0 {
		maxRepairs = 3
	}
	if result.ReconcileAfter <= 0 {
		result.ReconcileAfter = 5 * time.Minute
	}
	if result.Healthy {
		if !observationMatches(m, result.Qdrant) || !observationMatches(m, result.Elasticsearch) {
			return "", ErrNotReady
		}
		tag, err := s.q.Exec(ctx, `UPDATE index_manifests SET qdrant_count=$3,qdrant_digest=$4,qdrant_observed_at=now(),
elasticsearch_count=$5,elasticsearch_digest=$6,elasticsearch_observed_at=now(),
last_reconciled_at=now(),last_reconcile_error=$7,reconcile_lease_until=now()+$8::interval,
reconcile_claim_token='',repair_attempts=0
WHERE generation_id=$1 AND reconcile_claim_token=$2 AND state='active'`, m.GenerationID,
			m.ReconcileClaimToken, result.Qdrant.Count, result.Qdrant.Digest,
			result.Elasticsearch.Count, result.Elasticsearch.Digest, "", result.ReconcileAfter.String())
		if err != nil {
			return "", fmt.Errorf("finish healthy reconciliation: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return "", ErrConflict
		}
		return RepairNotNeeded, nil
	}
	if result.Reason == "" {
		return "", ErrInvalidManifest
	}
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin divergent reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var repairAttempts int
	err = tx.QueryRow(ctx, `UPDATE index_manifests SET qdrant_count=$3,qdrant_digest=NULLIF($4,''),qdrant_observed_at=now(),
elasticsearch_count=$5,elasticsearch_digest=NULLIF($6,''),elasticsearch_observed_at=now(),
last_reconciled_at=now(),last_reconcile_error=$7,reconcile_lease_until=now()+$8::interval,reconcile_claim_token=''
WHERE generation_id=$1 AND reconcile_claim_token=$2 AND state='active'
RETURNING repair_attempts`, m.GenerationID, m.ReconcileClaimToken, result.Qdrant.Count,
		result.Qdrant.Digest, result.Elasticsearch.Count, result.Elasticsearch.Digest, result.Reason,
		result.ReconcileAfter.String()).Scan(&repairAttempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrConflict
	}
	if err != nil {
		return "", fmt.Errorf("record divergent reconciliation: %w", err)
	}
	if repairAttempts >= maxRepairs {
		if err := tx.Commit(ctx); err != nil {
			return "", fmt.Errorf("commit exhausted reconciliation: %w", err)
		}
		return RepairExhausted, nil
	}
	var status string
	var pending bool
	err = tx.QueryRow(ctx, `SELECT j.status,o.published_at IS NULL
FROM ingestion_jobs AS j JOIN ingestion_outbox AS o ON o.job_id=j.job_id
WHERE j.job_id=$1 AND j.tenant_id=$2 AND j.doc_id=$3 FOR UPDATE OF j,o`,
		m.DocumentVersionID, m.TenantID, m.DocumentID).Scan(&status, &pending)
	if err != nil {
		return "", fmt.Errorf("load repair ingestion job: %w", err)
	}
	if pending || status == "published" || status == "processing" || status == "queued" {
		if err := tx.Commit(ctx); err != nil {
			return "", fmt.Errorf("commit pending reconciliation: %w", err)
		}
		return RepairPending, nil
	}
	if status != "completed" && status != "failed" {
		return "", fmt.Errorf("%w: cannot repair ingestion state %s", ErrConflict, status)
	}
	tag, err := tx.Exec(ctx, `UPDATE ingestion_jobs SET status='published',lease_until=NULL,completed_at=NULL,error='',updated_at=now()
WHERE job_id=$1 AND tenant_id=$2 AND doc_id=$3 AND status IN ('completed','failed')`,
		m.DocumentVersionID, m.TenantID, m.DocumentID)
	if err != nil || tag.RowsAffected() != 1 {
		if err == nil {
			err = ErrConflict
		}
		return "", fmt.Errorf("reopen repair ingestion job: %w", err)
	}
	tag, err = tx.Exec(ctx, `UPDATE ingestion_outbox SET published_at=NULL,claimed_at=NULL,available_at=now()
WHERE job_id=$1 AND tenant_id=$2 AND doc_id=$3`, m.DocumentVersionID, m.TenantID, m.DocumentID)
	if err != nil || tag.RowsAffected() != 1 {
		if err == nil {
			err = ErrConflict
		}
		return "", fmt.Errorf("reopen repair outbox: %w", err)
	}
	tag, err = tx.Exec(ctx, `UPDATE index_manifests SET repair_attempts=repair_attempts+1 WHERE generation_id=$1`, m.GenerationID)
	if err != nil || tag.RowsAffected() != 1 {
		if err == nil {
			err = ErrConflict
		}
		return "", fmt.Errorf("count manifest repair: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit manifest repair: %w", err)
	}
	return RepairScheduled, nil
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

func (s *PostgresStore) ConfirmActive(ctx context.Context, generationID string, qdrant, elasticsearch BackendObservation) error {
	if generationID == "" || qdrant.Count < 0 || qdrant.Digest == "" || elasticsearch.Count < 0 || elasticsearch.Digest == "" {
		return ErrInvalidManifest
	}
	tag, err := s.q.Exec(ctx, `UPDATE index_manifests SET
qdrant_count=$2,qdrant_digest=$3,qdrant_observed_at=now(),
elasticsearch_count=$4,elasticsearch_digest=$5,elasticsearch_observed_at=now(),
last_reconcile_error='',last_reconciled_at=now(),repair_attempts=0,reconcile_claim_token=''
WHERE generation_id=$1 AND state='active'
AND expected_chunk_count=$2 AND expected_chunk_digest=$3
AND expected_chunk_count=$4 AND expected_chunk_digest=$5`, generationID,
		qdrant.Count, qdrant.Digest, elasticsearch.Count, elasticsearch.Digest)
	if err != nil {
		return fmt.Errorf("confirm active generation repair: %w", err)
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
		tag, err := tx.Exec(ctx, `UPDATE index_manifests SET state='retired',retired_at=now(),
reconcile_claim_token='',reconcile_lease_until=NULL
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
	tag, err := tx.Exec(ctx, `UPDATE index_manifests SET state='active',activated_at=now(),retired_at=NULL
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

func (s *PostgresStore) RollbackTarget(ctx context.Context, request RollbackRequest) (Manifest, error) {
	v := request.Version
	if v.TenantID == "" || v.DocumentID == "" || v.DocumentVersionID == "" ||
		request.TargetGenerationID == "" || request.ExpectedActiveGenerationID == "" ||
		request.TargetGenerationID == request.ExpectedActiveGenerationID || request.Window <= 0 ||
		request.Lease <= 0 || request.ClaimToken == "" {
		return Manifest{}, ErrInvalidManifest
	}
	row := s.q.QueryRow(ctx, `UPDATE index_manifests SET retention_lease_until=now()+$7::interval,retention_claim_token=$8
WHERE generation_id=$1 AND tenant_id=$2 AND document_id=$3 AND document_version_id=$4
AND state='retired' AND retired_at >= now()-$6::interval
AND EXISTS (SELECT 1 FROM index_manifests active WHERE active.tenant_id=$2
AND active.document_id=$3 AND active.document_version_id=$4
AND active.generation_id=$5 AND active.state='active')
AND qdrant_deleted_at IS NULL AND elasticsearch_deleted_at IS NULL
AND (retention_lease_until IS NULL OR retention_lease_until <= now())
RETURNING generation_id,tenant_id,document_id,document_version_id,chunker_version,
embedding_model,vector_dimension,schema_version,collection_version,index_version,
expected_active_generation_id,expected_chunk_count,expected_chunk_digest,state`, request.TargetGenerationID,
		v.TenantID, v.DocumentID, v.DocumentVersionID, request.ExpectedActiveGenerationID,
		request.Window.String(), request.Lease.String(), request.ClaimToken)
	manifest, err := scanManifest(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Manifest{}, ErrConflict
	}
	if err != nil {
		return Manifest{}, fmt.Errorf("protect rollback target: %w", err)
	}
	manifest.RetentionClaimToken = request.ClaimToken
	return manifest, nil
}

func (s *PostgresStore) Rollback(ctx context.Context, commit RollbackCommit) error {
	r := commit.Request
	v := r.Version
	if v.TenantID == "" || v.DocumentID == "" || v.DocumentVersionID == "" ||
		r.TargetGenerationID == "" || r.ExpectedActiveGenerationID == "" || r.ClaimToken == "" ||
		commit.Qdrant.Count < 0 || commit.Qdrant.Digest == "" ||
		commit.Elasticsearch.Count < 0 || commit.Elasticsearch.Digest == "" {
		return ErrInvalidManifest
	}
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin generation rollback: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	lockID := v.TenantID + "/" + v.DocumentVersionID
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockID); err != nil {
		return fmt.Errorf("lock generation rollback: %w", err)
	}
	var current string
	err = tx.QueryRow(ctx, `SELECT generation_id FROM index_manifests
WHERE tenant_id=$1 AND document_id=$2 AND document_version_id=$3 AND state='active'`,
		v.TenantID, v.DocumentID, v.DocumentVersionID).Scan(&current)
	if err != nil {
		return fmt.Errorf("load active generation for rollback: %w", err)
	}
	if current != r.ExpectedActiveGenerationID {
		return ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE index_manifests SET state='retired',retired_at=now(),
reconcile_claim_token='',reconcile_lease_until=NULL
WHERE tenant_id=$1 AND document_id=$2 AND document_version_id=$3
AND generation_id=$4 AND state='active'`, v.TenantID, v.DocumentID, v.DocumentVersionID, current)
	if err != nil {
		return fmt.Errorf("retire current generation for rollback: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	tag, err = tx.Exec(ctx, `UPDATE index_manifests SET state='active',activated_at=now(),retired_at=NULL,
qdrant_count=$6,qdrant_digest=$7,qdrant_observed_at=now(),
elasticsearch_count=$8,elasticsearch_digest=$9,elasticsearch_observed_at=now(),
last_reconcile_error='',last_reconciled_at=now(),repair_attempts=0,
reconcile_claim_token='',reconcile_lease_until=NULL,
retention_claim_token='',retention_lease_until=NULL
WHERE tenant_id=$1 AND document_id=$2 AND document_version_id=$3
AND generation_id=$4 AND state='retired' AND retention_claim_token=$5
AND retention_lease_until > now()
AND retired_at >= now()-$10::interval
AND expected_chunk_count=$6 AND expected_chunk_digest=$7
AND expected_chunk_count=$8 AND expected_chunk_digest=$9`, v.TenantID, v.DocumentID,
		v.DocumentVersionID, r.TargetGenerationID, r.ClaimToken,
		commit.Qdrant.Count, commit.Qdrant.Digest, commit.Elasticsearch.Count, commit.Elasticsearch.Digest,
		r.Window.String())
	if err != nil {
		return fmt.Errorf("promote rollback generation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit generation rollback: %w", err)
	}
	return nil
}

func (s *PostgresStore) ClaimRetention(ctx context.Context, claim RetentionClaim) ([]Manifest, error) {
	if claim.Window <= 0 || claim.Token == "" {
		return nil, ErrInvalidManifest
	}
	if claim.Limit <= 0 {
		claim.Limit = 100
	}
	if claim.Lease <= 0 {
		claim.Lease = 5 * time.Minute
	}
	rows, err := s.q.Query(ctx, `WITH candidates AS (
	SELECT generation_id FROM index_manifests
	WHERE state='retired' AND retired_at < now()-$1::interval
	AND (retention_lease_until IS NULL OR retention_lease_until <= now())
	ORDER BY retired_at,created_at FOR UPDATE SKIP LOCKED LIMIT $2
)
UPDATE index_manifests AS m SET retention_lease_until=now()+$3::interval,retention_claim_token=$4
FROM candidates AS c WHERE m.generation_id=c.generation_id
RETURNING m.generation_id,m.tenant_id,m.document_id,m.document_version_id,
m.chunker_version,m.embedding_model,m.vector_dimension,m.schema_version,
m.collection_version,m.index_version,m.expected_active_generation_id,
m.expected_chunk_count,m.expected_chunk_digest,m.state,
m.qdrant_deleted_at,m.elasticsearch_deleted_at`, claim.Window.String(), claim.Limit,
		claim.Lease.String(), claim.Token)
	if err != nil {
		return nil, fmt.Errorf("claim retained generations: %w", err)
	}
	defer rows.Close()
	manifests := make([]Manifest, 0, claim.Limit)
	for rows.Next() {
		manifest, err := scanRetentionManifest(rows)
		if err != nil {
			return nil, fmt.Errorf("scan retained generation: %w", err)
		}
		manifest.RetentionClaimToken = claim.Token
		manifests = append(manifests, manifest)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate retained generations: %w", err)
	}
	return manifests, nil
}

func (s *PostgresStore) FinishRetention(ctx context.Context, result RetentionResult) error {
	m := result.Manifest
	if m.GenerationID == "" || m.RetentionClaimToken == "" {
		return ErrInvalidManifest
	}
	if result.QdrantDeleted && result.ElasticsearchDeleted && result.Reason == "" {
		tag, err := s.q.Exec(ctx, `DELETE FROM index_manifests
WHERE generation_id=$1 AND retention_claim_token=$2 AND state='retired'
AND retention_lease_until > now()`, m.GenerationID, m.RetentionClaimToken)
		if err != nil {
			return fmt.Errorf("delete retained manifest: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return nil
	}
	if result.Reason == "" {
		return ErrInvalidManifest
	}
	if result.RetryAfter <= 0 {
		result.RetryAfter = time.Hour
	}
	tag, err := s.q.Exec(ctx, `UPDATE index_manifests SET
qdrant_deleted_at=CASE WHEN $3 THEN COALESCE(qdrant_deleted_at,now()) ELSE qdrant_deleted_at END,
elasticsearch_deleted_at=CASE WHEN $4 THEN COALESCE(elasticsearch_deleted_at,now()) ELSE elasticsearch_deleted_at END,
retention_last_error=$5,retention_attempts=retention_attempts+1,
retention_lease_until=now()+$6::interval,retention_claim_token=''
WHERE generation_id=$1 AND retention_claim_token=$2 AND state='retired'
AND retention_lease_until > now()`, m.GenerationID,
		m.RetentionClaimToken, result.QdrantDeleted, result.ElasticsearchDeleted,
		result.Reason, result.RetryAfter.String())
	if err != nil {
		return fmt.Errorf("record retained generation cleanup: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

// OperationsSnapshot returns current generation health without resource-level
// dimensions, keeping downstream metrics bounded as tenants and documents grow.
func (s *PostgresStore) OperationsSnapshot(ctx context.Context, maxRepairs int) (OperationsSnapshot, error) {
	if maxRepairs < 1 {
		return OperationsSnapshot{}, ErrInvalidManifest
	}
	snapshot := OperationsSnapshot{
		Manifests: make(map[ManifestState]int, 5),
		OldestAge: make(map[ManifestState]time.Duration, 5),
	}
	rows, err := s.q.Query(ctx, `SELECT state,count(*),
COALESCE(EXTRACT(EPOCH FROM max(now()-created_at)),0) AS oldest_age_seconds
FROM index_manifests GROUP BY state`)
	if err != nil {
		return OperationsSnapshot{}, fmt.Errorf("snapshot generation states: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var state ManifestState
		var count int
		var oldestSeconds float64
		if err := rows.Scan(&state, &count, &oldestSeconds); err != nil {
			return OperationsSnapshot{}, fmt.Errorf("scan generation state snapshot: %w", err)
		}
		snapshot.Manifests[state] = count
		snapshot.OldestAge[state] = time.Duration(oldestSeconds * float64(time.Second))
	}
	if err := rows.Err(); err != nil {
		return OperationsSnapshot{}, fmt.Errorf("iterate generation state snapshot: %w", err)
	}
	err = s.q.QueryRow(ctx, `SELECT
	count(*) FILTER (WHERE state='active' AND last_reconcile_error<>'') AS backend_diverged,
count(*) FILTER (WHERE state='active' AND last_reconcile_error<>'' AND repair_attempts >= $1) AS repair_exhausted,
count(*) FILTER (WHERE state='retired' AND retention_last_error<>'') AS retention_failed
FROM index_manifests`, maxRepairs).Scan(
		&snapshot.BackendDiverged, &snapshot.RepairExhausted, &snapshot.RetentionFailed,
	)
	if err != nil {
		return OperationsSnapshot{}, fmt.Errorf("snapshot generation diagnostics: %w", err)
	}
	return snapshot, nil
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

func scanRetentionManifest(row pgx.Row) (Manifest, error) {
	var manifest Manifest
	var expectedCount sql.NullInt64
	var expectedDigest sql.NullString
	var qdrantDeletedAt, elasticsearchDeletedAt sql.NullTime
	err := row.Scan(&manifest.GenerationID, &manifest.TenantID, &manifest.DocumentID,
		&manifest.DocumentVersionID, &manifest.ChunkerVersion, &manifest.EmbeddingModel,
		&manifest.VectorDimension, &manifest.SchemaVersion, &manifest.CollectionVersion,
		&manifest.IndexVersion, &manifest.ExpectedActiveGenerationID, &expectedCount,
		&expectedDigest, &manifest.State, &qdrantDeletedAt, &elasticsearchDeletedAt)
	if expectedCount.Valid && expectedDigest.Valid {
		manifest.ExpectedChunkCount = int(expectedCount.Int64)
		manifest.ExpectedChunkDigest = expectedDigest.String
		manifest.ExpectedSealed = true
	}
	if qdrantDeletedAt.Valid {
		manifest.QdrantDeletedAt = qdrantDeletedAt.Time
	}
	if elasticsearchDeletedAt.Valid {
		manifest.ElasticsearchDeletedAt = elasticsearchDeletedAt.Time
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
