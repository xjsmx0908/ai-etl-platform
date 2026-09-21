package indexmanifest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"ai-etl-pipeline/internal/model"
)

type reconciliationStoreStub struct {
	manifests    []Manifest
	results      []ReconciliationResult
	disposition  RepairDisposition
	finishErrors map[string]error
	// Failed generations are claimed through a separate call so the active
	// observation pass can be asserted to leave them alone.
	failed          []Manifest
	claimedBudgets  []int
	scheduled       []string
	scheduleErrors  map[string]error
	claimFailedErr  error
	scheduleCalls   int
	claimFailedCall int
}

type reconciliationObserverStub struct {
	reports []ReconciliationReport
	errors  []error
}

func (o *reconciliationObserverStub) ObserveReconciliation(report ReconciliationReport, err error) {
	o.reports = append(o.reports, report)
	o.errors = append(o.errors, err)
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

func (s *reconciliationStoreStub) ClaimFailedRepairs(_ context.Context, _ ReconciliationClaim, maxRepairs int) ([]Manifest, error) {
	s.claimFailedCall++
	s.claimedBudgets = append(s.claimedBudgets, maxRepairs)
	if s.claimFailedErr != nil {
		return nil, s.claimFailedErr
	}
	return s.failed, nil
}

func (s *reconciliationStoreStub) ScheduleFailedRepair(_ context.Context, manifest Manifest) error {
	s.scheduleCalls++
	if err := s.scheduleErrors[manifest.GenerationID]; err != nil {
		return err
	}
	s.scheduled = append(s.scheduled, manifest.GenerationID)
	return nil
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

// A manifest whose durable bookkeeping cannot be written must not stop the rest
// of the batch. Every claimed manifest is already leased, so returning early
// would leave all the later ones unobserved until their lease expires, and one
// unrepairable row would quietly reduce the pass to whatever was claimed before
// it. The failure must still reach the caller.
func TestReconcilerReportsFinishFailureWithoutStoppingTheBatch(t *testing.T) {
	manifests := []Manifest{
		{GenerationID: "gen-broken", TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1", ExpectedChunkCount: 1, ExpectedChunkDigest: "expected", State: StateActive},
		{GenerationID: "gen-next", TenantID: "acme", DocumentID: "doc-2", DocumentVersionID: "job-2", ExpectedChunkCount: 1, ExpectedChunkDigest: "expected", State: StateActive},
		{GenerationID: "gen-last", TenantID: "acme", DocumentID: "doc-3", DocumentVersionID: "job-3", ExpectedChunkCount: 1, ExpectedChunkDigest: "expected", State: StateActive},
	}
	store := &reconciliationStoreStub{manifests: manifests, finishErrors: map[string]error{
		"gen-broken": errors.New("load repair ingestion job: no rows in result set"),
	}}
	matching := reconciliationProjectionStub{byGeneration: map[string]BackendObservation{
		"gen-broken": {Count: 1, Digest: "expected"},
		"gen-next":   {Count: 1, Digest: "expected"},
		"gen-last":   {Count: 1, Digest: "expected"},
	}}
	observer := &reconciliationObserverStub{}
	reconciler := NewReconciler(store, matching, matching, ReconcilerOptions{
		BatchSize: 3, Interval: time.Minute, Lease: time.Minute, MaxRepairs: 3,
	}).WithObserver(observer)

	report, err := reconciler.RunOnce(context.Background())
	if err == nil {
		t.Fatal("finish failure was not reported to the caller")
	}
	if !strings.Contains(err.Error(), "gen-broken") {
		t.Fatalf("error does not name the failing manifest: %v", err)
	}
	if report.Checked != 3 || report.Healthy != 2 {
		t.Fatalf("report = %+v", report)
	}
	if len(store.results) != 3 {
		t.Fatalf("finished %d manifests, want all 3", len(store.results))
	}
	if len(observer.errors) != 1 || observer.errors[0] == nil {
		t.Fatalf("observer errors = %+v", observer.errors)
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

func TestReconcilerPublishesOneBoundedReportPerPass(t *testing.T) {
	manifest := Manifest{
		GenerationID: "gen-1", TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1",
		ExpectedChunkCount: 1, ExpectedChunkDigest: "expected", State: StateActive,
	}
	store := &reconciliationStoreStub{manifests: []Manifest{manifest}}
	matching := reconciliationProjectionStub{byGeneration: map[string]BackendObservation{
		"gen-1": {Count: 1, Digest: "expected"},
	}}
	observer := &reconciliationObserverStub{}
	reconciler := NewReconciler(store, matching, matching, ReconcilerOptions{BatchSize: 1, Lease: time.Minute}).WithObserver(observer)

	report, err := reconciler.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(observer.reports) != 1 || !reflect.DeepEqual(observer.reports[0], report) || observer.errors[0] != nil {
		t.Fatalf("observed reports=%+v errors=%+v", observer.reports, observer.errors)
	}
}

// A generation whose build failed is not active, so the observation pass never
// claims it and the outbox row that carried its one delivery is already
// published. Without a replay pass it stays failed forever while every health
// signal reports the platform as healthy.
func TestReconcilerReplaysFailedGenerationsThroughTheirIngestionJob(t *testing.T) {
	failed := []Manifest{
		{GenerationID: "gen-dead-1", TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1", State: StateFailed},
		{GenerationID: "gen-dead-2", TenantID: "acme", DocumentID: "doc-2", DocumentVersionID: "job-2", State: StateFailed},
	}
	store := &reconciliationStoreStub{failed: failed}
	empty := reconciliationProjectionStub{byGeneration: map[string]BackendObservation{}}
	reconciler := NewReconciler(store, empty, empty, ReconcilerOptions{
		BatchSize: 10, Interval: time.Minute, Lease: 30 * time.Second, MaxRepairs: 3,
	})

	report, err := reconciler.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Checked != 2 || report.RepairReplayed != 2 || report.RepairUnavailable != 0 {
		t.Fatalf("report = %+v", report)
	}
	if !reflect.DeepEqual(store.scheduled, []string{"gen-dead-1", "gen-dead-2"}) {
		t.Fatalf("scheduled = %v", store.scheduled)
	}
	// A failed generation has no projection to observe, so the active pass must
	// not have touched it: no finish results, no health accounting.
	if len(store.results) != 0 || report.Healthy != 0 || report.Diverged != 0 {
		t.Fatalf("failed generations were observed as active: results=%+v report=%+v", store.results, report)
	}
}

// The bound has to reach the claim, otherwise "bounded repair" is only a name:
// a generation whose budget is spent would be replayed on every pass forever.
func TestReconcilerAsksTheStoreForTheConfiguredBoundedRepairBudget(t *testing.T) {
	store := &reconciliationStoreStub{}
	empty := reconciliationProjectionStub{byGeneration: map[string]BackendObservation{}}
	reconciler := NewReconciler(store, empty, empty, ReconcilerOptions{
		BatchSize: 10, Interval: time.Minute, Lease: 30 * time.Second, MaxRepairs: 7,
	})

	report, err := reconciler.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if store.claimFailedCall != 1 || !reflect.DeepEqual(store.claimedBudgets, []int{7}) {
		t.Fatalf("claim budgets = %v calls = %d", store.claimedBudgets, store.claimFailedCall)
	}
	if report.RepairReplayed != 0 || report.Checked != 0 {
		t.Fatalf("report = %+v", report)
	}
}

// An unconfigured reconciler must still bound its budget rather than ask the
// store to claim with a budget of zero, which the store rejects as invalid.
func TestReconcilerDefaultsTheRepairBudgetWhenUnconfigured(t *testing.T) {
	store := &reconciliationStoreStub{}
	empty := reconciliationProjectionStub{byGeneration: map[string]BackendObservation{}}
	reconciler := NewReconciler(store, empty, empty, ReconcilerOptions{BatchSize: 1, Lease: time.Minute})

	if _, err := reconciler.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.claimedBudgets, []int{3}) {
		t.Fatalf("claim budgets = %v", store.claimedBudgets)
	}
}

// A failed generation with no ingestion job has no automatic route back to a
// projection. That is a different outcome from a scheduling failure and must be
// reported as such: the repair budget is still charged so the state escalates
// to repair_exhausted instead of being re-claimed on every pass.
func TestReconcilerCountsFailedGenerationsWithoutAReplayPathSeparately(t *testing.T) {
	store := &reconciliationStoreStub{
		failed:         []Manifest{{GenerationID: "gen-orphan", TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1", State: StateFailed}},
		scheduleErrors: map[string]error{"gen-orphan": ErrNoReplayPath},
	}
	empty := reconciliationProjectionStub{byGeneration: map[string]BackendObservation{}}
	reconciler := NewReconciler(store, empty, empty, ReconcilerOptions{
		BatchSize: 10, Interval: time.Minute, Lease: time.Minute, MaxRepairs: 3,
	})

	report, err := reconciler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("an unavailable replay path is a recorded outcome, not a pass failure: %v", err)
	}
	if report.RepairUnavailable != 1 || report.RepairReplayed != 0 || report.Conflicted != 0 {
		t.Fatalf("report = %+v", report)
	}
}

// A failure to claim failed generations must not stop the active pass from
// being reported: the two claims are independent, and losing the active
// observation would hide live divergence behind a replay problem.
func TestReconcilerStillReportsActivePassWhenTheFailedClaimFails(t *testing.T) {
	active := Manifest{
		GenerationID: "gen-1", TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1",
		ExpectedChunkCount: 1, ExpectedChunkDigest: "expected", State: StateActive,
	}
	store := &reconciliationStoreStub{
		manifests:      []Manifest{active},
		claimFailedErr: errors.New("claim failed repairs: connection refused"),
	}
	matching := reconciliationProjectionStub{byGeneration: map[string]BackendObservation{
		"gen-1": {Count: 1, Digest: "expected"},
	}}
	observer := &reconciliationObserverStub{}
	reconciler := NewReconciler(store, matching, matching, ReconcilerOptions{
		BatchSize: 10, Interval: time.Minute, Lease: time.Minute, MaxRepairs: 3,
	}).WithObserver(observer)

	report, err := reconciler.RunOnce(context.Background())
	if err == nil {
		t.Fatal("expected the claim failure to reach the caller")
	}
	if report.Healthy != 1 || report.Checked != 1 {
		t.Fatalf("report = %+v", report)
	}
	if len(observer.reports) != 1 || observer.reports[0].Healthy != 1 {
		t.Fatalf("observed reports=%+v", observer.reports)
	}
}
