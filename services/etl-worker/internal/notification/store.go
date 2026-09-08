package notification

import (
	"context"
	"time"
)

// Enqueuer persists a notification event at least once. Implementations must
// treat the same DedupeKey as a no-op after the first insert.
type Enqueuer interface {
	Enqueue(context.Context, Event) error
}

// Store is the durable outbox used by the relay.
type Store interface {
	Enqueuer
	ClaimPending(ctx context.Context, limit int, lease time.Duration) ([]Event, error)
	MarkPublished(ctx context.Context, eventID string, publishedAt time.Time) error
	Release(ctx context.Context, eventID string, nextAttempt time.Time, lastError string) error
	Snapshot(ctx context.Context) (Snapshot, error)
}

// Snapshot is a low-cardinality operations view for metrics.
type Snapshot struct {
	Pending   int
	Retried   int
	OldestAge time.Duration
}
