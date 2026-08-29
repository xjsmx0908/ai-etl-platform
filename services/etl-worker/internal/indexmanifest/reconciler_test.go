package indexmanifest

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-etl-pipeline/internal/model"
)

type reconciliationStoreStub struct {
	manifests    []Manifest
	results      []ReconciliationResult
	disposition  RepairDisposition
	finishErrors map[string]error
}

func (s *reconciliationStoreStub) ClaimReconciliation(context.Context, ReconciliationClaim) ([]Manifest, error) {
	return s.manifests, nil
}

func (s *reconciliationStoreStub) FinishReconciliation(_ context.Context, result ReconciliationResult, _ int) (RepairDisposition, error) {
	s.results = append(s.results, result)
	if err := s.finishErrors[result.Manifest.GenerationID]; err != nil {
		return "", err
	}
	if s.disposition != "" {
		return s.disposition, nil
	}
	return RepairNotNeeded, nil
}

func TestReconcilerContinuesAfterStaleClaimConflict(t *testing.T) {
	manifests := []Manifest{
		{GenerationID: "gen-stale", TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1", ExpectedChunkCount: 1, ExpectedChunkDigest: "expected", State: StateActive},
		{GenerationID: "gen-next", TenantID: "acme", DocumentID: "doc-2", DocumentVersionID: "job-2", ExpectedChunkCount: 1, ExpectedChunkDigest: "expected", State: StateActive},
	}
	store := &reconciliationStoreStub{manifests: manifests, finishErrors: map[string]error{"gen-stale": ErrConflict}}
	matching := reconciliationProjectionStub{byGeneration: map[string]BackendObservation{
		"gen-stale": {Count: 1, Digest: "expected"},
		"gen-next":  {Count: 1, Digest: "expected"},
	}}

	report, err := NewReconciler(store, matching, matching, ReconcilerOptions{BatchSize: 2, Lease: time.Minute}).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Checked != 2 || report.Conflicted != 1 || report.Healthy != 1 {
		t.Fatalf("report = %+v", report)
	}
	if len(store.results) != 2 {
		t.Fatalf("finished %d manifests, want 2", len(store.results))
	}
}

type reconciliationProjectionStub struct {
	byGeneration map[string]BackendObservation
	errors       map[string]error
}

func (p reconciliationProjectionStub) UpsertGeneration(context.Context, GenerationIdentity, model.Chunk) error {
	return nil
}

func (p reconciliationProjectionStub) ObserveGeneration(_ context.Context, identity GenerationIdentity) (BackendObservation, error) {
	return p.byGeneration[identity.GenerationID], p.errors[identity.GenerationID]
}

func TestReconcilerSchedulesDivergenceAndContinuesAfterBackendError(t *testing.T) {
	manifests := []Manifest{
		{GenerationID: "gen-mismatch", TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1", ExpectedChunkCount: 2, ExpectedChunkDigest: "expected", State: StateActive},
		{GenerationID: "gen-error", TenantID: "acme", DocumentID: "doc-2", DocumentVersionID: "job-2", ExpectedChunkCount: 1, ExpectedChunkDigest: "expected-2", State: StateActive},
	}
	store := &reconciliationStoreStub{manifests: manifests, disposition: RepairScheduled}
	qdrant := reconciliationProjectionStub{
		byGeneration: map[string]BackendObservation{"gen-mismatch": {Count: 2, Digest: "wrong"}},
		errors:       map[string]error{"gen-error": errors.New("qdrant unavailable")},
	}
	elastic := reconciliationProjectionStub{byGeneration: map[string]BackendObservation{
		"gen-mismatch": {Count: 2, Digest: "expected"},
	}}

	report, err := NewReconciler(store, qdrant, elastic, ReconcilerOptions{BatchSize: 10, Lease: time.Minute, MaxRepairs: 3}).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Checked != 2 || report.Diverged != 2 || report.RepairScheduled != 2 {
		t.Fatalf("report = %+v", report)
	}
	if len(store.results) != 2 || store.results[0].Healthy || store.results[1].Reason == "" {
		t.Fatalf("results = %+v", store.results)
	}
}

func TestReconcilerRecordsHealthyActiveGenerationWithoutRepair(t *testing.T) {
	manifest := Manifest{
		GenerationID: "gen-1", TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1",
		ExpectedChunkCount: 2, ExpectedChunkDigest: "sha256:expected", State: StateActive,
	}
	store := &reconciliationStoreStub{manifests: []Manifest{manifest}}
	matching := projectionStub{observation: BackendObservation{Count: 2, Digest: "sha256:expected"}}
	reconciler := NewReconciler(store, matching, matching, ReconcilerOptions{
		BatchSize: 10, Interval: time.Minute, Lease: 30 * time.Second, MaxRepairs: 3,
	})

	report, err := reconciler.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Checked != 1 || report.Healthy != 1 || report.RepairScheduled != 0 {
		t.Fatalf("report = %+v", report)
	}
	if len(store.results) != 1 || !store.results[0].Healthy {
		t.Fatalf("results = %+v", store.results)
	}
	if store.results[0].Qdrant.Count != 2 || store.results[0].Elasticsearch.Digest != "sha256:expected" {
		t.Fatalf("observations not persisted: %+v", store.results[0])
	}
}
