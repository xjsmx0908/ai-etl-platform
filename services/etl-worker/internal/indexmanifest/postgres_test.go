package indexmanifest

import (
	"context"
	"errors"
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
		"index_version", "expected_chunk_count", "expected_chunk_digest", "state",
	}).AddRow(m.GenerationID, m.TenantID, m.DocumentID, m.DocumentVersionID, m.ChunkerVersion,
		m.EmbeddingModel, m.VectorDimension, m.SchemaVersion, m.CollectionVersion,
		m.IndexVersion, count, digest, m.State)
}

func TestPostgresStoreBeginsBeforeExpectedIdentityAndSealsIdempotently(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	m := testManifest()
	m.ExpectedChunkCount, m.ExpectedChunkDigest, m.ExpectedSealed = 0, "", false
	mock.ExpectExec("INSERT INTO index_manifests").WithArgs(
		m.GenerationID, m.TenantID, m.DocumentID, m.DocumentVersionID, m.ChunkerVersion,
		m.EmbeddingModel, m.VectorDimension, m.SchemaVersion, m.CollectionVersion,
		m.IndexVersion, m.CreatedAt,
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
