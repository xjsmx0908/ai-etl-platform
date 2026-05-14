// Package checkpoint provides processing progress persistence for crash recovery.
package checkpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"ai-etl-pipeline/internal/model"
)

// --- In-memory checkpoint (development) ---

// MemoryStore provides an in-memory checkpoint for development and testing.
type MemoryStore struct {
	mu    sync.Mutex
	store map[string]model.Checkpoint
}

// NewMemoryStore creates an in-memory checkpoint store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{store: make(map[string]model.Checkpoint)}
}

func (cs *MemoryStore) Save(_ context.Context, cp model.Checkpoint) error {
	cs.mu.Lock()
	cs.store[cp.DocID] = cp
	cs.mu.Unlock()
	slog.Debug("checkpoint saved", "doc_id", cp.DocID, "chunks_done", cp.ChunksDone)
	return nil
}

func (cs *MemoryStore) Load(_ context.Context, docID string) (model.Checkpoint, bool, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cp, ok := cs.store[docID]
	return cp, ok, nil
}

func (cs *MemoryStore) Delete(_ context.Context, docID string) error {
	cs.mu.Lock()
	delete(cs.store, docID)
	cs.mu.Unlock()
	return nil
}

func (cs *MemoryStore) Close() error { return nil }

// --- Redis checkpoint (production) ---

const (
	keyPrefix = "ckpt:"
	ttl       = 24 * time.Hour
)

// RedisStore provides distributed checkpoint persistence via Redis.
type RedisStore struct {
	client *redis.Client
}

// NewRedisStore creates a Redis-backed checkpoint store and verifies connectivity.
func NewRedisStore(addr, password string, db int) (*RedisStore, error) {
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

	slog.Info("redis checkpoint connected", "addr", addr, "db", db)
	return &RedisStore{client: client}, nil
}

func (r *RedisStore) Save(ctx context.Context, cp model.Checkpoint) error {
	data, err := json.Marshal(cp)
	if err != nil {
		return fmt.Errorf("marshal checkpoint: %w", err)
	}

	key := keyPrefix + cp.DocID
	if err := r.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("redis set checkpoint: %w", err)
	}
	return nil
}

func (r *RedisStore) Load(ctx context.Context, docID string) (model.Checkpoint, bool, error) {
	key := keyPrefix + docID
	data, err := r.client.Get(ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return model.Checkpoint{}, false, nil
		}
		return model.Checkpoint{}, false, fmt.Errorf("redis get checkpoint: %w", err)
	}

	var cp model.Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return model.Checkpoint{}, false, fmt.Errorf("unmarshal checkpoint: %w", err)
	}
	return cp, true, nil
}

func (r *RedisStore) Delete(ctx context.Context, docID string) error {
	key := keyPrefix + docID
	if err := r.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("redis del checkpoint: %w", err)
	}
	return nil
}

func (r *RedisStore) Close() error {
	return r.client.Close()
}
