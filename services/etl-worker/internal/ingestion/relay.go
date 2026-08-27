package ingestion

import (
	"context"
	"log/slog"
	"time"

	"ai-etl-pipeline/internal/model"
)

type Publisher interface {
	Publish(context.Context, model.Task) error
}

// Relay publishes committed outbox events at least once. A failed publish is
// left pending for the next pass; a successful publish is acknowledged only
// after Kafka accepts the task.
type Relay struct {
	Store     Store
	Publisher Publisher
	BatchSize int
	Lease     time.Duration
}

func (r Relay) RunOnce(ctx context.Context) (int, error) {
	if r.Store == nil || r.Publisher == nil {
		return 0, ErrInvalidSubmission
	}
	events, err := r.Store.ClaimPending(ctx, r.BatchSize, r.Lease)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, event := range events {
		task := event.Task
		task.JobID, task.EventID = event.JobID, event.EventID
		if err := r.Publisher.Publish(ctx, task); err != nil {
			slog.Warn("ingestion outbox publish failed", "event_id", event.EventID, "error", err)
			delay := time.Second * time.Duration(1<<min(event.Attempts, 6))
			if releaseErr := r.Store.Release(ctx, event.EventID, time.Now().UTC().Add(delay)); releaseErr != nil {
				return published, releaseErr
			}
			continue
		}
		if err := r.Store.MarkPublished(ctx, event.EventID, time.Now().UTC()); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}
