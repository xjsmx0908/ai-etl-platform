package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// SaveOptions protects state writes from stale owners and concurrent updates.
type SaveOptions struct {
	ExpectedVersion int64
	FencingToken    int64
}

// Store persists Agent runs and step history.
type Store interface {
	CreateRun(ctx context.Context, run Run) error
	LoadRun(ctx context.Context, runID string) (Run, error)
	SaveRun(ctx context.Context, run Run, opts SaveOptions) (Run, error)
}

// MemoryStore is a single-process Store implementation for deterministic tests and dev mode.
type MemoryStore struct {
	mu   sync.RWMutex
	runs map[string]Run
}

// NewMemoryStore creates an empty in-memory Agent store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{runs: make(map[string]Run)}
}

// CreateRun stores a new run.
func (s *MemoryStore) CreateRun(_ context.Context, run Run) error {
	if run.ID == "" {
		return fmt.Errorf("run id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.runs[run.ID]; exists {
		return fmt.Errorf("run %q already exists", run.ID)
	}
	if run.Version <= 0 {
		run.Version = 1
	}
	s.runs[run.ID] = cloneRun(run)
	return nil
}

// LoadRun loads a run by id.
func (s *MemoryStore) LoadRun(_ context.Context, runID string) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.runs[runID]
	if !ok {
		return Run{}, fmt.Errorf("run %q not found", runID)
	}
	return cloneRun(run), nil
}

// SaveRun replaces the durable run record.
func (s *MemoryStore) SaveRun(_ context.Context, run Run, opts SaveOptions) (Run, error) {
	if run.ID == "" {
		return Run{}, fmt.Errorf("run id is required")
	}
	if opts.FencingToken <= 0 {
		return Run{}, fmt.Errorf("fencing token is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.runs[run.ID]
	if !exists {
		return Run{}, fmt.Errorf("run %q not found", run.ID)
	}
	if opts.ExpectedVersion > 0 && current.Version != opts.ExpectedVersion {
		return Run{}, fmt.Errorf("run %q version conflict: expected %d got %d", run.ID, opts.ExpectedVersion, current.Version)
	}
	if opts.FencingToken < current.FencingToken {
		return Run{}, fmt.Errorf("run %q stale fencing token: current %d got %d", run.ID, current.FencingToken, opts.FencingToken)
	}
	run.Version = current.Version + 1
	run.FencingToken = opts.FencingToken
	s.runs[run.ID] = cloneRun(run)
	return cloneRun(run), nil
}

// RedisStore persists Agent runs in Redis with optimistic version checks.
type RedisStore struct {
	client *redis.Client
	prefix string
	ttl    time.Duration
}

// NewRedisStore creates a Redis-backed Agent Store.
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
		return nil, fmt.Errorf("redis agent store connect failed: %w", err)
	}
	return &RedisStore{client: client, prefix: "agent:run", ttl: ttl}, nil
}

// CreateRun stores a new Redis run record.
func (s *RedisStore) CreateRun(ctx context.Context, run Run) error {
	if run.ID == "" {
		return fmt.Errorf("run id is required")
	}
	if run.Version <= 0 {
		run.Version = 1
	}
	data, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("marshal agent run: %w", err)
	}
	ok, err := s.client.SetNX(ctx, s.key(run.ID), data, s.ttl).Result()
	if err != nil {
		return fmt.Errorf("redis create agent run: %w", err)
	}
	if !ok {
		return fmt.Errorf("run %q already exists", run.ID)
	}
	return nil
}

// LoadRun loads a Redis run record.
func (s *RedisStore) LoadRun(ctx context.Context, runID string) (Run, error) {
	data, err := s.client.Get(ctx, s.key(runID)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return Run{}, fmt.Errorf("run %q not found", runID)
		}
		return Run{}, fmt.Errorf("redis load agent run: %w", err)
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return Run{}, fmt.Errorf("decode agent run: %w", err)
	}
	return cloneRun(run), nil
}

// SaveRun atomically replaces a Redis run record when version and fencing token are valid.
func (s *RedisStore) SaveRun(ctx context.Context, run Run, opts SaveOptions) (Run, error) {
	if run.ID == "" {
		return Run{}, fmt.Errorf("run id is required")
	}
	if opts.FencingToken <= 0 {
		return Run{}, fmt.Errorf("fencing token is required")
	}
	var saved Run
	key := s.key(run.ID)
	err := s.client.Watch(ctx, func(tx *redis.Tx) error {
		data, err := tx.Get(ctx, key).Bytes()
		if err != nil {
			if err == redis.Nil {
				return fmt.Errorf("run %q not found", run.ID)
			}
			return fmt.Errorf("redis load agent run: %w", err)
		}
		var current Run
		if err := json.Unmarshal(data, &current); err != nil {
			return fmt.Errorf("decode agent run: %w", err)
		}
		if opts.ExpectedVersion > 0 && current.Version != opts.ExpectedVersion {
			return fmt.Errorf("run %q version conflict: expected %d got %d", run.ID, opts.ExpectedVersion, current.Version)
		}
		if opts.FencingToken < current.FencingToken {
			return fmt.Errorf("run %q stale fencing token: current %d got %d", run.ID, current.FencingToken, opts.FencingToken)
		}
		run.Version = current.Version + 1
		run.FencingToken = opts.FencingToken
		next, err := json.Marshal(run)
		if err != nil {
			return fmt.Errorf("marshal agent run: %w", err)
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, key, next, s.ttl)
			return nil
		})
		if err != nil {
			return err
		}
		saved = cloneRun(run)
		return nil
	}, key)
	if err != nil {
		if err == redis.TxFailedErr {
			return Run{}, fmt.Errorf("run %q version conflict", run.ID)
		}
		return Run{}, err
	}
	return saved, nil
}

// Close releases Redis resources.
func (s *RedisStore) Close() error {
	return s.client.Close()
}

func (s *RedisStore) key(runID string) string {
	return s.prefix + ":" + runID
}

func cloneRun(run Run) Run {
	out := run
	if run.Steps != nil {
		out.Steps = append([]Step(nil), run.Steps...)
		for i := range out.Steps {
			if out.Steps[i].ToolArguments != nil {
				out.Steps[i].ToolArguments = append([]byte(nil), out.Steps[i].ToolArguments...)
			}
			if out.Steps[i].ToolResult != nil {
				res := *out.Steps[i].ToolResult
				res.Data = cloneMap(res.Data)
				out.Steps[i].ToolResult = &res
			}
			if out.Steps[i].CompensationResult != nil {
				res := *out.Steps[i].CompensationResult
				res.Data = cloneMap(res.Data)
				out.Steps[i].CompensationResult = &res
			}
		}
	}
	out.Memory = cloneMap(run.Memory)
	return out
}

func cloneMap(src map[string]interface{}) map[string]interface{} {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]interface{}, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
