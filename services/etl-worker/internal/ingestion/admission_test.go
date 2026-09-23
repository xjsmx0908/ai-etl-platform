package ingestion

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/model"
)

func TestMemoryStoreAdmitIsIdempotentForStableJobID(t *testing.T) {
	store := NewMemoryStore()
	submission := Submission{
		JobID:            "job-20260827-1",
		EventID:          "event-20260827-1",
		RequestSignature: "sha256:request-1",
		Document: docstore.Document{
			TenantID: "tenant-a", DocID: "doc-42", FileName: "handbook.pdf",
			ObjectKey: "tenant-a/doc-42.pdf", Permission: "internal",
			Status: docstore.StatusQueued, Stage: "queued", CreatedAt: time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC),
		},
		Task: model.Task{DocID: "doc-42", TenantID: "tenant-a", FilePath: "tenant-a/doc-42.pdf"},
	}

	first, err := store.Admit(context.Background(), submission)
	if err != nil {
		t.Fatalf("first admission: %v", err)
	}
	second, err := store.Admit(context.Background(), submission)
	if err != nil {
		t.Fatalf("repeat admission: %v", err)
	}

	if first != (Receipt{JobID: submission.JobID, EventID: submission.EventID, DocID: "doc-42", TenantID: "tenant-a"}) {
		t.Fatalf("first receipt = %+v", first)
	}
	if second != first {
		t.Fatalf("repeat receipt = %+v, want %+v", second, first)
	}
	if got := store.PendingCount(); got != 1 {
		t.Fatalf("pending events = %d, want 1", got)
	}
}

func TestMemoryStoreRejectsStableJobIDForDifferentRequest(t *testing.T) {
	store := NewMemoryStore()
	submission := Submission{
		JobID: "job-1", EventID: "event-1", RequestSignature: "sha256:first",
		Document: docstore.Document{TenantID: "tenant-a", DocID: "doc-1"},
		Task:     model.Task{TenantID: "tenant-a", DocID: "doc-1"},
	}
	if _, err := store.Admit(context.Background(), submission); err != nil {
		t.Fatalf("first admission: %v", err)
	}
	submission.RequestSignature = "sha256:different"
	if _, err := store.Admit(context.Background(), submission); !errors.Is(err, ErrAdmissionConflict) {
		t.Fatalf("conflicting admission error = %v, want ErrAdmissionConflict", err)
	}
	if got := store.PendingCount(); got != 1 {
		t.Fatalf("pending events = %d, want original event only", got)
	}
}

// A delivery that ends without a terminal transition has to hand the claim back,
// or the redelivery answers ClaimBusy for the rest of the lease. This is the
// in-process shape of what the worker hit on a mid-ingestion restart: the lease is
// sized to exceed the worst-case pipeline run, so waiting it out is hours, not a
// backoff, and the worker re-Nacks the same offset the whole time.
func TestMemoryStoreReleaseClaimLetsTheNextDeliveryClaimImmediately(t *testing.T) {
	store := NewMemoryStore()
	submission := Submission{
		JobID: "job-1", EventID: "event-1", RequestSignature: "sha256:request-1",
		Document: docstore.Document{
			TenantID: "tenant-a", DocID: "doc-1", FileName: "handbook.pdf",
			ObjectKey: "tenant-a/doc-1.pdf", Permission: "internal",
			Status: docstore.StatusQueued, Stage: "queued",
		},
		Task: model.Task{DocID: "doc-1", TenantID: "tenant-a", FilePath: "tenant-a/doc-1.pdf"},
	}
	if _, err := store.Admit(context.Background(), submission); err != nil {
		t.Fatalf("admit: %v", err)
	}
	task := model.Task{
		JobID: "job-1", EventID: "event-1", TenantID: "tenant-a",
		DocID: "doc-1", FilePath: "tenant-a/doc-1.pdf",
	}
	lease := 12 * time.Hour

	if got, err := store.Claim(context.Background(), task, lease); err != nil || got != ClaimAcquired {
		t.Fatalf("first claim = (%q,%v), want (%q,nil)", got, err, ClaimAcquired)
	}
	if got, _ := store.Claim(context.Background(), task, lease); got != ClaimBusy {
		t.Fatalf("claim while held = %q, want %q", got, ClaimBusy)
	}
	if err := store.ReleaseClaim(context.Background(), task); err != nil {
		t.Fatalf("release claim: %v", err)
	}
	if got, err := store.Claim(context.Background(), task, lease); err != nil || got != ClaimAcquired {
		t.Fatalf("claim after release = (%q,%v), want (%q,nil)", got, err, ClaimAcquired)
	}

	// A job that already reached a terminal state must not be reopened by a late
	// release, and must stay terminal for the next delivery.
	if err := store.Complete(context.Background(), task, time.Now()); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := store.ReleaseClaim(context.Background(), task); err != nil {
		t.Fatalf("release after completion: %v", err)
	}
	if got, _ := store.Claim(context.Background(), task, lease); got != ClaimTerminal {
		t.Fatalf("claim after completion = %q, want %q", got, ClaimTerminal)
	}
}

func TestMemoryStoreReleaseClaimRejectsIncompleteTask(t *testing.T) {
	if err := NewMemoryStore().ReleaseClaim(context.Background(), model.Task{JobID: "job-1"}); !errors.Is(err, ErrInvalidSubmission) {
		t.Fatalf("release error = %v, want ErrInvalidSubmission", err)
	}
}

type recordingPublisher struct {
	err   error
	tasks []model.Task
}

func (p *recordingPublisher) Publish(_ context.Context, task model.Task) error {
	if p.err != nil {
		return p.err
	}
	p.tasks = append(p.tasks, task)
	return nil
}

func TestRelayLeavesFailedPublicationPendingForRetry(t *testing.T) {
	store := NewMemoryStore()
	_, err := store.Admit(context.Background(), Submission{
		JobID: "job-1", EventID: "event-1",
		RequestSignature: "sha256:request-1",
		Document:         docstore.Document{TenantID: "tenant-a", DocID: "doc-1"},
		Task:             model.Task{TenantID: "tenant-a", DocID: "doc-1"},
	})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	publisher := &recordingPublisher{err: errors.New("kafka unavailable")}
	relay := Relay{Store: store, Publisher: publisher, BatchSize: 10}

	if published, err := relay.RunOnce(context.Background()); err != nil || published != 0 {
		t.Fatalf("failed pass = (%d, %v), want (0, nil)", published, err)
	}
	if got := store.PendingCount(); got != 1 {
		t.Fatalf("pending after outage = %d, want 1", got)
	}

	publisher.err = nil
	if published, err := relay.RunOnce(context.Background()); err != nil || published != 1 {
		t.Fatalf("recovery pass = (%d, %v), want (1, nil)", published, err)
	}
	if got := store.PendingCount(); got != 0 {
		t.Fatalf("pending after recovery = %d, want 0", got)
	}
	if len(publisher.tasks) != 1 || publisher.tasks[0].JobID != "job-1" || publisher.tasks[0].EventID != "event-1" {
		t.Fatalf("published tasks = %+v", publisher.tasks)
	}
}
