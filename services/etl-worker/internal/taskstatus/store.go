// Package taskstatus provides tenant-scoped task status persistence.
package taskstatus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"ai-etl-pipeline/internal/model"
)

// MemoryStore stores task statuses in process memory for tests and development.
type MemoryStore struct {
	mu       sync.RWMutex
	statuses map[string]model.TaskStatus
}

// NewMemoryStore creates an empty in-memory task status store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{statuses: make(map[string]model.TaskStatus)}
}

// Save stores or replaces a task status.
func (s *MemoryStore) Save(_ context.Context, status model.TaskStatus) error {
	if err := validateStatus(status); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses[key(status.TenantID, status.TaskID)] = cloneStatus(status)
	return nil
}

// Load returns a tenant-scoped task status.
func (s *MemoryStore) Load(_ context.Context, tenantID string, taskID string) (model.TaskStatus, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	status, ok := s.statuses[key(tenantID, taskID)]
	if !ok {
		return model.TaskStatus{}, false, nil
	}
	return cloneStatus(status), true, nil
}

// Close releases resources.
func (s *MemoryStore) Close() error { return nil }

// RedisStore stores task statuses in Redis for cross-process API/worker sharing.
type RedisStore struct {
	client *redis.Client
	prefix string
	ttl    time.Duration
}

// NewRedisStore creates a Redis-backed task status store.
func NewRedisStore(addr, password string, db int, ttl time.Duration) (*RedisStore, error) {
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
		return nil, fmt.Errorf("redis task status connect failed: %w", err)
	}
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	return &RedisStore{client: client, prefix: "taskstatus", ttl: ttl}, nil
}

// Save stores or replaces a task status.
func (s *RedisStore) Save(ctx context.Context, status model.TaskStatus) error {
	if err := validateStatus(status); err != nil {
		return err
	}
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("marshal task status: %w", err)
	}
	if err := s.client.Set(ctx, s.key(status.TenantID, status.TaskID), data, s.ttl).Err(); err != nil {
		return fmt.Errorf("redis set task status: %w", err)
	}
	return nil
}

// Load returns a tenant-scoped task status.
func (s *RedisStore) Load(ctx context.Context, tenantID string, taskID string) (model.TaskStatus, bool, error) {
	data, err := s.client.Get(ctx, s.key(tenantID, taskID)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return model.TaskStatus{}, false, nil
		}
		return model.TaskStatus{}, false, fmt.Errorf("redis get task status: %w", err)
	}
	var status model.TaskStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return model.TaskStatus{}, false, fmt.Errorf("decode task status: %w", err)
	}
	return cloneStatus(status), true, nil
}

// Close releases Redis resources.
func (s *RedisStore) Close() error {
	return s.client.Close()
}

func (s *RedisStore) key(tenantID string, taskID string) string {
	return s.prefix + ":" + key(tenantID, taskID)
}

func validateStatus(status model.TaskStatus) error {
	if strings.TrimSpace(status.TenantID) == "" {
		return fmt.Errorf("tenant_id is required")
	}
	if strings.TrimSpace(status.TaskID) == "" {
		return fmt.Errorf("task_id is required")
	}
	if strings.TrimSpace(string(status.Status)) == "" {
		return fmt.Errorf("task status is required")
	}
	return nil
}

func key(tenantID string, taskID string) string {
	return strings.TrimSpace(tenantID) + ":" + strings.TrimSpace(taskID)
}

func cloneStatus(status model.TaskStatus) model.TaskStatus {
	out := status
	if status.Metadata != nil {
		out.Metadata = make(map[string]string, len(status.Metadata))
		for k, v := range status.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}
