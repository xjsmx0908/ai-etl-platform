package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMemoryStore_SaveRunRejectsVersionConflict(t *testing.T) {
	store := NewMemoryStore()
	run := Run{
		ID:        "run-1",
		TenantID:  "tenant-a",
		UserID:    "user-a",
		State:     StateCreated,
		MaxSteps:  2,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	loaded, err := store.LoadRun(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	loaded.State = StateRunning
	saved, err := store.SaveRun(context.Background(), loaded, SaveOptions{ExpectedVersion: loaded.Version, FencingToken: 1})
	if err != nil {
		t.Fatalf("save run: %v", err)
	}
	loaded.State = StateCompleted
	_, err = store.SaveRun(context.Background(), loaded, SaveOptions{ExpectedVersion: loaded.Version, FencingToken: 1})
	if err == nil || !strings.Contains(err.Error(), "version conflict") {
		t.Fatalf("expected version conflict, got saved=%+v err=%v", saved, err)
	}
}

func TestMemoryStore_SaveRunRejectsStaleFencingToken(t *testing.T) {
	store := NewMemoryStore()
	run := Run{
		ID:        "run-1",
		TenantID:  "tenant-a",
		UserID:    "user-a",
		State:     StateCreated,
		MaxSteps:  2,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	loaded, _ := store.LoadRun(context.Background(), "run-1")
	loaded.State = StateRunning
	saved, err := store.SaveRun(context.Background(), loaded, SaveOptions{ExpectedVersion: loaded.Version, FencingToken: 3})
	if err != nil {
		t.Fatalf("save with new fencing token: %v", err)
	}
	saved.State = StateCompleted
	_, err = store.SaveRun(context.Background(), saved, SaveOptions{ExpectedVersion: saved.Version, FencingToken: 2})
	if err == nil || !strings.Contains(err.Error(), "stale fencing token") {
		t.Fatalf("expected stale fencing token error, got %v", err)
	}
}
