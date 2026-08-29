package indexmanifest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	pgxmock "github.com/pashagolub/pgxmock/v5"
)

func testManifest() Manifest {
	return Manifest{
		GenerationID: "gen-1", TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1",
		ChunkerVersion: "chunker-v1", EmbeddingModel: "embed-v1", VectorDimension: 3,
		SchemaVersion: "schema-v1", CollectionVersion: "collection-v1", IndexVersion: "index-v1",
		ExpectedChunkCount: 2, ExpectedChunkDigest: "sha256:digest", State: StateBuilding,
		CreatedAt: time.Date(2026, 8, 28, 1, 0, 0, 0, time.UTC),
	}
}

func immutableManifestRow(m Manifest) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"generation_id", "tenant_id", "document_id", "document_version_id", "chunker_version",
		"embedding_model", "vector_dimension", "schema_version", "collection_version",
		"index_version", "expected_chunk_count", "expected_chunk_digest", "state",
	}).AddRow(m.GenerationID, m.TenantID, m.DocumentID, m.DocumentVersionID, m.ChunkerVersion,
		m.EmbeddingModel, m.VectorDimension, m.SchemaVersion, m.CollectionVersion,
		m.IndexVersion, m.ExpectedChunkCount, m.ExpectedChunkDigest, m.State)
}

func immutableManifestRowNullable(m Manifest, count any, digest any) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"generation_id", "tenant_id", "document_id", "document_version_id", "chunker_version",
		"embedding_model", "vector_dimension", "schema_version", "collection_version",
		"index_version", "expected_active_generation_id", "expected_chunk_count", "expected_chunk_digest", "state",
	}).AddRow(m.GenerationID, m.TenantID, m.DocumentID, m.DocumentVersionID, m.ChunkerVersion,
		m.EmbeddingModel, m.VectorDimension, m.SchemaVersion, m.CollectionVersion,
		m.IndexVersion, m.ExpectedActiveGenerationID, count, digest, m.State)
}

func TestPostgresStoreBeginsBeforeExpectedIdentityAndSealsIdempotently(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	m := testManifest()
	m.ExpectedChunkCount, m.ExpectedChunkDigest, m.ExpectedSealed = 0, "", false
	mock.ExpectQuery("SELECT generation_id").WithArgs(m.TenantID, m.DocumentID, m.DocumentVersionID).WillReturnError(pgx.ErrNoRows)
	mock.ExpectExec("INSERT INTO index_manifests").WithArgs(
		m.GenerationID, m.TenantID, m.DocumentID, m.DocumentVersionID, m.ChunkerVersion,
		m.EmbeddingModel, m.VectorDimension, m.SchemaVersion, m.CollectionVersion,
		m.IndexVersion, "", m.CreatedAt,
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectQuery("SELECT generation_id").WithArgs(m.GenerationID).WillReturnRows(immutableManifestRowNullable(m, nil, nil))
	store := NewPostgresStore(mock)
	got, err := store.Begin(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpectedSealed {
		t.Fatal("new manifest unexpectedly sealed")
	}
	for attempt := range 2 {
		rowsAffected := int64(1)
		if attempt > 0 {
			rowsAffected = 0
		}
		mock.ExpectExec("UPDATE index_manifests").WithArgs(m.GenerationID, 2, "sha256:digest").WillReturnResult(pgxmock.NewResult("UPDATE", rowsAffected))
		mock.ExpectQuery("SELECT generation_id").WithArgs(m.GenerationID).WillReturnRows(immutableManifestRowNullable(m, 2, "sha256:digest"))
		got, err = store.SealExpected(context.Background(), m.GenerationID, 2, "sha256:digest")
		if err != nil {
			t.Fatal(err)
		}
	}
	if !got.ExpectedSealed || got.ExpectedChunkCount != 2 {
		t.Fatalf("sealed manifest = %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreBeginReplayKeepsPersistedActivationPredecessor(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	m := testManifest()
	m.ExpectedChunkCount, m.ExpectedChunkDigest = 0, ""
	existing := m
	existing.ExpectedActiveGenerationID = "gen-old"
	existing.State = StateReady
	// A newer generation is active when this delayed build is replayed. The
	// insert conflicts, and the original predecessor remains authoritative.
	mock.ExpectQuery("SELECT generation_id").WithArgs(m.TenantID, m.DocumentID, m.DocumentVersionID).
		WillReturnRows(pgxmock.NewRows([]string{"generation_id"}).AddRow("gen-new"))
	mock.ExpectExec("INSERT INTO index_manifests").WithArgs(
		m.GenerationID, m.TenantID, m.DocumentID, m.DocumentVersionID, m.ChunkerVersion,
		m.EmbeddingModel, m.VectorDimension, m.SchemaVersion, m.CollectionVersion,
		m.IndexVersion, "gen-new", m.CreatedAt,
	).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectQuery("SELECT generation_id").WithArgs(m.GenerationID).
		WillReturnRows(immutableManifestRowNullable(existing, 2, "sha256:digest"))

	got, err := NewPostgresStore(mock).Begin(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpectedActiveGenerationID != "gen-old" {
		t.Fatalf("predecessor = %q, want persisted gen-old", got.ExpectedActiveGenerationID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreEnsureIsIdempotentForIdenticalManifest(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	m := testManifest()
	for range 2 {
		mock.ExpectExec("INSERT INTO index_manifests").WithArgs(
			m.GenerationID, m.TenantID, m.DocumentID, m.DocumentVersionID, m.ChunkerVersion,
			m.EmbeddingModel, m.VectorDimension, m.SchemaVersion, m.CollectionVersion,
			m.IndexVersion, m.ExpectedChunkCount, m.ExpectedChunkDigest, StateBuilding, m.CreatedAt,
		).WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectQuery("SELECT generation_id").WithArgs(m.GenerationID).WillReturnRows(immutableManifestRow(m))
		if err := NewPostgresStore(mock).Ensure(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreEnsureRejectsConflictingReplay(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	want := testManifest()
	existing := want
	existing.EmbeddingModel = "other-model"
	mock.ExpectExec("INSERT INTO index_manifests").WithArgs(
		want.GenerationID, want.TenantID, want.DocumentID, want.DocumentVersionID, want.ChunkerVersion,
		want.EmbeddingModel, want.VectorDimension, want.SchemaVersion, want.CollectionVersion,
		want.IndexVersion, want.ExpectedChunkCount, want.ExpectedChunkDigest, StateBuilding, want.CreatedAt,
	).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	mock.ExpectQuery("SELECT generation_id").WithArgs(want.GenerationID).WillReturnRows(immutableManifestRow(existing))
	if err := NewPostgresStore(mock).Ensure(context.Background(), want); !errors.Is(err, ErrConflict) {
		t.Fatalf("Ensure error=%v, want ErrConflict", err)
	}
}

func TestPostgresStoreRejectsIncompleteBuildIdentity(t *testing.T) {
	for name, mutate := range map[string]func(*Manifest){
		"chunker":    func(m *Manifest) { m.ChunkerVersion = "" },
		"embedding":  func(m *Manifest) { m.EmbeddingModel = "" },
		"dimension":  func(m *Manifest) { m.VectorDimension = 0 },
		"schema":     func(m *Manifest) { m.SchemaVersion = "" },
		"collection": func(m *Manifest) { m.CollectionVersion = "" },
		"index":      func(m *Manifest) { m.IndexVersion = "" },
	} {
		t.Run(name, func(t *testing.T) {
			m := testManifest()
			mutate(&m)
			if err := NewPostgresStore(nil).Ensure(context.Background(), m); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("Ensure error=%v", err)
			}
		})
	}
}

func TestPostgresStoreObserveFailRetryAndReadyLifecycle(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	store := NewPostgresStore(mock)
	mock.ExpectExec("UPDATE index_manifests").WithArgs("gen-1", 2, "sha256:digest").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if err := store.Observe(context.Background(), "gen-1", BackendQdrant, BackendObservation{Count: 2, Digest: "sha256:digest"}); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("UPDATE index_manifests").WithArgs("gen-1", "elasticsearch unavailable").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if err := store.Fail(context.Background(), "gen-1", "elasticsearch unavailable"); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("UPDATE index_manifests").WithArgs("gen-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if err := store.Retry(context.Background(), "gen-1"); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("UPDATE index_manifests").WithArgs("gen-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if err := store.MarkReady(context.Background(), "gen-1"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreConfirmsMatchingActiveGenerationRepair(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	qdrant := BackendObservation{Count: 2, Digest: "sha256:digest"}
	elasticsearch := BackendObservation{Count: 2, Digest: "sha256:digest"}
	mock.ExpectExec("UPDATE index_manifests SET.*last_reconcile_error='',last_reconciled_at=now\\(\\),repair_attempts=0,reconcile_claim_token=''").
		WithArgs("gen-1", qdrant.Count, qdrant.Digest, elasticsearch.Count, elasticsearch.Digest).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = NewPostgresStore(mock).ConfirmActive(context.Background(), "gen-1", qdrant, elasticsearch)
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreRejectsUnverifiedActiveGenerationRepair(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectExec("UPDATE index_manifests SET").
		WithArgs("gen-1", 1, "wrong", 2, "sha256:digest").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err = NewPostgresStore(mock).ConfirmActive(context.Background(), "gen-1",
		BackendObservation{Count: 1, Digest: "wrong"},
		BackendObservation{Count: 2, Digest: "sha256:digest"})
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("ConfirmActive error=%v, want ErrNotReady", err)
	}
}

func TestPostgresStoreRejectsMismatchedHealthyReconciliation(t *testing.T) {
	m := testManifest()
	m.State, m.ReconcileClaimToken = StateActive, "claim-1"
	_, err := NewPostgresStore(nil).FinishReconciliation(context.Background(), ReconciliationResult{
		Manifest: m, Healthy: true, ReconcileAfter: 5 * time.Minute,
		Qdrant:        BackendObservation{Count: 1, Digest: "wrong"},
		Elasticsearch: BackendObservation{Count: 2, Digest: "sha256:digest"},
	}, 3)
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("FinishReconciliation error=%v, want ErrNotReady", err)
	}
}

func TestPostgresStorePropagatesActiveGenerationConfirmationFailure(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	dbErr := errors.New("database unavailable")
	mock.ExpectExec("UPDATE index_manifests SET").
		WithArgs("gen-1", 2, "sha256:digest", 2, "sha256:digest").
		WillReturnError(dbErr)

	err = NewPostgresStore(mock).ConfirmActive(context.Background(), "gen-1",
		BackendObservation{Count: 2, Digest: "sha256:digest"},
		BackendObservation{Count: 2, Digest: "sha256:digest"})
	if !errors.Is(err, dbErr) {
		t.Fatalf("ConfirmActive error=%v, want wrapped database error", err)
	}
}

func TestPostgresStoreActivationComparesExpectedCurrentGeneration(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	target := ActivationTarget{
		Version:      VersionIdentity{TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1"},
		GenerationID: "gen-2", ExpectedActiveGenerationID: "gen-1",
	}
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("acme/job-1").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT generation_id").WithArgs("acme", "doc-1", "job-1").WillReturnRows(pgxmock.NewRows([]string{"generation_id"}).AddRow("gen-1"))
	mock.ExpectExec("UPDATE index_manifests").WithArgs("acme", "doc-1", "job-1", "gen-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE index_manifests").WithArgs("acme", "doc-1", "job-1", "gen-2").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	if err := NewPostgresStore(mock).Activate(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreActivationRejectsStaleWriterWithoutRetiringCurrent(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	target := ActivationTarget{
		Version:      VersionIdentity{TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1"},
		GenerationID: "old-delayed", ExpectedActiveGenerationID: "gen-1",
	}
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("acme/job-1").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT generation_id").WithArgs("acme", "doc-1", "job-1").WillReturnRows(pgxmock.NewRows([]string{"generation_id"}).AddRow("gen-2"))
	mock.ExpectRollback()
	err = NewPostgresStore(mock).Activate(context.Background(), target)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Activate error=%v, want ErrConflict", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreActivationAcceptsExpectedNoActiveGeneration(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	target := ActivationTarget{Version: VersionIdentity{TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1"}, GenerationID: "gen-1"}
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("acme/job-1").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT generation_id").WithArgs("acme", "doc-1", "job-1").WillReturnError(pgx.ErrNoRows)
	mock.ExpectExec("UPDATE index_manifests").WithArgs("acme", "doc-1", "job-1", "gen-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	if err := NewPostgresStore(mock).Activate(context.Background(), target); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreProtectsRetiredRollbackTargetWithinWindow(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	m := testManifest()
	m.GenerationID, m.State = "gen-retired", StateRetired
	m.ActivatedAt = time.Now().UTC().Add(-24 * time.Hour)
	request := RollbackRequest{
		Version:            VersionIdentity{TenantID: m.TenantID, DocumentID: m.DocumentID, DocumentVersionID: m.DocumentVersionID},
		TargetGenerationID: m.GenerationID, ExpectedActiveGenerationID: "gen-active",
		Window: 7 * 24 * time.Hour, Lease: 5 * time.Minute, ClaimToken: "retention-1",
	}
	mock.ExpectQuery("UPDATE index_manifests SET retention_lease_until").WithArgs(
		m.GenerationID, m.TenantID, m.DocumentID, m.DocumentVersionID,
		"gen-active", "168h0m0s", "5m0s", "retention-1",
	).WillReturnRows(immutableManifestRowNullable(m, m.ExpectedChunkCount, m.ExpectedChunkDigest))

	got, err := NewPostgresStore(mock).RollbackTarget(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if got.GenerationID != m.GenerationID || got.RetentionClaimToken != request.ClaimToken {
		t.Fatalf("target = %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreRollsBackWithExpectedActiveAndFencedTarget(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	request := RollbackRequest{
		Version:            VersionIdentity{TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1"},
		TargetGenerationID: "gen-retired", ExpectedActiveGenerationID: "gen-active",
		Window: 7 * 24 * time.Hour, Lease: 5 * time.Minute, ClaimToken: "retention-1",
	}
	commit := RollbackCommit{Request: request,
		Qdrant:        BackendObservation{Count: 2, Digest: "sha256:digest"},
		Elasticsearch: BackendObservation{Count: 2, Digest: "sha256:digest"},
	}
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("acme/job-1").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT generation_id").WithArgs("acme", "doc-1", "job-1").WillReturnRows(pgxmock.NewRows([]string{"generation_id"}).AddRow("gen-active"))
	mock.ExpectExec("UPDATE index_manifests SET state='retired'").WithArgs("acme", "doc-1", "job-1", "gen-active").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE index_manifests SET state='active'").WithArgs(
		"acme", "doc-1", "job-1", "gen-retired", "retention-1",
		2, "sha256:digest", 2, "sha256:digest", "168h0m0s",
	).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	if err := NewPostgresStore(mock).Rollback(context.Background(), commit); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreResolvesOnlyActiveGeneration(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	version := VersionIdentity{TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1"}
	mock.ExpectQuery("SELECT generation_id").WithArgs("acme", "doc-1", "job-1").WillReturnRows(pgxmock.NewRows([]string{"generation_id"}).AddRow("gen-2"))
	generationID, found, err := NewPostgresStore(mock).ActiveGeneration(context.Background(), version)
	if err != nil {
		t.Fatal(err)
	}
	if !found || generationID != "gen-2" {
		t.Fatalf("generation=%q found=%v", generationID, found)
	}
}

func TestPostgresStoreResolvesCandidateVisibilityInOneBatch(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT document_id,document_version_id,generation_id,state,last_reconcile_error FROM index_manifests").
		WithArgs("acme", []string{"doc-active", "doc-managed", "doc-unmanaged"}).
		WillReturnRows(pgxmock.NewRows([]string{"document_id", "document_version_id", "generation_id", "state", "last_reconcile_error"}).
			AddRow("doc-active", "job-1", "gen-active", StateActive, "").
			AddRow("doc-active", "job-1", "gen-unhealthy", StateActive, "projection identity mismatch").
			AddRow("doc-active", "job-1", "gen-retired", StateRetired, "").
			AddRow("doc-managed", "job-2", "gen-building", StateBuilding, "").
			AddRow("doc-managed", "job-2", "gen-ready", StateReady, "").
			AddRow("doc-managed", "job-2", "gen-failed", StateFailed, ""))

	refs := []GenerationReference{
		{DocumentID: "doc-active", DocumentVersionID: "job-1", GenerationID: "gen-active"},
		{DocumentID: "doc-active", DocumentVersionID: "job-1", GenerationID: "gen-unhealthy"},
		{DocumentID: "doc-active", DocumentVersionID: "job-1", GenerationID: "gen-retired"},
		{DocumentID: "doc-managed", DocumentVersionID: "job-2", GenerationID: "gen-building"},
		{DocumentID: "doc-managed", DocumentVersionID: "job-2", GenerationID: "gen-ready"},
		{DocumentID: "doc-managed", DocumentVersionID: "job-2", GenerationID: "gen-failed"},
		{DocumentID: "doc-managed"},
		{DocumentID: "doc-unmanaged"},
		{DocumentID: "doc-active", GenerationID: "gen-active"},
		{},
	}
	got, err := NewPostgresStore(mock).ResolveVisibility(context.Background(), "acme", refs)
	if err != nil {
		t.Fatal(err)
	}
	want := []bool{true, false, false, false, false, false, false, true, false, false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visibility = %v, want %v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreClaimsActiveManifestsFairlyWithLease(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	m := testManifest()
	m.State = StateActive
	m.ExpectedActiveGenerationID = "gen-old"
	m.ExpectedSealed = true
	mock.ExpectQuery("WITH candidates AS.*state='active'.*FOR UPDATE SKIP LOCKED.*UPDATE index_manifests").
		WithArgs(25, "45s", "claim-1").
		WillReturnRows(immutableManifestRowNullable(m, m.ExpectedChunkCount, m.ExpectedChunkDigest))

	got, err := NewPostgresStore(mock).ClaimReconciliation(context.Background(), ReconciliationClaim{Limit: 25, Lease: 45 * time.Second, Token: "claim-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GenerationID != m.GenerationID || got[0].State != StateActive || got[0].ReconcileClaimToken != "claim-1" {
		t.Fatalf("claimed manifests = %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreClaimsOnlyExpiredRetiredGenerations(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	m := testManifest()
	m.GenerationID, m.State = "gen-old", StateRetired
	m.ExpectedSealed = true
	mock.ExpectQuery("WITH candidates AS.*state='retired'.*retired_at < now\\(\\)-.*FOR UPDATE SKIP LOCKED.*UPDATE index_manifests").
		WithArgs("168h0m0s", 25, "5m0s", "retention-1").WillReturnRows(
		pgxmock.NewRows([]string{
			"generation_id", "tenant_id", "document_id", "document_version_id", "chunker_version",
			"embedding_model", "vector_dimension", "schema_version", "collection_version", "index_version",
			"expected_active_generation_id", "expected_chunk_count", "expected_chunk_digest", "state",
			"qdrant_deleted_at", "elasticsearch_deleted_at",
		}).AddRow(m.GenerationID, m.TenantID, m.DocumentID, m.DocumentVersionID, m.ChunkerVersion,
			m.EmbeddingModel, m.VectorDimension, m.SchemaVersion, m.CollectionVersion, m.IndexVersion,
			m.ExpectedActiveGenerationID, m.ExpectedChunkCount, m.ExpectedChunkDigest, m.State, nil, nil))

	got, err := NewPostgresStore(mock).ClaimRetention(context.Background(), RetentionClaim{
		Window: 7 * 24 * time.Hour, Limit: 25, Lease: 5 * time.Minute, Token: "retention-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GenerationID != m.GenerationID || got[0].RetentionClaimToken != "retention-1" {
		t.Fatalf("claimed=%+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStorePersistsPartialRetentionProgress(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	m := testManifest()
	m.GenerationID, m.State, m.RetentionClaimToken = "gen-old", StateRetired, "retention-1"
	result := RetentionResult{Manifest: m, QdrantDeleted: true, Reason: "delete elasticsearch: unavailable", RetryAfter: time.Hour}
	mock.ExpectExec("UPDATE index_manifests SET.*qdrant_deleted_at=CASE.*retention_last_error").WithArgs(
		m.GenerationID, m.RetentionClaimToken, true, false, result.Reason, "1h0m0s",
	).WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	if err := NewPostgresStore(mock).FinishRetention(context.Background(), result); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreDeletesManifestOnlyAfterBothProjectionDeletes(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	m := testManifest()
	m.GenerationID, m.State, m.RetentionClaimToken = "gen-old", StateRetired, "retention-1"
	mock.ExpectExec("DELETE FROM index_manifests").WithArgs(m.GenerationID, m.RetentionClaimToken).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	if err := NewPostgresStore(mock).FinishRetention(context.Background(), RetentionResult{
		Manifest: m, QdrantDeleted: true, ElasticsearchDeleted: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreFinishesHealthyReconciliationWithoutReplay(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	m := testManifest()
	m.State, m.ReconcileClaimToken = StateActive, "claim-1"
	result := ReconciliationResult{Manifest: m, Healthy: true,
		Qdrant:         BackendObservation{Count: 2, Digest: "sha256:digest"},
		Elasticsearch:  BackendObservation{Count: 2, Digest: "sha256:digest"},
		ReconcileAfter: 5 * time.Minute,
	}
	mock.ExpectExec("UPDATE index_manifests SET qdrant_count").WithArgs(
		m.GenerationID, m.ReconcileClaimToken, 2, "sha256:digest", 2, "sha256:digest", "", "5m0s",
	).WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	got, err := NewPostgresStore(mock).FinishReconciliation(context.Background(), result, 3)
	if err != nil || got != RepairNotNeeded {
		t.Fatalf("FinishReconciliation = (%q,%v)", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreSchedulesOneAtomicRepairReplay(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	m := testManifest()
	m.State, m.ReconcileClaimToken = StateActive, "claim-1"
	result := ReconciliationResult{Manifest: m, Reason: "projection identity mismatch",
		Qdrant:         BackendObservation{Count: 1, Digest: "wrong"},
		Elasticsearch:  BackendObservation{Count: 2, Digest: "sha256:digest"},
		ReconcileAfter: 5 * time.Minute,
	}
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE index_manifests SET qdrant_count").WithArgs(
		m.GenerationID, m.ReconcileClaimToken, 1, "wrong", 2, "sha256:digest", result.Reason, "5m0s",
	).WillReturnRows(pgxmock.NewRows([]string{"repair_attempts"}).AddRow(0))
	mock.ExpectQuery("SELECT j.status,o.published_at IS NULL").WithArgs(
		m.DocumentVersionID, m.TenantID, m.DocumentID,
	).WillReturnRows(pgxmock.NewRows([]string{"status", "pending"}).AddRow("completed", false))
	mock.ExpectExec("UPDATE ingestion_jobs SET status='published'").WithArgs(
		m.DocumentVersionID, m.TenantID, m.DocumentID,
	).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE ingestion_outbox SET published_at=NULL").WithArgs(
		m.DocumentVersionID, m.TenantID, m.DocumentID,
	).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE index_manifests SET repair_attempts=repair_attempts\\+1").WithArgs(
		m.GenerationID,
	).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	got, err := NewPostgresStore(mock).FinishReconciliation(context.Background(), result, 3)
	if err != nil || got != RepairScheduled {
		t.Fatalf("FinishReconciliation = (%q,%v)", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreRejectsStaleReconciliationClaim(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	m := testManifest()
	m.State, m.ReconcileClaimToken = StateActive, "stale-claim"
	mock.ExpectExec("UPDATE index_manifests SET qdrant_count").WithArgs(
		m.GenerationID, m.ReconcileClaimToken, 2, "sha256:digest", 2, "sha256:digest", "", "5m0s",
	).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	_, err = NewPostgresStore(mock).FinishReconciliation(context.Background(), ReconciliationResult{
		Manifest: m, Healthy: true, ReconcileAfter: 5 * time.Minute,
		Qdrant: BackendObservation{Count: 2, Digest: "sha256:digest"}, Elasticsearch: BackendObservation{Count: 2, Digest: "sha256:digest"},
	}, 3)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
}

func TestPostgresStoreDoesNotDuplicatePendingRepair(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	m := testManifest()
	m.State, m.ReconcileClaimToken = StateActive, "claim-1"
	result := ReconciliationResult{Manifest: m, Reason: "mismatch", ReconcileAfter: 5 * time.Minute}
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE index_manifests SET qdrant_count").WithArgs(
		m.GenerationID, m.ReconcileClaimToken, 0, "", 0, "", result.Reason, "5m0s",
	).WillReturnRows(pgxmock.NewRows([]string{"repair_attempts"}).AddRow(1))
	mock.ExpectQuery("SELECT j.status,o.published_at IS NULL").WithArgs(
		m.DocumentVersionID, m.TenantID, m.DocumentID,
	).WillReturnRows(pgxmock.NewRows([]string{"status", "pending"}).AddRow("published", true))
	mock.ExpectCommit()
	got, err := NewPostgresStore(mock).FinishReconciliation(context.Background(), result, 3)
	if err != nil || got != RepairPending {
		t.Fatalf("FinishReconciliation = (%q,%v)", got, err)
	}
}

func TestPostgresStoreStopsSchedulingAfterRepairLimit(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	m := testManifest()
	m.State, m.ReconcileClaimToken = StateActive, "claim-1"
	result := ReconciliationResult{Manifest: m, Reason: "persistent mismatch", ReconcileAfter: 5 * time.Minute}
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE index_manifests SET qdrant_count").WithArgs(
		m.GenerationID, m.ReconcileClaimToken, 0, "", 0, "", result.Reason, "5m0s",
	).WillReturnRows(pgxmock.NewRows([]string{"repair_attempts"}).AddRow(3))
	mock.ExpectCommit()
	got, err := NewPostgresStore(mock).FinishReconciliation(context.Background(), result, 3)
	if err != nil || got != RepairExhausted {
		t.Fatalf("FinishReconciliation = (%q,%v)", got, err)
	}
}
