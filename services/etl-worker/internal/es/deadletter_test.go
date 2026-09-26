package es

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"ai-etl-pipeline/internal/model"
)

// stubPresence answers presence from a fixed set, or fails.
type stubPresence struct {
	present map[string]bool
	err     error
	calls   int
	asked   [][]string
}

func (p *stubPresence) PresentChunkIDs(_ context.Context, chunkIDs []string) (map[string]bool, error) {
	p.calls++
	p.asked = append(p.asked, append([]string(nil), chunkIDs...))
	if p.err != nil {
		return nil, p.err
	}
	out := map[string]bool{}
	for _, id := range chunkIDs {
		if p.present[id] {
			out[id] = true
		}
	}
	return out, nil
}

// stubQueue is a RetryQueue whose dead-letter contents the test controls
// exactly. The in-memory queue cannot produce an undecodable record, and it
// re-derives its own ack rule, so the branches that matter here need a queue
// that reports what it is told.
type stubQueue struct {
	entries []DeadLetterEntry
	acked   []DeadLetterEntry
	readErr error
	ackErr  error
	// ackRemoves, when >= 0, overrides how many entries an ack reports removing.
	ackRemoves int
}

func (q *stubQueue) Enqueue(context.Context, RetryMessage) error { return nil }
func (q *stubQueue) Pop(context.Context, time.Duration) (RetryMessage, bool, error) {
	return RetryMessage{}, false, nil
}
func (q *stubQueue) EnqueueDeadLetter(context.Context, RetryMessage) (DeadLetterStats, error) {
	return DeadLetterStats{}, nil
}
func (q *stubQueue) DeadLetterDepth(context.Context) (int64, error) {
	return int64(len(q.entries)), nil
}
func (q *stubQueue) DeadLetterEntries(_ context.Context, offset, limit int64) ([]DeadLetterEntry, error) {
	if q.readErr != nil {
		return nil, q.readErr
	}
	if limit <= 0 || offset >= int64(len(q.entries)) {
		return nil, nil
	}
	end := offset + limit
	if end > int64(len(q.entries)) {
		end = int64(len(q.entries))
	}
	return q.entries[offset:end], nil
}
func (q *stubQueue) AckDeadLetterEntries(_ context.Context, entries []DeadLetterEntry) (int64, error) {
	if q.ackErr != nil {
		return 0, q.ackErr
	}
	q.acked = append(q.acked, entries...)
	if q.ackRemoves >= 0 {
		return int64(q.ackRemoves), nil
	}
	return int64(len(entries)), nil
}
func (q *stubQueue) Close() error { return nil }

func indexEntry(chunkID string) DeadLetterEntry {
	msg := RetryMessage{Op: queueOpIndex, Chunk: model.Chunk{ChunkID: chunkID, DocID: "doc-1", TenantID: "default"}}
	return DeadLetterEntry{Value: chunkID, Message: msg}
}

// TestDrainRemovesOnlyTheChunksTheIndexHolds is the whole contract in one test:
// a record whose chunk is in the index is a resolved duplicate and goes, and a
// record whose chunk is not in the index is the only description of a real gap
// and stays. Flipping the decision to "always ack" turns this red on the
// retained record; flipping it to "never ack" turns it red on the drained ones.
func TestDrainRemovesOnlyTheChunksTheIndexHolds(t *testing.T) {
	queue := &stubQueue{
		ackRemoves: -1,
		entries: []DeadLetterEntry{
			indexEntry("chunk-a"),
			indexEntry("chunk-b"),
			indexEntry("chunk-c"),
		},
	}
	presence := &stubPresence{present: map[string]bool{"chunk-a": true, "chunk-c": true}}

	stats, err := DrainResolvedDeadLetters(context.Background(), queue, presence, 0)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}

	if stats.Drained != 2 {
		t.Fatalf("drained = %d, want 2", stats.Drained)
	}
	if stats.RetainedMissing != 1 {
		t.Fatalf("retained_missing = %d, want 1", stats.RetainedMissing)
	}
	if len(queue.acked) != 2 {
		t.Fatalf("acked %d records, want 2", len(queue.acked))
	}
	for _, entry := range queue.acked {
		if entry.Message.Chunk.ChunkID == "chunk-b" {
			t.Fatal("chunk-b was acked even though the index does not hold it; " +
				"that removes the only record of a real Qdrant/ES divergence")
		}
	}
	if queue.acked[0].Value != "chunk-a" || queue.acked[1].Value != "chunk-c" {
		t.Fatalf("acked values = %q, want the stored bytes of chunk-a and chunk-c", []string{queue.acked[0].Value, queue.acked[1].Value})
	}
}

// TestDrainRetainsEverythingWhenTheIndexCannotBeRead covers the fail-closed
// branch. The dangerous reading of a failed lookup is "the index does not have
// it", which would retain; the catastrophic reading is "assume it does", which
// would delete. This asserts the first.
func TestDrainRetainsEverythingWhenTheIndexCannotBeRead(t *testing.T) {
	queue := &stubQueue{ackRemoves: -1, entries: []DeadLetterEntry{indexEntry("chunk-a"), indexEntry("chunk-b")}}
	presence := &stubPresence{err: errors.New("es unreachable")}

	stats, err := DrainResolvedDeadLetters(context.Background(), queue, presence, 0)
	if err == nil {
		t.Fatal("drain reported no error while the presence lookup failed")
	}
	if stats.RetainedUnavailable != 2 {
		t.Fatalf("retained_unavailable = %d, want 2", stats.RetainedUnavailable)
	}
	if stats.Errors != 1 {
		t.Fatalf("errors = %d, want 1: a lookup that could not be made must move the error counter", stats.Errors)
	}
	if stats.Drained != 0 || len(queue.acked) != 0 {
		t.Fatalf("drained %d records on a failed lookup; want none", stats.Drained)
	}
}

// TestDrainRetainsRecordsItCannotJudge pins the other three retain branches.
// Each of these is a record whose intent presence cannot speak for, so acking it
// would be acting on a guess.
func TestDrainRetainsRecordsItCannotJudge(t *testing.T) {
	undecodable := DeadLetterEntry{Value: "{not json", Undecodable: true}
	otherOp := DeadLetterEntry{
		Value:   "other-op",
		Message: RetryMessage{Op: "delete", Chunk: model.Chunk{ChunkID: "chunk-a"}},
	}
	noChunkID := indexEntry("   ")
	decodable := indexEntry("chunk-a")

	queue := &stubQueue{ackRemoves: -1, entries: []DeadLetterEntry{undecodable, otherOp, noChunkID, decodable}}
	presence := &stubPresence{present: map[string]bool{"chunk-a": true}}

	stats, err := DrainResolvedDeadLetters(context.Background(), queue, presence, 0)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}

	if stats.RetainedUnsupported != 3 {
		t.Fatalf("retained_unsupported = %d, want 3 (undecodable, non-index op, empty chunk id)", stats.RetainedUnsupported)
	}
	if stats.Drained != 1 {
		t.Fatalf("drained = %d, want 1", stats.Drained)
	}
	// The undecodable record's raw bytes must survive the round trip: it is the
	// only thing that could ever remove it.
	if presence.asked[0][0] != "chunk-a" {
		t.Fatalf("presence was asked about %q, want only chunk-a", presence.asked[0])
	}
}

// TestDrainPagesPastRecordsItRetains covers the reason the scan pages instead of
// reading the head and giving up. A head full of genuinely-missing chunks must
// not hide the resolved records behind them, or a single real gap would freeze
// the drain for the lifetime of the list.
func TestDrainPagesPastRecordsItRetains(t *testing.T) {
	queue := &stubQueue{ackRemoves: -1, entries: []DeadLetterEntry{
		indexEntry("missing-1"),
		indexEntry("missing-2"),
		indexEntry("chunk-a"),
		indexEntry("chunk-b"),
		indexEntry("chunk-c"),
	}}
	presence := &stubPresence{present: map[string]bool{"chunk-a": true, "chunk-b": true, "chunk-c": true}}

	stats, err := DrainResolvedDeadLetters(context.Background(), queue, presence, 2)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}

	if stats.Scanned != 5 {
		t.Fatalf("scanned = %d, want 5 (a batch of 2 must not stop at the first page)", stats.Scanned)
	}
	if stats.Drained != 3 {
		t.Fatalf("drained = %d, want 3", stats.Drained)
	}
	if stats.RetainedMissing != 2 {
		t.Fatalf("retained_missing = %d, want 2", stats.RetainedMissing)
	}
}

// TestDrainReportsAReadFailureWithoutAcking guards the case where the list
// itself cannot be read: nothing may be acked, because nothing was judged.
func TestDrainReportsAReadFailureWithoutAcking(t *testing.T) {
	queue := &stubQueue{ackRemoves: -1, readErr: errors.New("redis down")}
	presence := &stubPresence{present: map[string]bool{}}

	stats, err := DrainResolvedDeadLetters(context.Background(), queue, presence, 0)
	if err == nil {
		t.Fatal("drain reported no error while the list could not be read")
	}
	if stats.Errors != 1 {
		t.Fatalf("errors = %d, want 1", stats.Errors)
	}
	if len(queue.acked) != 0 {
		t.Fatalf("acked %d records after a failed read", len(queue.acked))
	}
}

// TestDrainReportsAShortAck keeps the two numbers apart: resolved is what the
// drain decided, drained is what the queue actually removed. Rounding the
// second up to the first would hide a list that is not being emptied.
func TestDrainReportsAShortAck(t *testing.T) {
	queue := &stubQueue{ackRemoves: 1, entries: []DeadLetterEntry{indexEntry("chunk-a"), indexEntry("chunk-b")}}
	presence := &stubPresence{present: map[string]bool{"chunk-a": true, "chunk-b": true}}

	stats, err := DrainResolvedDeadLetters(context.Background(), queue, presence, 0)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if stats.Drained != 1 {
		t.Fatalf("drained = %d, want the queue's own count of 1", stats.Drained)
	}
}

// TestDrainerRunsImmediatelyAndThenOnTheInterval pins that the first pass is not
// deferred to the first tick: the records that matter are the ones already in
// the list when the process starts.
func TestDrainerRunsImmediatelyAndThenOnTheInterval(t *testing.T) {
	queue := &stubQueue{ackRemoves: -1, entries: []DeadLetterEntry{indexEntry("chunk-a")}}
	presence := &stubPresence{present: map[string]bool{"chunk-a": true}}

	var passes int
	drainer := NewDeadLetterDrainer(queue, presence, 10*time.Millisecond).
		WithObserver(func(DeadLetterDrainStats) { passes++ })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		drainer.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for passes == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	if passes == 0 {
		t.Fatal("the drainer did not run a pass before its first tick")
	}
}

// TestDrainerIsDisabledAtZeroInterval pins that zero means off, rather than
// ticking every zero seconds.
func TestDrainerIsDisabledAtZeroInterval(t *testing.T) {
	drainer := NewDeadLetterDrainer(&stubQueue{ackRemoves: -1}, &stubPresence{}, 0)
	done := make(chan struct{})
	go func() {
		drainer.Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return for a zero interval")
	}
}

// TestRedisRetryQueueDeadLetterDrainRoundTrip drives the two new queue methods
// against a real Redis. The stub queue above implements the read and the ack
// itself, so it would keep passing if LRANGE paging or the LREM were wrong -
// the two only agree because someone wrote them to agree.
//
// Skipped unless ES_TEST_REDIS_ADDR is set, and it uses a probe key.
func TestRedisRetryQueueDeadLetterDrainRoundTrip(t *testing.T) {
	addr := os.Getenv("ES_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set ES_TEST_REDIS_ADDR (and ES_TEST_REDIS_PASSWORD) to check the Redis drain round trip")
	}

	const (
		probeKey   = "es:index:deadletter:drain-probe"
		probeQueue = "es:index:retry:drain-probe"
	)
	ctx := context.Background()

	q, err := NewRedisRetryQueue(addr, os.Getenv("ES_TEST_REDIS_PASSWORD"), 0, probeQueue, probeKey, 100, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("connect redis at %s: %v", addr, err)
	}
	defer q.Close()
	if err := q.client.Del(ctx, probeKey).Err(); err != nil {
		t.Fatalf("clear probe key: %v", err)
	}
	defer q.client.Del(ctx, probeKey)

	for i := 0; i < 3; i++ {
		msg := RetryMessage{
			Op:       queueOpIndex,
			Chunk:    model.Chunk{ChunkID: fmt.Sprintf("probe-chunk-%d", i), DocID: "probe-doc", TenantID: "default"},
			Retry:    12,
			LastErr:  "429 cluster_block_exception",
			QueuedAt: time.Unix(0, 0).UTC(),
		}
		if _, err := q.EnqueueDeadLetter(ctx, msg); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}

	presence := &stubPresence{present: map[string]bool{"probe-chunk-0": true, "probe-chunk-2": true}}
	stats, err := DrainResolvedDeadLetters(ctx, q, presence, 2)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if stats.Scanned != 3 || stats.Drained != 2 || stats.RetainedMissing != 1 {
		t.Fatalf("stats = %+v, want scanned=3 drained=2 retained_missing=1", stats)
	}

	// The list itself is the authority: the counters above are the drain's own
	// report, and this is what Redis actually holds.
	depth, err := q.DeadLetterDepth(ctx)
	if err != nil {
		t.Fatalf("depth: %v", err)
	}
	if depth != 1 {
		t.Fatalf("depth = %d, want 1", depth)
	}
	remaining, err := q.DeadLetterEntries(ctx, 0, 10)
	if err != nil {
		t.Fatalf("read remaining: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Message.Chunk.ChunkID != "probe-chunk-1" {
		t.Fatalf("remaining = %+v, want only probe-chunk-1", remaining)
	}

	// Running again must be a no-op: the retained record is retained on purpose,
	// and a drain that acked it on the second pass would be a drain that only
	// needed to be asked twice.
	second, err := DrainResolvedDeadLetters(ctx, q, presence, 2)
	if err != nil {
		t.Fatalf("second drain: %v", err)
	}
	if second.Drained != 0 || second.RetainedMissing != 1 {
		t.Fatalf("second pass = %+v, want drained=0 retained_missing=1", second)
	}
}
