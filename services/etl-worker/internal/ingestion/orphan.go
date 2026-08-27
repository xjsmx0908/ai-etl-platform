package ingestion

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type ObjectCandidate struct {
	Key        string
	ModifiedAt time.Time
}

type ObjectInventory interface {
	ListOlderThan(context.Context, time.Time, int) ([]ObjectCandidate, error)
	Delete(context.Context, string) error
}

type ObjectReferences interface {
	IsObjectReferenced(context.Context, string) (bool, error)
}

// OrphanCollector closes the object-store/database transaction gap. It deletes
// only objects older than the grace period and proven absent from both the
// current document catalog and durable ingestion jobs. Reference uncertainty
// always fails closed.
type OrphanCollector struct {
	Objects     ObjectInventory
	References  ObjectReferences
	GracePeriod time.Duration
	BatchSize   int
	Now         func() time.Time
}

func (c OrphanCollector) RunOnce(ctx context.Context) (int, error) {
	if c.Objects == nil || c.References == nil || c.GracePeriod <= 0 {
		return 0, ErrInvalidSubmission
	}
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	limit := c.BatchSize
	if limit <= 0 {
		limit = 100
	}
	cutoff := now.Add(-c.GracePeriod)
	objects, err := c.Objects.ListOlderThan(ctx, cutoff, limit)
	if err != nil {
		return 0, fmt.Errorf("list orphan candidates: %w", err)
	}
	deleted := 0
	for _, object := range objects {
		// Defend against inventories that do not honor the requested cutoff.
		if object.Key == "" || !strings.Contains(object.Key, "/versions/") || !object.ModifiedAt.Before(cutoff) {
			continue
		}
		referenced, err := c.References.IsObjectReferenced(ctx, object.Key)
		if err != nil {
			return deleted, fmt.Errorf("check object reference: %w", err)
		}
		if referenced {
			continue
		}
		if err := c.Objects.Delete(ctx, object.Key); err != nil {
			return deleted, fmt.Errorf("delete orphan object %s: %w", object.Key, err)
		}
		deleted++
	}
	return deleted, nil
}
