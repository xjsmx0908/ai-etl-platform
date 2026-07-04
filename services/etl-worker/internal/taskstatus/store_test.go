package taskstatus

import (
	"context"
	"testing"
	"time"

	"ai-etl-pipeline/internal/model"
)

func TestMemoryStoreSavesAndLoadsTenantScopedStatus(t *testing.T) {
	store := NewMemoryStore()
	status := model.TaskStatus{
		TaskID:    "doc-1",
		DocID:     "doc-1",
		TenantID:  "tenant-a",
		Status:    model.TaskStatusProcessing,
		Stage:     "processing",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Metadata:  map[string]string{"contract_no": "CN-1"},
	}
	if err := store.Save(context.Background(), status); err != nil {
		t.Fatalf("save status: %v", err)
	}

	got, ok, err := store.Load(context.Background(), "tenant-a", "doc-1")
	if err != nil {
		t.Fatalf("load status: %v", err)
	}
	if !ok {
		t.Fatal("expected status found")
	}
	if got.Status != model.TaskStatusProcessing || got.Stage != "processing" {
		t.Fatalf("unexpected status: %+v", got)
	}

	got.Metadata["contract_no"] = "mutated"
	got, ok, err = store.Load(context.Background(), "tenant-a", "doc-1")
	if err != nil || !ok {
		t.Fatalf("reload status ok=%v err=%v", ok, err)
	}
	if got.Metadata["contract_no"] != "CN-1" {
		t.Fatalf("expected cloned metadata, got %+v", got.Metadata)
	}

	if _, ok, err := store.Load(context.Background(), "tenant-b", "doc-1"); err != nil || ok {
		t.Fatalf("expected tenant-b not found, ok=%v err=%v", ok, err)
	}
}

func TestMemoryStoreValidatesRequiredFields(t *testing.T) {
	store := NewMemoryStore()
	if err := store.Save(context.Background(), model.TaskStatus{TenantID: "tenant-a", Status: model.TaskStatusQueued}); err == nil {
		t.Fatal("expected missing task id error")
	}
	if err := store.Save(context.Background(), model.TaskStatus{TaskID: "doc-1", Status: model.TaskStatusQueued}); err == nil {
		t.Fatal("expected missing tenant id error")
	}
	if err := store.Save(context.Background(), model.TaskStatus{TaskID: "doc-1", TenantID: "tenant-a"}); err == nil {
		t.Fatal("expected missing status error")
	}
}
