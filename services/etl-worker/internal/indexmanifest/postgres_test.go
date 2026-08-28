package indexmanifest

import (
	"context"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v5"
)

func TestPostgresStoreCreatesAndObservesManifest(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	store := NewPostgresStore(mock)
	m := Manifest{GenerationID: "gen-1", TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "v-1", ChunkerVersion: "chunker-v1", EmbeddingModel: "embed-v1", VectorDimension: 3, SchemaVersion: "schema-v1", CollectionVersion: "collection-v1", IndexVersion: "index-v1", ExpectedChunkCount: 2, ExpectedChunkDigest: "digest", State: StateBuilding, CreatedAt: time.Now().UTC()}
	mock.ExpectExec("INSERT INTO index_manifests").WithArgs(m.GenerationID, m.TenantID, m.DocumentID, m.DocumentVersionID, m.ChunkerVersion, m.EmbeddingModel, m.VectorDimension, m.SchemaVersion, m.CollectionVersion, m.IndexVersion, m.ExpectedChunkCount, m.ExpectedChunkDigest, StateBuilding, m.CreatedAt).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := store.Create(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("UPDATE index_manifests").WithArgs(m.GenerationID, 2, "digest").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if err := store.Observe(context.Background(), m.GenerationID, BackendQdrant, BackendObservation{Count: 2, Digest: "digest"}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreMarkReadyAndActivateUseCompareAndSet(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	store := NewPostgresStore(mock)
	mock.ExpectExec("UPDATE index_manifests").WithArgs("gen-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if err := store.MarkReady(context.Background(), "gen-1"); err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("acme/v-1").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("UPDATE index_manifests").WithArgs("acme", "v-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE index_manifests").WithArgs("acme", "v-1", "gen-2").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	if err := store.Activate(context.Background(), "acme", "v-1", "gen-2"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreRejectsUnverifiedReadinessAndNonReadyActivation(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	store := NewPostgresStore(mock)
	mock.ExpectExec("UPDATE index_manifests").WithArgs("gen-1").WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	if err := store.MarkReady(context.Background(), "gen-1"); err != ErrNotReady {
		t.Fatalf("MarkReady error=%v", err)
	}
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs("acme/v-1").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("UPDATE index_manifests").WithArgs("acme", "v-1").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE index_manifests").WithArgs("acme", "v-1", "missing").WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()
	if err := store.Activate(context.Background(), "acme", "v-1", "missing"); err != ErrConflict {
		t.Fatalf("Activate error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreRecordsFailedAttempts(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectExec("UPDATE index_manifests").WithArgs("gen-1", "elasticsearch unavailable").WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if err := NewPostgresStore(mock).Fail(context.Background(), "gen-1", "elasticsearch unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
