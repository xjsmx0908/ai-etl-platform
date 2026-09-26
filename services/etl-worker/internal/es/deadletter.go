package es

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// defaultDeadLetterDrainBatch is how many records one read of the dead-letter
// list pulls. It is a read-size decision, not a retention one: a measured
// record is ~25KB because RetryMessage embeds the chunk and its embedding
// vector, so a page is about 5MB and the whole capped list (2000 records) is
// about 50MB - which is why the drain pages instead of reading the list whole.
const defaultDeadLetterDrainBatch = 200

// deadLetterDrainTimeout bounds one pass. A pass talks to Redis once per page
// and to Elasticsearch once, and it runs on a timer, so a pass that hangs must
// not be able to overlap the next one.
const deadLetterDrainTimeout = 2 * time.Minute

// DeadLetterEntry is one retained dead-letter record together with the exact
// bytes it is stored as.
//
// The raw value travels with the message because removal is by value: the
// dead-letter set is a Redis list of opaque strings, and an index into it is
// only valid until the next write. Carrying the bytes from the read to the ack
// is what makes the ack name a specific record rather than a position.
type DeadLetterEntry struct {
	Value   string
	Message RetryMessage
	// Undecodable marks a record whose JSON no longer parses. It is retained
	// rather than removed: the drain cannot judge an intent it cannot read, and
	// this is the only copy of it.
	Undecodable bool
}

// ChunkPresence answers "does the full-text index already hold this chunk".
// It is an interface so the drain's decision can be tested without a live
// Elasticsearch, and so the judgement stays separable from the transport.
type ChunkPresence interface {
	PresentChunkIDs(ctx context.Context, chunkIDs []string) (map[string]bool, error)
}

// DeadLetterDrainStats reports one pass.
//
// Drained is the only number that means "the list got shorter". The three
// retained_* numbers are the interesting half: each one is a record that is
// still there, and each says something different about why.
//
// Errors counts the passes - or, when paging, the pages - that failed outright.
// A pass can fail and still drain, so Errors and Drained are not exclusive.
type DeadLetterDrainStats struct {
	Scanned             int
	Drained             int
	RetainedMissing     int
	RetainedUnavailable int
	RetainedUnsupported int
	Errors              int
}

// DrainResolvedDeadLetters removes the dead-letter records whose intent the
// full-text index has already satisfied.
//
// The intent of a dead-letter record is narrow: it was created because one
// chunk could not be written to Elasticsearch, and its whole purpose is that
// write. So "the index already holds this chunk id" means the intent is met and
// the record is a stale duplicate; "the index does not hold it" means the gap
// is real and the record is the only description of it.
//
// That asymmetry is the design. A record that is wrongly acked is gone, and
// with it the evidence of a divergence between Qdrant and Elasticsearch;
// a record that is wrongly retained costs a few kilobytes and one line on a
// dashboard. So every branch that cannot decide retains:
//
//   - a presence lookup that errored, timed out or was refused retains;
//   - a record whose op is not "index" retains (presence cannot speak for it);
//   - a record that does not decode retains;
//   - a record whose chunk id is empty retains.
//
// This is deliberately not a replay. Replaying would re-index the chunk from a
// copy taken at failure time, which overwrites whatever the index holds now
// with an older version of the block; the correct repair for a real gap is to
// re-run ingestion, which the reconciler already schedules from the manifest.
func DrainResolvedDeadLetters(ctx context.Context, queue RetryQueue, presence ChunkPresence, batch int64) (DeadLetterDrainStats, error) {
	var stats DeadLetterDrainStats
	if batch <= 0 {
		batch = defaultDeadLetterDrainBatch
	}

	// The whole list is scanned before anything is removed. Paging by offset is
	// only stable while the list is unchanged, and deferring the ack also turns
	// one write per record into one write per pass.
	var (
		ackable  []DeadLetterEntry
		offset   int64
		firstErr error
	)
	for {
		entries, err := queue.DeadLetterEntries(ctx, offset, batch)
		if err != nil {
			stats.Errors++
			return stats, fmt.Errorf("read es dead-letter entries: %w", err)
		}
		if len(entries) == 0 {
			break
		}
		stats.Scanned += len(entries)
		offset += int64(len(entries))

		resolved, presenceErr := resolveDeadLetterEntries(ctx, presence, entries, &stats)
		ackable = append(ackable, resolved...)
		if presenceErr != nil {
			stats.Errors++
			if firstErr == nil {
				firstErr = presenceErr
			}
			slog.Warn("es dead-letter drain could not read the full-text index; records retained",
				"records", len(entries), "error", presenceErr)
		}

		if len(entries) < int(batch) {
			break
		}
	}

	if len(ackable) > 0 {
		removed, err := queue.AckDeadLetterEntries(ctx, ackable)
		if err != nil {
			stats.Errors++
			return stats, fmt.Errorf("ack es dead-letter entries: %w", err)
		}
		stats.Drained = int(removed)
		if removed != int64(len(ackable)) {
			// LREM reports what it actually removed. A short count means the list
			// changed underneath the scan - the writer appends, it never removes,
			// so this should not happen and is worth saying out loud rather than
			// rounding away.
			slog.Warn("es dead-letter drain removed fewer records than it resolved",
				"resolved", len(ackable), "removed", removed)
		}
	}

	// The error is returned even when the pass drained records from the pages it
	// could read: a caller that saw only "0 errors" would take a partly-blind
	// pass for a clean one, and the whole point of this drain is that a lookup it
	// could not make must not read as a lookup that found nothing.
	if firstErr != nil {
		return stats, fmt.Errorf("es dead-letter drain could not read the full-text index: %w", firstErr)
	}
	return stats, nil
}

// resolveDeadLetterEntries decides one page. It returns the records the index
// has satisfied, and the presence error if there was one - in which case the
// returned slice is empty and every candidate was counted as retained.
func resolveDeadLetterEntries(ctx context.Context, presence ChunkPresence, entries []DeadLetterEntry, stats *DeadLetterDrainStats) ([]DeadLetterEntry, error) {
	candidates := make([]DeadLetterEntry, 0, len(entries))
	chunkIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Undecodable || entry.Message.Op != queueOpIndex {
			stats.RetainedUnsupported++
			continue
		}
		chunkID := strings.TrimSpace(entry.Message.Chunk.ChunkID)
		if chunkID == "" {
			stats.RetainedUnsupported++
			continue
		}
		candidates = append(candidates, entry)
		chunkIDs = append(chunkIDs, chunkID)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	present, err := presence.PresentChunkIDs(ctx, chunkIDs)
	if err != nil {
		stats.RetainedUnavailable += len(candidates)
		return nil, err
	}

	ackable := make([]DeadLetterEntry, 0, len(candidates))
	for _, entry := range candidates {
		if present[strings.TrimSpace(entry.Message.Chunk.ChunkID)] {
			ackable = append(ackable, entry)
			continue
		}
		stats.RetainedMissing++
	}
	return ackable, nil
}

// DeadLetterDrainer runs DrainResolvedDeadLetters on a timer.
//
// Without it the dead-letter list only ever grows: the retry path writes to it
// and nothing reads it, so a resolved incident stays on the dashboard forever
// and the depth gauge never returns to zero. The list is bounded
// (ES_DEADLETTER_MAX) and the bound discards the *oldest* records, so an
// undrained list is not merely untidy - it is slowly deleting the record of
// what failed in order to make room for what failed later.
type DeadLetterDrainer struct {
	queue    RetryQueue
	presence ChunkPresence
	interval time.Duration
	observe  func(DeadLetterDrainStats)
}

// NewDeadLetterDrainer builds a drainer. An interval of zero or less disables
// it, which is why Run is not started in that case rather than ticking forever.
func NewDeadLetterDrainer(queue RetryQueue, presence ChunkPresence, interval time.Duration) *DeadLetterDrainer {
	return &DeadLetterDrainer{queue: queue, presence: presence, interval: interval}
}

// WithObserver registers a callback invoked after every pass, including the
// ones that drained nothing.
func (d *DeadLetterDrainer) WithObserver(fn func(DeadLetterDrainStats)) *DeadLetterDrainer {
	d.observe = fn
	return d
}

// Run drains immediately and then on every interval, until ctx is done.
//
// The first pass is not deferred to the first tick: the records that matter are
// the ones already in the list when this process starts, and a restart is
// exactly when an operator expects the backlog to be dealt with.
func (d *DeadLetterDrainer) Run(ctx context.Context) {
	if d == nil || d.queue == nil || d.presence == nil || d.interval <= 0 {
		return
	}

	drainOnce := func() {
		passCtx, cancel := context.WithTimeout(ctx, deadLetterDrainTimeout)
		defer cancel()
		stats, err := DrainResolvedDeadLetters(passCtx, d.queue, d.presence, 0)
		if err != nil {
			slog.Warn("es dead-letter drain pass failed", "error", err)
		}
		if stats.Scanned > 0 || stats.Drained > 0 || err != nil {
			slog.Info("es dead-letter drain pass complete",
				"scanned", stats.Scanned, "drained", stats.Drained,
				"retained_missing", stats.RetainedMissing,
				"retained_unavailable", stats.RetainedUnavailable,
				"retained_unsupported", stats.RetainedUnsupported)
		}
		if d.observe != nil {
			d.observe(stats)
		}
	}

	drainOnce()

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			drainOnce()
		}
	}
}
