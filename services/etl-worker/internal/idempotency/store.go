// Package idempotency provides deduplication for non-idempotent HTTP operations.
package idempotency

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	stateProcessing = "processing"
	stateCompleted  = "completed"
)

// ReserveState describes the current state for a request key.
type ReserveState string

const (
	ReserveNew        ReserveState = "new"
	ReserveProcessing ReserveState = "processing"
	ReserveReplay     ReserveState = "replay"
	ReserveConflict   ReserveState = "conflict"
)

// CachedResponse stores a previous successful response for replay.
type CachedResponse struct {
	StatusCode  int    `json:"status_code"`
	ContentType string `json:"content_type"`
	Body        []byte `json:"body"`
}

// ReserveResult is returned by Store.Reserve.
type ReserveResult struct {
	State  ReserveState
	Cached *CachedResponse
}

type record struct {
	State      string          `json:"state"`
	RequestSig string          `json:"request_sig"`
	Response   *CachedResponse `json:"response,omitempty"`
}

// Store is a backend for idempotency keys.
type Store interface {
	Reserve(ctx context.Context, tenantID, key, requestSig string) (ReserveResult, error)
	Complete(ctx context.Context, tenantID, key, requestSig string, resp CachedResponse) error
	Abort(ctx context.Context, tenantID, key, requestSig string) error
	Close() error
}

// MemoryStore keeps idempotency records in memory (single-process only).
type MemoryStore struct {
	mu    sync.Mutex
	ttl   time.Duration
	store map[string]memoryRecord
}

type memoryRecord struct {
	value     record
	expiresAt time.Time
}

// NewMemoryStore creates an in-memory idempotency store.
func NewMemoryStore(ttl time.Duration) *MemoryStore {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &MemoryStore{
		ttl:   ttl,
		store: make(map[string]memoryRecord),
	}
}

func (m *MemoryStore) Reserve(_ context.Context, tenantID, key, requestSig string) (ReserveResult, error) {
	composite := composeKey(tenantID, key)
	now := time.Now()

	m.mu.Lock()
	defer m.mu.Unlock()

	if v, ok := m.store[composite]; ok {
		if now.After(v.expiresAt) {
			delete(m.store, composite)
		}
	}

	if v, ok := m.store[composite]; ok {
		if v.value.RequestSig != requestSig {
			return ReserveResult{State: ReserveConflict}, nil
		}
		switch v.value.State {
		case stateProcessing:
			return ReserveResult{State: ReserveProcessing}, nil
		case stateCompleted:
			return ReserveResult{State: ReserveReplay, Cached: v.value.Response}, nil
		default:
			return ReserveResult{State: ReserveProcessing}, nil
		}
	}

	m.store[composite] = memoryRecord{
		value: record{
			State:      stateProcessing,
			RequestSig: requestSig,
		},
		expiresAt: now.Add(m.ttl),
	}
	return ReserveResult{State: ReserveNew}, nil
}

func (m *MemoryStore) Complete(_ context.Context, tenantID, key, requestSig string, resp CachedResponse) error {
	composite := composeKey(tenantID, key)

	m.mu.Lock()
	defer m.mu.Unlock()

	if v, ok := m.store[composite]; ok {
		if v.value.RequestSig != requestSig {
			return fmt.Errorf("idempotency key reused with different request")
		}
		m.store[composite] = memoryRecord{
			value: record{
				State:      stateCompleted,
				RequestSig: requestSig,
				Response:   &resp,
			},
			expiresAt: time.Now().Add(m.ttl),
		}
		return nil
	}

	m.store[composite] = memoryRecord{
		value: record{
			State:      stateCompleted,
			RequestSig: requestSig,
			Response:   &resp,
		},
		expiresAt: time.Now().Add(m.ttl),
	}
	return nil
}

func (m *MemoryStore) Abort(_ context.Context, tenantID, key, requestSig string) error {
	composite := composeKey(tenantID, key)

	m.mu.Lock()
	defer m.mu.Unlock()

	if v, ok := m.store[composite]; ok {
		if v.value.RequestSig != requestSig {
			return fmt.Errorf("idempotency key reused with different request")
		}
		// Delete only when still processing. Completed responses must remain replayable.
		if v.value.State == stateProcessing {
			delete(m.store, composite)
		}
	}
	return nil
}

func (m *MemoryStore) Close() error { return nil }

// RedisStore keeps idempotency records in Redis for multi-instance deployments.
type RedisStore struct {
	client *redis.Client
	ttl    time.Duration
}

// NewRedisStore creates a Redis-backed idempotency store.
func NewRedisStore(addr, password string, db int, ttl time.Duration) (*RedisStore, error) {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
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
		return nil, fmt.Errorf("redis connect failed: %w", err)
	}
	return &RedisStore{client: client, ttl: ttl}, nil
}

func (r *RedisStore) Reserve(ctx context.Context, tenantID, key, requestSig string) (ReserveResult, error) {
	composite := composeKey(tenantID, key)
	redisKey := "idem:" + composite

	first := record{
		State:      stateProcessing,
		RequestSig: requestSig,
	}
	data, _ := json.Marshal(first)

	set, err := r.client.SetNX(ctx, redisKey, data, r.ttl).Result()
	if err != nil {
		return ReserveResult{}, fmt.Errorf("redis reserve idempotency key: %w", err)
	}
	if set {
		return ReserveResult{State: ReserveNew}, nil
	}

	current, err := r.client.Get(ctx, redisKey).Bytes()
	if err != nil {
		if err == redis.Nil {
			// Key was evicted between SetNX and Get; let caller retry next request.
			return ReserveResult{State: ReserveProcessing}, nil
		}
		return ReserveResult{}, fmt.Errorf("redis get idempotency key: %w", err)
	}

	var rec record
	if err := json.Unmarshal(current, &rec); err != nil {
		return ReserveResult{}, fmt.Errorf("decode idempotency record: %w", err)
	}
	if rec.RequestSig != requestSig {
		return ReserveResult{State: ReserveConflict}, nil
	}
	switch rec.State {
	case stateCompleted:
		return ReserveResult{State: ReserveReplay, Cached: rec.Response}, nil
	case stateProcessing:
		return ReserveResult{State: ReserveProcessing}, nil
	default:
		return ReserveResult{State: ReserveProcessing}, nil
	}
}

func (r *RedisStore) Complete(ctx context.Context, tenantID, key, requestSig string, resp CachedResponse) error {
	composite := composeKey(tenantID, key)
	redisKey := "idem:" + composite

	current, err := r.client.Get(ctx, redisKey).Bytes()
	if err != nil && err != redis.Nil {
		return fmt.Errorf("redis get idempotency key: %w", err)
	}
	if err == nil {
		var rec record
		if uerr := json.Unmarshal(current, &rec); uerr == nil {
			if rec.RequestSig != requestSig {
				return fmt.Errorf("idempotency key reused with different request")
			}
		}
	}

	completed := record{
		State:      stateCompleted,
		RequestSig: requestSig,
		Response:   &resp,
	}
	data, _ := json.Marshal(completed)
	if err := r.client.Set(ctx, redisKey, data, r.ttl).Err(); err != nil {
		return fmt.Errorf("redis set idempotency response: %w", err)
	}
	return nil
}

func (r *RedisStore) Abort(ctx context.Context, tenantID, key, requestSig string) error {
	composite := composeKey(tenantID, key)
	redisKey := "idem:" + composite

	current, err := r.client.Get(ctx, redisKey).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil
		}
		return fmt.Errorf("redis get idempotency key: %w", err)
	}

	var rec record
	if err := json.Unmarshal(current, &rec); err != nil {
		return fmt.Errorf("decode idempotency record: %w", err)
	}
	if rec.RequestSig != requestSig {
		return fmt.Errorf("idempotency key reused with different request")
	}

	if rec.State == stateProcessing {
		if err := r.client.Del(ctx, redisKey).Err(); err != nil {
			return fmt.Errorf("redis delete idempotency key: %w", err)
		}
	}
	return nil
}

func (r *RedisStore) Close() error {
	return r.client.Close()
}

func composeKey(tenantID, key string) string {
	return tenantID + ":" + key
}
