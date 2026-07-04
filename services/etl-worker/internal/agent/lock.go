package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
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

// Acquire obtains a lock unless any owner holds a non-expired lease.
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
	if exists && now.Before(current.expiresAt) {
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

// RedisLockManager coordinates Agent run ownership across query-api replicas.
type RedisLockManager struct {
	client   *redis.Client
	prefix   string
	tokenTTL time.Duration
	now      func() time.Time
}

// NewRedisLockManager creates a Redis-backed lock manager and verifies connectivity.
func NewRedisLockManager(addr, password string, db int, tokenTTL time.Duration) (*RedisLockManager, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     20,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis agent lock manager connect failed: %w", err)
	}
	if tokenTTL <= 0 {
		tokenTTL = 24 * time.Hour
	}
	return &RedisLockManager{
		client:   client,
		prefix:   "agent:lock",
		tokenTTL: tokenTTL,
		now:      time.Now,
	}, nil
}

// Acquire obtains a run lock. Any non-expired owner blocks acquisition; use Extend to renew.
func (m *RedisLockManager) Acquire(ctx context.Context, runID, owner string, ttl time.Duration) (LockLease, error) {
	if runID == "" {
		return LockLease{}, fmt.Errorf("run id is required")
	}
	if owner == "" {
		return LockLease{}, fmt.Errorf("lock owner is required")
	}
	if ttl <= 0 {
		return LockLease{}, fmt.Errorf("lock ttl must be > 0")
	}

	var token int64
	ownerKey, tokenKey := m.ownerKey(runID), m.tokenKey(runID)
	err := m.client.Watch(ctx, func(tx *redis.Tx) error {
		currentOwner, err := tx.Get(ctx, ownerKey).Result()
		if err != nil && err != redis.Nil {
			return fmt.Errorf("redis load agent lock: %w", err)
		}
		if err == nil {
			return fmt.Errorf("run %q lock held by %q", runID, currentOwner)
		}

		pipe := tx.TxPipeline()
		incr := pipe.Incr(ctx, tokenKey)
		pipe.PExpire(ctx, tokenKey, m.tokenTTL)
		pipe.Set(ctx, ownerKey, owner, ttl)
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}
		token = incr.Val()
		return nil
	}, ownerKey, tokenKey)
	if err != nil {
		if err == redis.TxFailedErr {
			return LockLease{}, fmt.Errorf("run %q lock version conflict", runID)
		}
		return LockLease{}, err
	}
	return LockLease{
		RunID:        runID,
		Owner:        owner,
		FencingToken: token,
		ExpiresAt:    m.now().UTC().Add(ttl),
	}, nil
}

// Extend extends an existing Redis lock when the owner and fencing token still match.
func (m *RedisLockManager) Extend(ctx context.Context, lease LockLease, ttl time.Duration) (LockLease, error) {
	if ttl <= 0 {
		return LockLease{}, fmt.Errorf("lock ttl must be > 0")
	}

	ownerKey, tokenKey := m.ownerKey(lease.RunID), m.tokenKey(lease.RunID)
	err := m.client.Watch(ctx, func(tx *redis.Tx) error {
		if err := m.validateLease(ctx, tx, lease); err != nil {
			return err
		}
		pipe := tx.TxPipeline()
		pipe.PExpire(ctx, ownerKey, ttl)
		pipe.PExpire(ctx, tokenKey, m.tokenTTL)
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}
		return nil
	}, ownerKey, tokenKey)
	if err != nil {
		if err == redis.TxFailedErr {
			return LockLease{}, fmt.Errorf("lock lease is no longer valid")
		}
		return LockLease{}, err
	}
	lease.ExpiresAt = m.now().UTC().Add(ttl)
	return lease, nil
}

// Release releases a Redis lock only when the lease still matches.
func (m *RedisLockManager) Release(ctx context.Context, lease LockLease) error {
	ownerKey, tokenKey := m.ownerKey(lease.RunID), m.tokenKey(lease.RunID)
	err := m.client.Watch(ctx, func(tx *redis.Tx) error {
		if err := m.validateLease(ctx, tx, lease); err != nil {
			if err == redis.Nil {
				return nil
			}
			return err
		}
		if _, err := tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Del(ctx, ownerKey)
			pipe.PExpire(ctx, tokenKey, m.tokenTTL)
			return nil
		}); err != nil {
			return err
		}
		return nil
	}, ownerKey, tokenKey)
	if err != nil {
		if err == redis.TxFailedErr {
			return fmt.Errorf("lock lease is no longer valid")
		}
		return err
	}
	return nil
}

// Close releases Redis resources.
func (m *RedisLockManager) Close() error {
	return m.client.Close()
}

func (m *RedisLockManager) validateLease(ctx context.Context, tx *redis.Tx, lease LockLease) error {
	currentOwner, err := tx.Get(ctx, m.ownerKey(lease.RunID)).Result()
	if err != nil {
		if err == redis.Nil {
			return err
		}
		return fmt.Errorf("redis load agent lock owner: %w", err)
	}
	if currentOwner != lease.Owner {
		return fmt.Errorf("lock lease is no longer valid")
	}
	currentToken, err := tx.Get(ctx, m.tokenKey(lease.RunID)).Int64()
	if err != nil {
		if err == redis.Nil {
			return fmt.Errorf("lock lease is no longer valid")
		}
		return fmt.Errorf("redis load agent lock token: %w", err)
	}
	if currentToken != lease.FencingToken {
		return fmt.Errorf("lock lease is no longer valid")
	}
	return nil
}

func (m *RedisLockManager) ownerKey(runID string) string {
	return m.prefix + ":owner:" + runID
}

func (m *RedisLockManager) tokenKey(runID string) string {
	return m.prefix + ":token:" + runID
}
