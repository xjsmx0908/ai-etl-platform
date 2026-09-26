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

	// defaultDeadLetterMax bounds the dead-letter list. The number is a size
	// decision, not a round one: a measured dead-letter entry is ~25KB because
	// RetryMessage embeds the whole chunk *including its embedding vector*
	// (90 entries measured at 2.1MB on the deployed stack), so 2000 entries is
	// about 50MB - roughly a tenth of the 512MB Redis instance they share with
	// the retry queue, the ingestion checkpoints, the job leases and the outbox.
	defaultDeadLetterMax = 2000
)

// DeadLetterStats reports the dead-letter queue as it stands after one write.
type DeadLetterStats struct {
	// Depth is how many entries are retained, after the write and the trim.
	Depth int64
	// Dropped is how many of the oldest entries this write had to discard to
	// stay within the cap. Anything above zero means diagnostic records were
	// lost, and it is the only place that loss is visible.
	Dropped int64
}

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
	EnqueueDeadLetter(ctx context.Context, msg RetryMessage) (DeadLetterStats, error)
	DeadLetterDepth(ctx context.Context) (int64, error)
	// DeadLetterEntries reads one page of retained dead-letter records, oldest
	// first, so the drain can decide what the list is still holding.
	DeadLetterEntries(ctx context.Context, offset, limit int64) ([]DeadLetterEntry, error)
	// AckDeadLetterEntries removes exactly the records it is given. Removal is
	// by value rather than by index, because the list is a Redis list of opaque
	// strings and an index is only valid until the next write.
	AckDeadLetterEntries(ctx context.Context, entries []DeadLetterEntry) (int64, error)
	Close() error
}

// RedisRetryQueue stores retry jobs in Redis ZSET with delayed scheduling.
type RedisRetryQueue struct {
	client        *redis.Client
	key           string
	deadLetterKey string
	deadLetterMax int
	pollInterval  time.Duration
}

// NewRedisRetryQueue creates queue and validates connectivity.
func NewRedisRetryQueue(
	addr, password string,
	db int,
	key, deadLetterKey string,
	deadLetterMax int,
	pollInterval time.Duration,
) (*RedisRetryQueue, error) {
	if key == "" {
		return nil, fmt.Errorf("es queue key is required")
	}
	if deadLetterKey == "" {
		return nil, fmt.Errorf("es dead-letter queue key is required")
	}
	if deadLetterMax <= 0 {
		deadLetterMax = defaultDeadLetterMax
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
		deadLetterMax: deadLetterMax,
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

// EnqueueDeadLetter stores permanently failed retry messages, keeping only the
// newest deadLetterMax entries.
//
// The cap is not tidiness. This list shares one Redis instance with the retry
// queue, the ingestion checkpoints, the job leases and the outbox, and that
// instance runs with maxmemory-policy=noeviction - so an unbounded list does not
// fail on its own, it fills the instance until *every* write fails and ingestion
// stops with it. The scenario that fills it is the same one that created the
// entries: ES rejecting writes for long enough that chunks exhaust their retries.
func (q *RedisRetryQueue) EnqueueDeadLetter(ctx context.Context, msg RetryMessage) (DeadLetterStats, error) {
	var stats DeadLetterStats
	data, err := json.Marshal(msg)
	if err != nil {
		return stats, fmt.Errorf("marshal es dead-letter message: %w", err)
	}
	// Push, read the length and trim in one round trip: three separate calls
	// would be three chances to leave the list over its cap if one of them
	// failed, and this path runs while ES is already unhealthy.
	pipe := q.client.Pipeline()
	pipe.RPush(ctx, q.deadLetterKey, data)
	depthCmd := pipe.LLen(ctx, q.deadLetterKey)
	// LTRIM with a negative start counts from the tail, so -max..-1 keeps
	// exactly the newest max entries. A shorter list is left alone.
	pipe.LTrim(ctx, q.deadLetterKey, int64(-q.deadLetterMax), -1)
	if _, err := pipe.Exec(ctx); err != nil {
		return stats, fmt.Errorf("redis push es dead-letter message: %w", err)
	}
	stats.Depth = depthCmd.Val()
	if stats.Depth > int64(q.deadLetterMax) {
		stats.Dropped = stats.Depth - int64(q.deadLetterMax)
		stats.Depth = int64(q.deadLetterMax)
	}
	return stats, nil
}

// DeadLetterDepth reports the current number of retained entries. It exists so
// the depth gauge can be seeded at startup: without that the series would appear
// only once something had already gone wrong, and a panel asking "how many are
// stuck" would read as zero until the first failure.
func (q *RedisRetryQueue) DeadLetterDepth(ctx context.Context) (int64, error) {
	depth, err := q.client.LLen(ctx, q.deadLetterKey).Result()
	if err != nil {
		return 0, fmt.Errorf("redis llen es dead-letter: %w", err)
	}
	return depth, nil
}

// DeadLetterEntries reads a page of retained entries, oldest first.
//
// A record whose JSON no longer decodes is returned with an empty Message and
// its raw value intact: the drain has to be able to remove it (nothing else
// ever will) without pretending it understood it.
func (q *RedisRetryQueue) DeadLetterEntries(ctx context.Context, offset, limit int64) ([]DeadLetterEntry, error) {
	if limit <= 0 {
		return nil, nil
	}
	if offset < 0 {
		offset = 0
	}
	values, err := q.client.LRange(ctx, q.deadLetterKey, offset, offset+limit-1).Result()
	if err != nil {
		return nil, fmt.Errorf("redis lrange es dead-letter: %w", err)
	}
	entries := make([]DeadLetterEntry, 0, len(values))
	for _, value := range values {
		entry := DeadLetterEntry{Value: value}
		var msg RetryMessage
		if err := json.Unmarshal([]byte(value), &msg); err == nil {
			entry.Message = msg
		} else {
			entry.Undecodable = true
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// AckDeadLetterEntries removes the given records with one LREM each, in one
// round trip. LREM count=1 removes a single occurrence, so N records that
// happen to hold identical bytes still remove N entries.
func (q *RedisRetryQueue) AckDeadLetterEntries(ctx context.Context, entries []DeadLetterEntry) (int64, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	pipe := q.client.Pipeline()
	cmds := make([]*redis.IntCmd, 0, len(entries))
	for _, entry := range entries {
		cmds = append(cmds, pipe.LRem(ctx, q.deadLetterKey, 1, entry.Value))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("redis lrem es dead-letter: %w", err)
	}
	var removed int64
	for _, cmd := range cmds {
		removed += cmd.Val()
	}
	return removed, nil
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
	mu            sync.Mutex
	closed        bool
	items         []memoryRetryItem
	deadLetters   []RetryMessage
	deadLetterMax int
}

// NewMemoryRetryQueue creates an in-memory retry queue.
func NewMemoryRetryQueue(size int) *MemoryRetryQueue {
	if size <= 0 {
		size = 100
	}
	return &MemoryRetryQueue{
		items:         make([]memoryRetryItem, 0, size),
		deadLetterMax: defaultDeadLetterMax,
	}
}

// WithDeadLetterMax overrides the dead-letter cap. The Redis queue takes it from
// configuration; the in-memory queue is test/dev only, so it carries the default
// and this exists so a test can drive the trim without 2000 pushes.
func (q *MemoryRetryQueue) WithDeadLetterMax(max int) *MemoryRetryQueue {
	if max > 0 {
		q.deadLetterMax = max
	}
	return q
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
// It applies the same retention rule as the Redis queue so a test that drives
// the trim exercises the real semantics rather than a simplified copy.
func (q *MemoryRetryQueue) EnqueueDeadLetter(_ context.Context, msg RetryMessage) (DeadLetterStats, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return DeadLetterStats{}, fmt.Errorf("memory retry queue closed")
	}
	q.deadLetters = append(q.deadLetters, msg)
	var stats DeadLetterStats
	if over := len(q.deadLetters) - q.deadLetterMax; over > 0 {
		// Drop from the front: the newest entries describe the incident that is
		// still happening, the oldest describe one that is over.
		q.deadLetters = append([]RetryMessage(nil), q.deadLetters[over:]...)
		stats.Dropped = int64(over)
	}
	stats.Depth = int64(len(q.deadLetters))
	return stats, nil
}

// DeadLetterDepth reports the retained dead-letter count.
func (q *MemoryRetryQueue) DeadLetterDepth(_ context.Context) (int64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return int64(len(q.deadLetters)), nil
}

// DeadLetterEntries reads a page of retained entries, oldest first. The values
// are the same JSON the Redis queue stores, so a test that drives the drain
// exercises the real identity rule rather than a simplified copy of it.
func (q *MemoryRetryQueue) DeadLetterEntries(_ context.Context, offset, limit int64) ([]DeadLetterEntry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if limit <= 0 || offset >= int64(len(q.deadLetters)) {
		return nil, nil
	}
	if offset < 0 {
		offset = 0
	}
	end := offset + limit
	if end > int64(len(q.deadLetters)) {
		end = int64(len(q.deadLetters))
	}
	entries := make([]DeadLetterEntry, 0, end-offset)
	for _, msg := range q.deadLetters[offset:end] {
		data, err := json.Marshal(msg)
		if err != nil {
			return nil, fmt.Errorf("marshal es dead-letter message: %w", err)
		}
		entries = append(entries, DeadLetterEntry{Value: string(data), Message: msg})
	}
	return entries, nil
}

// AckDeadLetterEntries removes one occurrence of each given value.
func (q *MemoryRetryQueue) AckDeadLetterEntries(_ context.Context, entries []DeadLetterEntry) (int64, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	var removed int64
	for _, entry := range entries {
		for i, msg := range q.deadLetters {
			data, err := json.Marshal(msg)
			if err != nil {
				continue
			}
			if string(data) != entry.Value {
				continue
			}
			q.deadLetters = append(q.deadLetters[:i], q.deadLetters[i+1:]...)
			removed++
			break
		}
	}
	return removed, nil
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
