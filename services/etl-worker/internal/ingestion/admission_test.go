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
