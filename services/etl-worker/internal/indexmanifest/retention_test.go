package indexmanifest

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"ai-etl-pipeline/internal/model"
)

type rollbackStoreStub struct {
	manifest       Manifest
	err            error
	targetRequests []RollbackRequest
	commits        []RollbackCommit
}

func (s *rollbackStoreStub) RollbackTarget(_ context.Context, request RollbackRequest) (Manifest, error) {
	s.targetRequests = append(s.targetRequests, request)
	s.manifest.RetentionClaimToken = request.ClaimToken
	return s.manifest, s.err
}

func (s *rollbackStoreStub) Rollback(_ context.Context, commit RollbackCommit) error {
	s.commits = append(s.commits, commit)
	return s.err
}

type rollbackProjectionStub struct {
	observation BackendObservation
	err         error
	calls       []GenerationIdentity
}

func (p *rollbackProjectionStub) UpsertGeneration(context.Context, GenerationIdentity, model.Chunk) error {
	return nil
}

func (p *rollbackProjectionStub) ObserveGeneration(_ context.Context, identity GenerationIdentity) (BackendObservation, error) {
	p.calls = append(p.calls, identity)
	return p.observation, p.err
}

func TestRollbackerVerifiesTargetBeforeAtomicPromotion(t *testing.T) {
	manifest := testManifest()
	manifest.GenerationID, manifest.State = "gen-retired", StateRetired
	store := &rollbackStoreStub{manifest: manifest}
	matching := BackendObservation{Count: manifest.ExpectedChunkCount, Digest: manifest.ExpectedChunkDigest}
	qdrant := &rollbackProjectionStub{observation: matching}
	elastic := &rollbackProjectionStub{observation: matching}
	request := RollbackRequest{
		Version:            VersionIdentity{TenantID: manifest.TenantID, DocumentID: manifest.DocumentID, DocumentVersionID: manifest.DocumentVersionID},
		TargetGenerationID: "gen-retired", ExpectedActiveGenerationID: "gen-active", Window: 7 * 24 * time.Hour,
	}

	if err := NewRollbacker(store, qdrant, elastic).Rollback(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(store.commits) != 1 {
		t.Fatalf("commits=%d, want 1", len(store.commits))
	}
	commit := store.commits[0]
	if commit.Request.Version != request.Version || commit.Request.TargetGenerationID != request.TargetGenerationID ||
		commit.Request.ExpectedActiveGenerationID != request.ExpectedActiveGenerationID || commit.Request.Window != request.Window ||
		commit.Request.ClaimToken == "" || commit.Request.Lease <= 0 || commit.Qdrant != matching || commit.Elasticsearch != matching {
		t.Fatalf("commit = %+v", commit)
	}
	if commit.Request.ClaimToken != store.manifest.RetentionClaimToken {
		t.Fatal("rollback commit did not preserve the target fencing token")
	}
	wantIdentity := GenerationIdentity{VersionIdentity: request.Version, GenerationID: request.TargetGenerationID}
	if !reflect.DeepEqual(qdrant.calls, []GenerationIdentity{wantIdentity}) || !reflect.DeepEqual(elastic.calls, []GenerationIdentity{wantIdentity}) {
		t.Fatalf("qdrant calls=%+v elastic calls=%+v", qdrant.calls, elastic.calls)
	}
}

func TestRollbackerRejectsDivergedTargetWithoutPromotion(t *testing.T) {
	manifest := testManifest()
	manifest.GenerationID, manifest.State = "gen-retired", StateRetired
	store := &rollbackStoreStub{manifest: manifest}
	qdrant := &rollbackProjectionStub{observation: BackendObservation{Count: 1, Digest: "wrong"}}
	elastic := &rollbackProjectionStub{observation: BackendObservation{Count: 2, Digest: manifest.ExpectedChunkDigest}}
	request := RollbackRequest{
		Version:            VersionIdentity{TenantID: manifest.TenantID, DocumentID: manifest.DocumentID, DocumentVersionID: manifest.DocumentVersionID},
		TargetGenerationID: manifest.GenerationID, ExpectedActiveGenerationID: "gen-active", Window: time.Hour,
	}

	err := NewRollbacker(store, qdrant, elastic).Rollback(context.Background(), request)
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("Rollback error=%v, want ErrNotReady", err)
	}
	if len(store.commits) != 0 {
		t.Fatal("diverged target was promoted")
	}
}

type retentionStoreStub struct {
	manifests    []Manifest
	finished     []RetentionResult
	finishErrors map[string]error
}

type retentionObserverStub struct {
	reports []RetentionReport
	errors  []error
}

func (o *retentionObserverStub) ObserveRetention(report RetentionReport, err error) {
	o.reports = append(o.reports, report)
	o.errors = append(o.errors, err)
}

func (s *retentionStoreStub) ClaimRetention(context.Context, RetentionClaim) ([]Manifest, error) {
	return s.manifests, nil
}

func (s *retentionStoreStub) FinishRetention(_ context.Context, result RetentionResult) error {
	s.finished = append(s.finished, result)
	if err := s.finishErrors[result.Manifest.GenerationID]; err != nil {
		return err
	}
	return nil
}

type deletionProjectionStub struct {
	deleted []GenerationIdentity
	err     error
}

func (p *deletionProjectionStub) DeleteGeneration(_ context.Context, identity GenerationIdentity) error {
	p.deleted = append(p.deleted, identity)
	return p.err
}

type scriptedDeletionProjection struct {
	errors []error
	calls  int
}

func (p *scriptedDeletionProjection) DeleteGeneration(context.Context, GenerationIdentity) error {
	index := p.calls
	p.calls++
	if index < len(p.errors) {
		return p.errors[index]
	}
	return nil
}

func TestRetentionCollectorDeletesBothProjectionsBeforeManifest(t *testing.T) {
	manifest := testManifest()
	manifest.GenerationID, manifest.State, manifest.RetentionClaimToken = "gen-old", StateRetired, "retention-1"
	store := &retentionStoreStub{manifests: []Manifest{manifest}}
	qdrant := &deletionProjectionStub{}
	elastic := &deletionProjectionStub{}
	collector := NewRetentionCollector(store, qdrant, elastic, RetentionOptions{
		Window: 7 * 24 * time.Hour, Lease: 5 * time.Minute, BatchSize: 10,
	})

	report, err := collector.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Claimed != 1 || report.Deleted != 1 || report.Failed != 0 {
		t.Fatalf("report=%+v", report)
	}
	wantIdentity := GenerationIdentity{VersionIdentity: VersionIdentity{TenantID: manifest.TenantID, DocumentID: manifest.DocumentID, DocumentVersionID: manifest.DocumentVersionID}, GenerationID: manifest.GenerationID}
	if !reflect.DeepEqual(qdrant.deleted, []GenerationIdentity{wantIdentity}) || !reflect.DeepEqual(elastic.deleted, []GenerationIdentity{wantIdentity}) {
		t.Fatalf("qdrant=%+v elastic=%+v", qdrant.deleted, elastic.deleted)
	}
	if len(store.finished) != 1 || !store.finished[0].QdrantDeleted || !store.finished[0].ElasticsearchDeleted || store.finished[0].Reason != "" {
		t.Fatalf("finished=%+v", store.finished)
	}
}

func TestRetentionCollectorPersistsPartialProgressForRetry(t *testing.T) {
	manifest := testManifest()
	manifest.GenerationID, manifest.State, manifest.RetentionClaimToken = "gen-old", StateRetired, "retention-1"
	store := &retentionStoreStub{manifests: []Manifest{manifest}}
	qdrant := &deletionProjectionStub{}
	elastic := &deletionProjectionStub{err: errors.New("elasticsearch unavailable")}

	report, err := NewRetentionCollector(store, qdrant, elastic, RetentionOptions{
		Window: time.Hour, Lease: time.Minute, BatchSize: 1,
	}).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Failed != 1 || report.Deleted != 0 || len(store.finished) != 1 {
		t.Fatalf("report=%+v finished=%+v", report, store.finished)
	}
	result := store.finished[0]
	if !result.QdrantDeleted || result.ElasticsearchDeleted || result.Reason == "" {
		t.Fatalf("partial result=%+v", result)
	}
}

func TestRetentionCollectorRetriesOnlyIncompleteProjection(t *testing.T) {
	manifest := testManifest()
	manifest.GenerationID, manifest.State, manifest.RetentionClaimToken = "gen-old", StateRetired, "retention-2"
	manifest.QdrantDeletedAt = time.Now().UTC()
	store := &retentionStoreStub{manifests: []Manifest{manifest}}
	qdrant := &scriptedDeletionProjection{errors: []error{errors.New("must not be called")}}
	elastic := &scriptedDeletionProjection{}

	report, err := NewRetentionCollector(store, qdrant, elastic, RetentionOptions{
		Window: time.Hour, Lease: time.Minute, BatchSize: 1,
	}).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Deleted != 1 || qdrant.calls != 0 || elastic.calls != 1 {
		t.Fatalf("report=%+v qdrant_calls=%d elastic_calls=%d", report, qdrant.calls, elastic.calls)
	}
}

func TestRetentionCollectorContinuesAfterStaleClaimConflict(t *testing.T) {
	first := testManifest()
	first.GenerationID, first.State, first.RetentionClaimToken = "gen-stale", StateRetired, "retention-1"
	second := first
	second.GenerationID = "gen-next"
	store := &retentionStoreStub{
		manifests:    []Manifest{first, second},
		finishErrors: map[string]error{"gen-stale": ErrConflict},
	}
	qdrant := &deletionProjectionStub{}
	elastic := &deletionProjectionStub{}

	report, err := NewRetentionCollector(store, qdrant, elastic, RetentionOptions{
		Window: time.Hour, Lease: 5 * time.Minute, BatchSize: 2,
	}).RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Claimed != 2 || report.Conflicted != 1 || report.Deleted != 1 {
		t.Fatalf("report=%+v", report)
	}
	if len(store.finished) != 2 {
		t.Fatalf("finished=%d, want 2", len(store.finished))
	}
}

func TestRetentionCollectorPublishesOneBoundedReportPerPass(t *testing.T) {
	manifest := testManifest()
	manifest.GenerationID, manifest.State, manifest.RetentionClaimToken = "gen-old", StateRetired, "retention-1"
	store := &retentionStoreStub{manifests: []Manifest{manifest}}
	observer := &retentionObserverStub{}
	collector := NewRetentionCollector(store, &deletionProjectionStub{}, &deletionProjectionStub{}, RetentionOptions{
		Window: time.Hour, Lease: time.Minute, BatchSize: 1,
	}).WithObserver(observer)

	report, err := collector.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(observer.reports) != 1 || !reflect.DeepEqual(observer.reports[0], report) || observer.errors[0] != nil {
		t.Fatalf("observed reports=%+v errors=%+v", observer.reports, observer.errors)
	}
}

type rollbackObserverStub struct{ outcomes []RollbackOutcome }

func (o *rollbackObserverStub) ObserveRollback(outcome RollbackOutcome) {
	o.outcomes = append(o.outcomes, outcome)
}

func TestRollbackerPublishesSuccessAndFailureOutcomes(t *testing.T) {
	manifest := testManifest()
	manifest.GenerationID, manifest.State = "gen-retired", StateRetired
	matching := BackendObservation{Count: manifest.ExpectedChunkCount, Digest: manifest.ExpectedChunkDigest}
	request := RollbackRequest{
		Version:            VersionIdentity{TenantID: manifest.TenantID, DocumentID: manifest.DocumentID, DocumentVersionID: manifest.DocumentVersionID},
		TargetGenerationID: manifest.GenerationID, ExpectedActiveGenerationID: "gen-active", Window: time.Hour,
	}
	observer := &rollbackObserverStub{}
	rollbacker := NewRollbacker(&rollbackStoreStub{manifest: manifest},
		&rollbackProjectionStub{observation: matching}, &rollbackProjectionStub{observation: matching}).WithObserver(observer)
	if err := rollbacker.Rollback(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	rollbacker = NewRollbacker(&rollbackStoreStub{manifest: manifest},
		&rollbackProjectionStub{observation: BackendObservation{Count: 1, Digest: "wrong"}},
		&rollbackProjectionStub{observation: matching}).WithObserver(observer)
	if err := rollbacker.Rollback(context.Background(), request); !errors.Is(err, ErrNotReady) {
		t.Fatalf("error=%v, want ErrNotReady", err)
	}
	if !reflect.DeepEqual(observer.outcomes, []RollbackOutcome{RollbackSucceeded, RollbackNotReady}) {
		t.Fatalf("outcomes=%v", observer.outcomes)
	}
}

func TestRollbackerPublishesTargetConflictOutcome(t *testing.T) {
	manifest := testManifest()
	observer := &rollbackObserverStub{}
	rollbacker := NewRollbacker(&rollbackStoreStub{manifest: manifest, err: ErrConflict},
		&rollbackProjectionStub{}, &rollbackProjectionStub{}).WithObserver(observer)
	err := rollbacker.Rollback(context.Background(), RollbackRequest{
		Version:            VersionIdentity{TenantID: manifest.TenantID, DocumentID: manifest.DocumentID, DocumentVersionID: manifest.DocumentVersionID},
		TargetGenerationID: "gen-retired", ExpectedActiveGenerationID: "gen-active", Window: time.Hour,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error=%v, want ErrConflict", err)
	}
	if !reflect.DeepEqual(observer.outcomes, []RollbackOutcome{RollbackConflict}) {
		t.Fatalf("outcomes=%v", observer.outcomes)
	}
}
