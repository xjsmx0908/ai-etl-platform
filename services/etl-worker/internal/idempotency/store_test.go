package idempotency

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStore_ReserveAndReplay(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	ctx := context.Background()

	res1, err := store.Reserve(ctx, "tenant-a", "idem-key-1", "sig-a")
	if err != nil {
		t.Fatalf("reserve first: %v", err)
	}
	if res1.State != ReserveNew {
		t.Fatalf("expected %q, got %q", ReserveNew, res1.State)
	}

	res2, err := store.Reserve(ctx, "tenant-a", "idem-key-1", "sig-a")
	if err != nil {
		t.Fatalf("reserve second: %v", err)
	}
	if res2.State != ReserveProcessing {
		t.Fatalf("expected %q, got %q", ReserveProcessing, res2.State)
	}

	if err := store.Complete(ctx, "tenant-a", "idem-key-1", "sig-a", CachedResponse{
		StatusCode:  202,
		ContentType: "application/json",
		Body:        []byte(`{"task_id":"doc-1"}`),
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	res3, err := store.Reserve(ctx, "tenant-a", "idem-key-1", "sig-a")
	if err != nil {
		t.Fatalf("reserve replay: %v", err)
	}
	if res3.State != ReserveReplay {
		t.Fatalf("expected %q, got %q", ReserveReplay, res3.State)
	}
	if res3.Cached == nil || string(res3.Cached.Body) != `{"task_id":"doc-1"}` {
		t.Fatalf("unexpected replay payload: %#v", res3.Cached)
	}
}

func TestMemoryStore_ConflictOnDifferentSignature(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	ctx := context.Background()

	if _, err := store.Reserve(ctx, "tenant-a", "idem-key-2", "sig-a"); err != nil {
		t.Fatalf("reserve first: %v", err)
	}

	res, err := store.Reserve(ctx, "tenant-a", "idem-key-2", "sig-b")
	if err != nil {
		t.Fatalf("reserve conflict: %v", err)
	}
	if res.State != ReserveConflict {
		t.Fatalf("expected %q, got %q", ReserveConflict, res.State)
	}
}

func TestMemoryStore_AbortReleasesProcessingKey(t *testing.T) {
	store := NewMemoryStore(time.Hour)
	ctx := context.Background()

	res1, err := store.Reserve(ctx, "tenant-a", "idem-key-3", "sig-a")
	if err != nil {
		t.Fatalf("reserve first: %v", err)
	}
	if res1.State != ReserveNew {
		t.Fatalf("expected %q, got %q", ReserveNew, res1.State)
	}

	if err := store.Abort(ctx, "tenant-a", "idem-key-3", "sig-a"); err != nil {
		t.Fatalf("abort: %v", err)
	}

	res2, err := store.Reserve(ctx, "tenant-a", "idem-key-3", "sig-a")
	if err != nil {
		t.Fatalf("reserve after abort: %v", err)
	}
	if res2.State != ReserveNew {
		t.Fatalf("expected %q after abort, got %q", ReserveNew, res2.State)
	}
}
