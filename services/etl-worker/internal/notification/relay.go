package notification

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Dispatcher delivers one sanitized event to an external channel.
type Dispatcher interface {
	Dispatch(context.Context, Event) error
}

// ErrSkipped means the event was intentionally not sent, for example because
// no webhook is configured. The relay treats this as a successful close.
var ErrSkipped = errors.New("notification delivery skipped")

// Relay publishes committed notification events at least once.
type Relay struct {
	Store      Store
	Dispatcher Dispatcher
	BatchSize  int
	Lease      time.Duration
}

func (r Relay) RunOnce(ctx context.Context) (int, error) {
	if r.Store == nil || r.Dispatcher == nil {
		return 0, fmt.Errorf("notification relay is not configured")
	}
	events, err := r.Store.ClaimPending(ctx, r.BatchSize, r.Lease)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, event := range events {
		if err := r.Dispatcher.Dispatch(ctx, event); err != nil {
			if errors.Is(err, ErrSkipped) {
				if markErr := r.Store.MarkPublished(ctx, event.ID, time.Now().UTC()); markErr != nil {
					return published, markErr
				}
				published++
				continue
			}
			slog.Warn("governance notification delivery failed", "event_id", event.ID, "event_type", event.Type, "error", err)
			delay := time.Second * time.Duration(1<<min(event.Attempts, 6))
			if releaseErr := r.Store.Release(ctx, event.ID, time.Now().UTC().Add(delay), err.Error()); releaseErr != nil {
				return published, releaseErr
			}
			continue
		}
		if err := r.Store.MarkPublished(ctx, event.ID, time.Now().UTC()); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}
