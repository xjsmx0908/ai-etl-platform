package es

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"ai-etl-pipeline/internal/model"

	"github.com/redis/go-redis/v9"
)

const (
	queueOpIndex = "index"
)

// RetryMessage represents one full-text indexing retry job.
type RetryMessage struct {
	Op       string      `json:"op"`
	Chunk    model.Chunk `json:"chunk"`
	Retry    int         `json:"retry"`
	LastErr  string      `json:"last_err,omitempty"`
	QueuedAt time.Time   `json:"queued_at"`
	// NextRetryAt controls when this message is eligible to be replayed.
	NextRetryAt time.Time `json:"next_retry_at"`
}

// RetryQueue persists ES retry messages.
type RetryQueue interface {
	Enqueue(ctx context.Context, msg RetryMessage) error
	Pop(ctx context.Context, block time.Duration) (RetryMessage, bool, error)
	EnqueueDeadLetter(ctx context.Context, msg RetryMessage) error
	Close() error
}

// RedisRetryQueue stores retry jobs in Redis ZSET with delayed scheduling.
type RedisRetryQueue struct {
	client        *redis.Client
	key           string
	deadLetterKey string
	pollInterval  time.Duration
}

// NewRedisRetryQueue creates queue and validates connectivity.
func NewRedisRetryQueue(
	addr, password string,
	db int,
	key, deadLetterKey string,
	pollInterval time.Duration,
) (*RedisRetryQueue, error) {
	if key == "" {
		return nil, fmt.Errorf("es queue key is required")
	}
	if deadLetterKey == "" {
		return nil, fmt.Errorf("es dead-letter queue key is required")
	}
	if pollInterval <= 0 {
		pollInterval = 500 * time.Millisecond
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

	return &RedisRetryQueue{
		client:        client,
		key:           key,
		deadLetterKey: deadLetterKey,
		pollInterval:  pollInterval,
	}, nil
}

// Enqueue schedules retry message by next_retry_at in Redis ZSET.
func (q *RedisRetryQueue) Enqueue(ctx context.Context, msg RetryMessage) error {
	if msg.NextRetryAt.IsZero() {
		msg.NextRetryAt = time.Now().UTC()
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal es retry message: %w", err)
	}
	score := float64(msg.NextRetryAt.UnixMilli())
	if err := q.client.ZAdd(ctx, q.key, redis.Z{
		Score:  score,
		Member: string(data),
	}).Err(); err != nil {
		return fmt.Errorf("redis zadd es retry message: %w", err)
	}
	return nil
}

// Pop pops one due message from ZSET. It only returns messages whose next_retry_at <= now.
func (q *RedisRetryQueue) Pop(ctx context.Context, block time.Duration) (RetryMessage, bool, error) {
	if block <= 0 {
		block = q.pollInterval
	}

	deadline := time.Now().Add(block)
	for {
		msg, waitFor, ok, err := q.popDueOnce(ctx)
		if err != nil {
			return RetryMessage{}, false, err
		}
		if ok {
			return msg, true, nil
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return RetryMessage{}, false, nil
		}

		sleep := q.pollInterval
		if waitFor > 0 && waitFor < sleep {
			sleep = waitFor
		}
		if remaining < sleep {
			sleep = remaining
		}
		if sleep <= 0 {
			return RetryMessage{}, false, nil
		}

		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return RetryMessage{}, false, ctx.Err()
		case <-timer.C:
		}
	}
}

func (q *RedisRetryQueue) popDueOnce(ctx context.Context) (RetryMessage, time.Duration, bool, error) {
	now := time.Now().UTC()

	items, err := q.client.ZRangeWithScores(ctx, q.key, 0, 0).Result()
	if err != nil {
		return RetryMessage{}, 0, false, fmt.Errorf("redis zrange es retry message: %w", err)
	}
	if len(items) == 0 {
		return RetryMessage{}, 0, false, nil
	}

	dueAt := time.UnixMilli(int64(items[0].Score)).UTC()
	if dueAt.After(now) {
		return RetryMessage{}, dueAt.Sub(now), false, nil
	}

	popped, err := q.client.ZPopMin(ctx, q.key, 1).Result()
	if err != nil {
		return RetryMessage{}, 0, false, fmt.Errorf("redis zpopmin es retry message: %w", err)
	}
	if len(popped) == 0 {
		return RetryMessage{}, 0, false, nil
	}

	member := memberToString(popped[0].Member)
	poppedDueAt := time.UnixMilli(int64(popped[0].Score)).UTC()
	if poppedDueAt.After(now) {
		// Guard for race: if we popped too early, reinsert and wait.
		if err := q.client.ZAdd(ctx, q.key, redis.Z{
			Score:  popped[0].Score,
			Member: member,
		}).Err(); err != nil {
			return RetryMessage{}, 0, false, fmt.Errorf("redis requeue es retry message: %w", err)
		}
		return RetryMessage{}, poppedDueAt.Sub(now), false, nil
	}

	var msg RetryMessage
	if err := json.Unmarshal([]byte(member), &msg); err != nil {
		return RetryMessage{}, 0, false, fmt.Errorf("unmarshal es retry message: %w", err)
	}
	return msg, 0, true, nil
}

func memberToString(member interface{}) string {
	switch v := member.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// EnqueueDeadLetter stores permanently failed retry messages.
func (q *RedisRetryQueue) EnqueueDeadLetter(ctx context.Context, msg RetryMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal es dead-letter message: %w", err)
	}
	if err := q.client.RPush(ctx, q.deadLetterKey, data).Err(); err != nil {
		return fmt.Errorf("redis rpush es dead-letter message: %w", err)
	}
	return nil
}

// Close closes Redis client.
func (q *RedisRetryQueue) Close() error {
	return q.client.Close()
}

type memoryRetryItem struct {
	msg   RetryMessage
	ready time.Time
}

// MemoryRetryQueue is in-memory queue for tests/dev with delayed retry support.
type MemoryRetryQueue struct {
	mu          sync.Mutex
	closed      bool
	items       []memoryRetryItem
	deadLetters []RetryMessage
}

// NewMemoryRetryQueue creates an in-memory retry queue.
func NewMemoryRetryQueue(size int) *MemoryRetryQueue {
	if size <= 0 {
		size = 100
	}
	return &MemoryRetryQueue{
		items: make([]memoryRetryItem, 0, size),
	}
}

// Enqueue pushes message into memory queue.
func (q *MemoryRetryQueue) Enqueue(ctx context.Context, msg RetryMessage) error {
	if msg.NextRetryAt.IsZero() {
		msg.NextRetryAt = time.Now().UTC()
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return fmt.Errorf("memory retry queue closed")
	}
	q.items = append(q.items, memoryRetryItem{msg: msg, ready: msg.NextRetryAt})
	sort.Slice(q.items, func(i, j int) bool {
		return q.items[i].ready.Before(q.items[j].ready)
	})
	return nil
}

// Pop pops one message whose ready time has arrived.
func (q *MemoryRetryQueue) Pop(ctx context.Context, block time.Duration) (RetryMessage, bool, error) {
	if block <= 0 {
		block = 500 * time.Millisecond
	}

	deadline := time.Now().Add(block)
	select {
	case <-ctx.Done():
		return RetryMessage{}, false, ctx.Err()
	default:
	}

	for {
		waitFor := 200 * time.Millisecond
		q.mu.Lock()
		closed := q.closed
		if len(q.items) == 0 {
			q.mu.Unlock()
			if closed {
				return RetryMessage{}, false, nil
			}
		} else {
			now := time.Now().UTC()
			item := q.items[0]
			if !item.ready.After(now) {
				q.items = q.items[1:]
				q.mu.Unlock()
				return item.msg, true, nil
			}
			nextWait := time.Until(item.ready)
			if nextWait < waitFor {
				waitFor = nextWait
			}
			q.mu.Unlock()
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return RetryMessage{}, false, nil
		}

		if waitFor > remaining {
			waitFor = remaining
		}
		if waitFor <= 0 {
			waitFor = 10 * time.Millisecond
		}

		timer := time.NewTimer(waitFor)
		select {
		case <-ctx.Done():
			timer.Stop()
			return RetryMessage{}, false, ctx.Err()
		case <-timer.C:
		}
	}
}

// EnqueueDeadLetter stores message in dead-letter bucket for inspection in tests.
func (q *MemoryRetryQueue) EnqueueDeadLetter(_ context.Context, msg RetryMessage) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return fmt.Errorf("memory retry queue closed")
	}
	q.deadLetters = append(q.deadLetters, msg)
	return nil
}

// DeadLetters returns a copy of dead-letter messages (test helper).
func (q *MemoryRetryQueue) DeadLetters() []RetryMessage {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]RetryMessage, len(q.deadLetters))
	copy(out, q.deadLetters)
	return out
}

// Close closes in-memory queue.
func (q *MemoryRetryQueue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		slog.Debug("memory retry queue already closed")
		return nil
	}
	q.closed = true
	return nil
}
