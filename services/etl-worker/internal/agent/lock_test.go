package agent

import (
	"context"
	"os"
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

func TestMemoryLockManager_RejectsSameOwnerReacquireUntilRelease(t *testing.T) {
	manager := NewMemoryLockManager()
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }

	leaseA, err := manager.Acquire(context.Background(), "run-1", "node-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire node-a: %v", err)
	}
	if _, err := manager.Acquire(context.Background(), "run-1", "node-a", time.Minute); err == nil {
		t.Fatal("expected same owner reacquire to be rejected before release")
	}
	if err := manager.Release(context.Background(), leaseA); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	leaseB, err := manager.Acquire(context.Background(), "run-1", "node-a", time.Minute)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	if leaseB.FencingToken <= leaseA.FencingToken {
		t.Fatalf("expected monotonic token after release, got old=%d new=%d", leaseA.FencingToken, leaseB.FencingToken)
	}
}

func TestMemoryLockManager_ExtendsMatchingLease(t *testing.T) {
	manager := NewMemoryLockManager()
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }

	lease, err := manager.Acquire(context.Background(), "run-1", "node-a", time.Second)
	if err != nil {
		t.Fatalf("acquire node-a: %v", err)
	}
	extended, err := manager.Extend(context.Background(), lease, 5*time.Second)
	if err != nil {
		t.Fatalf("extend lease: %v", err)
	}
	if !extended.ExpiresAt.After(lease.ExpiresAt) {
		t.Fatalf("expected extended expiry, got old=%s new=%s", lease.ExpiresAt, extended.ExpiresAt)
	}
	now = now.Add(2 * time.Second)
	if _, err := manager.Acquire(context.Background(), "run-1", "node-b", time.Second); err == nil {
		t.Fatal("expected competing owner to be rejected before extended ttl expires")
	}
	now = now.Add(4 * time.Second)
	if _, err := manager.Acquire(context.Background(), "run-1", "node-b", time.Second); err != nil {
		t.Fatalf("expected competing owner after extended ttl expiry: %v", err)
	}
}

func TestRedisLockManager_Contract(t *testing.T) {
	addr := os.Getenv("AGENT_REDIS_LOCK_TEST_ADDR")
	if addr == "" {
		t.Skip("set AGENT_REDIS_LOCK_TEST_ADDR to run Redis lock integration test")
	}
	manager, err := NewRedisLockManager(addr, os.Getenv("AGENT_REDIS_LOCK_TEST_PASSWORD"), 0, time.Hour)
	if err != nil {
		t.Fatalf("new redis lock manager: %v", err)
	}
	defer manager.Close()

	runID := "lock-contract-" + time.Now().Format("20060102150405.000000000")
	leaseA, err := manager.Acquire(context.Background(), runID, "node-a", 200*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire node-a: %v", err)
	}
	if _, err := manager.Acquire(context.Background(), runID, "node-b", time.Second); err == nil {
		t.Fatal("expected competing owner to be rejected")
	}
	if _, err := manager.Acquire(context.Background(), runID, "node-a", time.Second); err == nil {
		t.Fatal("expected same owner reacquire to be rejected before release")
	}
	if err := manager.Release(context.Background(), leaseA); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	leaseB, err := manager.Acquire(context.Background(), runID, "node-b", time.Second)
	if err != nil {
		t.Fatalf("acquire node-b after release: %v", err)
	}
	if leaseB.FencingToken <= leaseA.FencingToken {
		t.Fatalf("expected monotonic token after release, got old=%d new=%d", leaseA.FencingToken, leaseB.FencingToken)
	}
}
