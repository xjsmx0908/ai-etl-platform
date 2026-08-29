package deletionworkflow

import (
	"context"
	"errors"
	"testing"
	"time"
)

type collectorStoreStub struct {
	job      Job
	results  []FinishResult
	finished bool
}

func (s *collectorStoreStub) Claim(_ context.Context, claim ClaimRequest) ([]Job, error) {
	if s.finished {
		return nil, nil
	}
	s.job.ClaimToken = claim.Token
	return []Job{s.job}, nil
}

func (s *collectorStoreStub) Finish(_ context.Context, result FinishResult) error {
	s.results = append(s.results, result)
	if result.QdrantDeleted {
		s.job.QdrantDeletedAt = time.Now()
	}
	if result.ElasticsearchDeleted {
		s.job.ElasticsearchDeletedAt = time.Now()
	}
	if result.ObjectsDeleted {
		s.job.ObjectsDeletedAt = time.Now()
	}
	s.job.LastError = result.Reason
	if result.Reason == "" {
		s.finished = true
	}
	return nil
}

type documentDeleterStub struct {
	calls int
	err   error
}

func (d *documentDeleterStub) DeleteDocument(context.Context, string, string) error {
	d.calls++
	return d.err
}

func (d *documentDeleterStub) DeleteObjects(context.Context, []string) error {
	d.calls++
	return d.err
}

func TestCollectorRetriesOnlyIncompleteDependenciesBeforeFinalization(t *testing.T) {
	store := &collectorStoreStub{job: Job{JobID: "delete-1", TenantID: "acme", DocumentID: "policy-1", ObjectKeys: []string{"acme/policy-1.pdf"}}}
	qdrant := &documentDeleterStub{}
	elasticsearch := &documentDeleterStub{err: errors.New("unavailable")}
	objects := &documentDeleterStub{}
	collector := NewCollector(store, qdrant, elasticsearch, objects, Options{Lease: time.Minute, BatchSize: 10, RetryBackoff: time.Second})

	first, err := collector.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.Failed != 1 || first.Completed != 0 || len(store.results) != 1 {
		t.Fatalf("first report=%+v results=%+v", first, store.results)
	}
	if !store.results[0].QdrantDeleted || store.results[0].ElasticsearchDeleted || !store.results[0].ObjectsDeleted {
		t.Fatalf("first progress=%+v", store.results[0])
	}

	elasticsearch.err = nil
	second, err := collector.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Completed != 1 || second.Failed != 0 {
		t.Fatalf("second report=%+v", second)
	}
	if qdrant.calls != 1 || elasticsearch.calls != 2 || objects.calls != 1 {
		t.Fatalf("calls qdrant=%d elasticsearch=%d objects=%d", qdrant.calls, elasticsearch.calls, objects.calls)
	}
}
