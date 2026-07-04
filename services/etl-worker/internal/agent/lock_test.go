package agent

import (
	"context"
	"testing"
	"time"
)

func TestMemoryLockManager_RejectsCompetingOwnerUntilTTLExpires(t *testing.T) {
	manager := NewMemoryLockManager()
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }

	leaseA, err := manager.Acquire(context.Background(), "run-1", "node-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire node-a: %v", err)
	}
	if leaseA.FencingToken != 1 {
		t.Fatalf("expected first fencing token 1, got %d", leaseA.FencingToken)
	}

	if _, err := manager.Acquire(context.Background(), "run-1", "node-b", time.Minute); err == nil {
		t.Fatal("expected competing owner to be rejected")
	}

	now = now.Add(2 * time.Minute)
	leaseB, err := manager.Acquire(context.Background(), "run-1", "node-b", time.Minute)
	if err != nil {
		t.Fatalf("acquire node-b after ttl: %v", err)
	}
	if leaseB.FencingToken <= leaseA.FencingToken {
		t.Fatalf("expected newer fencing token, got old=%d new=%d", leaseA.FencingToken, leaseB.FencingToken)
	}
}

func TestMemoryLockManager_RejectsStaleRelease(t *testing.T) {
	manager := NewMemoryLockManager()
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }

	leaseA, err := manager.Acquire(context.Background(), "run-1", "node-a", time.Second)
	if err != nil {
		t.Fatalf("acquire node-a: %v", err)
	}
	now = now.Add(2 * time.Second)
	if _, err := manager.Acquire(context.Background(), "run-1", "node-b", time.Second); err != nil {
		t.Fatalf("acquire node-b: %v", err)
	}
	if err := manager.Release(context.Background(), leaseA); err == nil {
		t.Fatal("expected stale release to be rejected")
	}
}
