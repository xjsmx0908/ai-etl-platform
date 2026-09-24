package es

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"ai-etl-pipeline/internal/model"
)

// TestMemoryRetryQueueDeadLetterRetention pins the retention rule, because the
// rule is the only thing standing between this list and the shared Redis
// instance it lives on. The instance runs maxmemory-policy=noeviction, so an
// unbounded list does not fail loudly by itself - it fills the instance until
// every write fails, taking the retry queue, the checkpoints and the leases down
// with it. The entries kept must be the newest: those describe the incident that
// is still happening.
func TestMemoryRetryQueueDeadLetterRetention(t *testing.T) {
	q := NewMemoryRetryQueue(10).WithDeadLetterMax(3)
	defer q.Close()

	var (
		last       DeadLetterStats
		totalDrops int64
	)
	for i := 0; i < 5; i++ {
		msg := RetryMessage{Op: queueOpIndex, Chunk: model.Chunk{ChunkID: fmt.Sprintf("c%d", i)}}
		stats, err := q.EnqueueDeadLetter(context.Background(), msg)
		if err != nil {
			t.Fatalf("enqueue dead-letter %d: %v", i, err)
		}
		last = stats
		totalDrops += stats.Dropped
	}

	if last.Depth != 3 {
		t.Fatalf("depth = %d, want the cap 3", last.Depth)
	}
	// Dropped is per write, not cumulative: writes 1-3 fit under the cap and
	// report 0, then writes 4 and 5 each discard exactly one entry. The counter
	// in the metrics layer accumulates these, so the sum is what has to come out
	// right - asserting only on the last value would still pass if an earlier
	// discard had gone unreported, which is precisely the case where the loss
	// would be invisible.
	if last.Dropped != 1 {
		t.Fatalf("last write reported dropped = %d, want 1: one entry per write past the cap", last.Dropped)
	}
	if totalDrops != 2 {
		t.Fatalf("dropped %d entries in total, want 2 - the count is the only place that loss is visible", totalDrops)
	}
	items := q.DeadLetters()
	if len(items) != 3 {
		t.Fatalf("retained %d entries, want 3", len(items))
	}
	for i, want := range []string{"c2", "c3", "c4"} {
		if items[i].Chunk.ChunkID != want {
			t.Fatalf("retained[%d] = %s, want %s: the oldest entries must be the ones discarded",
				i, items[i].Chunk.ChunkID, want)
		}
	}

	// Depth read back from the queue must agree with what the write reported,
	// because the depth gauge is seeded from this call at startup.
	depth, err := q.DeadLetterDepth(context.Background())
	if err != nil {
		t.Fatalf("dead-letter depth: %v", err)
	}
	if depth != 3 {
		t.Fatalf("DeadLetterDepth = %d, want 3", depth)
	}
}

// TestRedisRetryQueueDeadLetterRetention exercises the path that actually runs
// in production. The in-memory queue above implements the trim itself, so it
// would keep passing even if the LTRIM were deleted from the Redis
// implementation - the two only agree because someone wrote them to agree. This
// test drives a real Redis instead.
//
// Skipped unless ES_TEST_REDIS_ADDR is set, and it writes to a probe key, so
// running it can never trim a real dead-letter list.
func TestRedisRetryQueueDeadLetterRetention(t *testing.T) {
	addr := os.Getenv("ES_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set ES_TEST_REDIS_ADDR (and ES_TEST_REDIS_PASSWORD) to check the Redis retention rule")
	}

	const (
		probeKey   = "es:index:deadletter:retention-probe"
		probeQueue = "es:index:retry:retention-probe"
		cap        = 3
	)
	ctx := context.Background()

	q, err := NewRedisRetryQueue(addr, os.Getenv("ES_TEST_REDIS_PASSWORD"), 0, probeQueue, probeKey, cap, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("connect redis at %s: %v", addr, err)
	}
	defer q.Close()

	// Start from a clean key: a leftover entry would make the trim look like it
	// worked when it did not.
	if err := q.client.Del(ctx, probeKey).Err(); err != nil {
		t.Fatalf("clear probe key: %v", err)
	}
	defer q.client.Del(ctx, probeKey)

	var (
		last       DeadLetterStats
		totalDrops int64
	)
	for i := 0; i < 5; i++ {
		msg := RetryMessage{Op: queueOpIndex, Chunk: model.Chunk{ChunkID: fmt.Sprintf("c%d", i)}}
		stats, err := q.EnqueueDeadLetter(ctx, msg)
		if err != nil {
			t.Fatalf("enqueue dead-letter %d: %v", i, err)
		}
		last = stats
		totalDrops += stats.Dropped

		// The cap must hold after every single write, not just at the end: if
		// the trim ever lagged behind the push, the list would be unbounded
		// between writes, which is the failure this whole change exists to stop.
		live, err := q.client.LLen(ctx, probeKey).Result()
		if err != nil {
			t.Fatalf("llen after write %d: %v", i, err)
		}
		if live > cap {
			t.Fatalf("after write %d the list holds %d entries, above the cap %d: the trim did not run",
				i, live, cap)
		}
	}

	if last.Depth != cap {
		t.Fatalf("reported depth = %d, want the cap %d", last.Depth, cap)
	}
	if totalDrops != 2 {
		t.Fatalf("dropped %d entries in total, want 2", totalDrops)
	}

	depth, err := q.DeadLetterDepth(ctx)
	if err != nil {
		t.Fatalf("dead-letter depth: %v", err)
	}
	if depth != cap {
		t.Fatalf("DeadLetterDepth = %d, want %d", depth, cap)
	}

	// The newest entries must be the survivors. LTRIM with a negative start
	// counts from the tail; if that were the wrong way round the list would keep
	// the oldest entries, which describe an incident that is already over.
	raw, err := q.client.LRange(ctx, probeKey, 0, -1).Result()
	if err != nil {
		t.Fatalf("lrange probe key: %v", err)
	}
	if len(raw) != cap {
		t.Fatalf("retained %d entries, want %d", len(raw), cap)
	}
	for i, want := range []string{"c2", "c3", "c4"} {
		var msg RetryMessage
		if err := json.Unmarshal([]byte(raw[i]), &msg); err != nil {
			t.Fatalf("decode retained[%d]: %v", i, err)
		}
		if msg.Chunk.ChunkID != want {
			t.Fatalf("retained[%d] = %s, want %s", i, msg.Chunk.ChunkID, want)
		}
	}
}

func TestMemoryRetryQueue_DelayedPopOrder(t *testing.T) {
	q := NewMemoryRetryQueue(10)
	defer q.Close()

	now := time.Now().UTC()
	msgLate := RetryMessage{Op: queueOpIndex, Chunk: model.Chunk{ChunkID: "c-late"}, NextRetryAt: now.Add(120 * time.Millisecond)}
	msgSoon := RetryMessage{Op: queueOpIndex, Chunk: model.Chunk{ChunkID: "c-soon"}, NextRetryAt: now.Add(20 * time.Millisecond)}

	if err := q.Enqueue(context.Background(), msgLate); err != nil {
		t.Fatalf("enqueue late: %v", err)
	}
	if err := q.Enqueue(context.Background(), msgSoon); err != nil {
		t.Fatalf("enqueue soon: %v", err)
	}

	started := time.Now()
	first, ok, err := q.Pop(context.Background(), 300*time.Millisecond)
	if err != nil {
		t.Fatalf("pop first: %v", err)
	}
	if !ok {
		t.Fatal("expected first pop ok=true")
	}
	if first.Chunk.ChunkID != "c-soon" {
		t.Fatalf("expected c-soon first, got %s", first.Chunk.ChunkID)
	}
	if time.Since(started) < 15*time.Millisecond {
		t.Fatalf("expected delayed pop wait, got too fast: %v", time.Since(started))
	}

	second, ok, err := q.Pop(context.Background(), 300*time.Millisecond)
	if err != nil {
		t.Fatalf("pop second: %v", err)
	}
	if !ok {
		t.Fatal("expected second pop ok=true")
	}
	if second.Chunk.ChunkID != "c-late" {
		t.Fatalf("expected c-late second, got %s", second.Chunk.ChunkID)
	}
}

func TestMemoryRetryQueue_DeadLetter(t *testing.T) {
	q := NewMemoryRetryQueue(10)
	defer q.Close()

	msg := RetryMessage{Op: queueOpIndex, Chunk: model.Chunk{ChunkID: "c1"}, Retry: 3}
	if _, err := q.EnqueueDeadLetter(context.Background(), msg); err != nil {
		t.Fatalf("enqueue dead-letter: %v", err)
	}
	items := q.DeadLetters()
	if len(items) != 1 {
		t.Fatalf("expected 1 dead-letter item, got %d", len(items))
	}
	if items[0].Chunk.ChunkID != "c1" || items[0].Retry != 3 {
		t.Fatalf("unexpected dead-letter item: %+v", items[0])
	}
}
