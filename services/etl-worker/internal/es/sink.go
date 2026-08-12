package es

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-etl-pipeline/internal/model"
)

// AsyncSink writes full-text chunks with eventual consistency:
// try immediate index once; on failure push to retry queue.
type AsyncSink struct {
	indexer     *HTTPIndexer
	queue       RetryQueue
	maxRetries  int
	replayEvery time.Duration
	baseBackoff time.Duration
	maxBackoff  time.Duration
	jitterRatio float64
	rand        *rand.Rand
	randMu      sync.Mutex

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup

	// onDeadLetter is invoked (non-blocking) whenever a chunk moves to the
	// dead-letter set, so callers can count it in metrics/alerting.
	onDeadLetter func()
}

// SetDeadLetterHook registers a callback invoked when a chunk is dropped to
// the ES dead-letter set. Used to surface Qdrant/ES divergence in metrics.
func (s *AsyncSink) SetDeadLetterHook(fn func()) {
	s.onDeadLetter = fn
}

// NewAsyncSink builds sink and starts replay worker.
func NewAsyncSink(
	indexer *HTTPIndexer,
	queue RetryQueue,
	replayEvery time.Duration,
	maxRetries int,
	baseBackoff time.Duration,
	maxBackoff time.Duration,
	jitterRatio float64,
) *AsyncSink {
	if replayEvery <= 0 {
		replayEvery = 2 * time.Second
	}
	if maxRetries < 1 {
		maxRetries = 12
	}
	if baseBackoff <= 0 {
		baseBackoff = 2 * time.Second
	}
	if maxBackoff <= 0 {
		maxBackoff = 5 * time.Minute
	}
	if maxBackoff < baseBackoff {
		maxBackoff = baseBackoff
	}
	if jitterRatio < 0 {
		jitterRatio = 0
	}
	if jitterRatio > 1 {
		jitterRatio = 1
	}

	s := &AsyncSink{
		indexer:     indexer,
		queue:       queue,
		maxRetries:  maxRetries,
		replayEvery: replayEvery,
		baseBackoff: baseBackoff,
		maxBackoff:  maxBackoff,
		jitterRatio: jitterRatio,
		rand:        rand.New(rand.NewSource(time.Now().UnixNano())),
		stopCh:      make(chan struct{}),
	}
	s.wg.Add(1)
	go s.replayLoop()
	return s
}

// Enqueue first tries immediate index; if failed, enters retry queue.
func (s *AsyncSink) Enqueue(ctx context.Context, chunk model.Chunk) error {
	if err := s.indexer.IndexChunk(ctx, chunk); err == nil {
		return nil
	} else {
		now := time.Now().UTC()
		msg := RetryMessage{
			Op:       queueOpIndex,
			Chunk:    chunk,
			Retry:    0,
			LastErr:  err.Error(),
			QueuedAt: now,
			// First retry should also honor backoff to avoid hot-loop during outage.
			NextRetryAt: s.nextRetryAt(now, 0),
		}
		if qErr := s.queue.Enqueue(ctx, msg); qErr != nil {
			return fmt.Errorf("es immediate index failed (%v), enqueue failed: %w", err, qErr)
		}
		slog.Warn("es immediate index failed, queued for retry",
			"chunk_id", chunk.ChunkID, "doc_id", chunk.DocID, "error", err,
			"next_retry_at", msg.NextRetryAt.Format(time.RFC3339))
		return nil
	}
}

func (s *AsyncSink) replayLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.stopCh:
			return
		default:
		}

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		msg, ok, err := s.queue.Pop(ctx, s.replayEvery)
		cancel()
		if err != nil {
			slog.Warn("es retry queue pop failed", "error", err)
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if !ok {
			continue
		}

		if msg.Op != queueOpIndex {
			slog.Warn("es retry queue message dropped due to unsupported op", "op", msg.Op)
			_ = s.pushDeadLetter(context.Background(), msg)
			continue
		}

		ctxIdx, cancelIdx := context.WithTimeout(context.Background(), 15*time.Second)
		err = s.indexer.IndexChunk(ctxIdx, msg.Chunk)
		cancelIdx()
		if err == nil {
			slog.Info("es retry indexed successfully", "chunk_id", msg.Chunk.ChunkID, "doc_id", msg.Chunk.DocID, "retry", msg.Retry)
			continue
		}

		if !isRetryableError(err) {
			msg.LastErr = err.Error()
			slog.Error("es retry non-retryable error, moved to dead-letter",
				"chunk_id", msg.Chunk.ChunkID, "doc_id", msg.Chunk.DocID, "retry", msg.Retry, "error", err)
			_ = s.pushDeadLetter(context.Background(), msg)
			continue
		}

		msg.Retry++
		msg.LastErr = err.Error()
		if msg.Retry >= s.maxRetries {
			slog.Error("es retry dropped after max retries",
				"chunk_id", msg.Chunk.ChunkID, "doc_id", msg.Chunk.DocID, "retry", msg.Retry, "error", err)
			_ = s.pushDeadLetter(context.Background(), msg)
			continue
		}
		msg.NextRetryAt = s.nextRetryAt(time.Now().UTC(), msg.Retry)

		ctxRequeue, cancelRequeue := context.WithTimeout(context.Background(), 5*time.Second)
		requeueErr := s.queue.Enqueue(ctxRequeue, msg)
		cancelRequeue()
		if requeueErr != nil {
			slog.Error("es retry requeue failed",
				"chunk_id", msg.Chunk.ChunkID, "doc_id", msg.Chunk.DocID, "retry", msg.Retry, "error", requeueErr)
			time.Sleep(500 * time.Millisecond)
			continue
		}
		slog.Warn("es retry requeued",
			"chunk_id", msg.Chunk.ChunkID, "doc_id", msg.Chunk.DocID, "retry", msg.Retry,
			"next_retry_at", msg.NextRetryAt.Format(time.RFC3339))
	}
}

// Close stops replay worker and closes dependencies.
func (s *AsyncSink) Close() error {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	s.wg.Wait()

	var firstErr error
	if err := s.queue.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := s.indexer.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (s *AsyncSink) nextRetryAt(now time.Time, retry int) time.Time {
	if retry < 0 {
		retry = 0
	}
	backoff := s.baseBackoff
	for i := 0; i < retry; i++ {
		if backoff >= s.maxBackoff/2 {
			backoff = s.maxBackoff
			break
		}
		backoff *= 2
	}
	if backoff > s.maxBackoff {
		backoff = s.maxBackoff
	}
	if backoff < time.Millisecond {
		backoff = time.Millisecond
	}
	if s.jitterRatio > 0 {
		// Apply symmetric jitter in [-jitterRatio, +jitterRatio].
		ratio := (s.randFloat64()*2 - 1) * s.jitterRatio
		j := time.Duration(float64(backoff) * ratio)
		backoff += j
		if backoff < time.Millisecond {
			backoff = time.Millisecond
		}
	}
	return now.Add(backoff)
}

func (s *AsyncSink) randFloat64() float64 {
	s.randMu.Lock()
	defer s.randMu.Unlock()
	return s.rand.Float64()
}

func (s *AsyncSink) pushDeadLetter(ctx context.Context, msg RetryMessage) error {
	dlqCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.queue.EnqueueDeadLetter(dlqCtx, msg); err != nil {
		slog.Error("es dead-letter enqueue failed",
			"chunk_id", msg.Chunk.ChunkID, "doc_id", msg.Chunk.DocID, "retry", msg.Retry, "error", err)
		return err
	}
	slog.Warn("es message moved to dead-letter",
		"chunk_id", msg.Chunk.ChunkID, "doc_id", msg.Chunk.DocID, "retry", msg.Retry)
	if s.onDeadLetter != nil {
		s.onDeadLetter()
	}
	return nil
}

var statusPattern = regexp.MustCompile(`status=(\d{3})`)

func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	matched := statusPattern.FindStringSubmatch(msg)
	if len(matched) != 2 {
		// Network/dial/timeout/context-related failures should be retried.
		return true
	}
	code, convErr := strconv.Atoi(matched[1])
	if convErr != nil {
		return true
	}
	if code >= 500 {
		return true
	}
	switch code {
	case 408, 409, 425, 429:
		return true
	default:
		return false
	}
}
