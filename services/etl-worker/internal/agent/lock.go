package agent

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// LockLease is a time-bounded ownership grant for a single run.
type LockLease struct {
	RunID        string
	Owner        string
	FencingToken int64
	ExpiresAt    time.Time
}

// LockManager coordinates distributed ownership for Agent runs.
type LockManager interface {
	Acquire(ctx context.Context, runID, owner string, ttl time.Duration) (LockLease, error)
	Extend(ctx context.Context, lease LockLease, ttl time.Duration) (LockLease, error)
	Release(ctx context.Context, lease LockLease) error
}

type lockRecord struct {
	owner        string
	fencingToken int64
	expiresAt    time.Time
}

// MemoryLockManager is a single-process lock manager for tests and dev mode.
type MemoryLockManager struct {
	mu    sync.Mutex
	locks map[string]lockRecord
	now   func() time.Time
}

// NewMemoryLockManager creates an in-memory lock manager.
func NewMemoryLockManager() *MemoryLockManager {
	return &MemoryLockManager{
		locks: make(map[string]lockRecord),
		now:   time.Now,
	}
}

// Acquire obtains a lock unless another owner holds a non-expired lease.
func (m *MemoryLockManager) Acquire(_ context.Context, runID, owner string, ttl time.Duration) (LockLease, error) {
	if runID == "" {
		return LockLease{}, fmt.Errorf("run id is required")
	}
	if owner == "" {
		return LockLease{}, fmt.Errorf("lock owner is required")
	}
	if ttl <= 0 {
		return LockLease{}, fmt.Errorf("lock ttl must be > 0")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	current, exists := m.locks[runID]
	if exists && now.Before(current.expiresAt) && current.owner != owner {
		return LockLease{}, fmt.Errorf("run %q lock held by %q", runID, current.owner)
	}

	nextToken := current.fencingToken + 1
	rec := lockRecord{
		owner:        owner,
		fencingToken: nextToken,
		expiresAt:    now.Add(ttl),
	}
	m.locks[runID] = rec
	return LockLease{RunID: runID, Owner: owner, FencingToken: nextToken, ExpiresAt: rec.expiresAt}, nil
}

// Extend extends an existing lock when the owner and fencing token still match.
func (m *MemoryLockManager) Extend(_ context.Context, lease LockLease, ttl time.Duration) (LockLease, error) {
	if ttl <= 0 {
		return LockLease{}, fmt.Errorf("lock ttl must be > 0")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	current, exists := m.locks[lease.RunID]
	if !exists || current.owner != lease.Owner || current.fencingToken != lease.FencingToken {
		return LockLease{}, fmt.Errorf("lock lease is no longer valid")
	}
	current.expiresAt = m.now().Add(ttl)
	m.locks[lease.RunID] = current
	lease.ExpiresAt = current.expiresAt
	return lease, nil
}

// Release releases a lock only when the lease still matches.
func (m *MemoryLockManager) Release(_ context.Context, lease LockLease) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	current, exists := m.locks[lease.RunID]
	if !exists {
		return nil
	}
	if current.owner != lease.Owner || current.fencingToken != lease.FencingToken {
		return fmt.Errorf("lock lease is no longer valid")
	}
	current.owner = ""
	current.expiresAt = time.Time{}
	m.locks[lease.RunID] = current
	return nil
}
