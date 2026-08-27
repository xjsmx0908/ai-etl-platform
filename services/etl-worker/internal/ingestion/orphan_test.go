package ingestion

import (
	"context"
	"errors"
	"testing"
	"time"
)

type orphanInventoryStub struct {
	objects []ObjectCandidate
	deleted []string
}

func (s *orphanInventoryStub) ListOlderThan(context.Context, time.Time, int) ([]ObjectCandidate, error) {
	return append([]ObjectCandidate(nil), s.objects...), nil
}

func (s *orphanInventoryStub) Delete(_ context.Context, key string) error {
	s.deleted = append(s.deleted, key)
	return nil
}

type objectReferencesStub struct {
	referenced map[string]bool
	err        error
}

func (s objectReferencesStub) IsObjectReferenced(_ context.Context, key string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.referenced[key], nil
}

func TestOrphanCollectorDeletesOnlyOldUnreferencedObjects(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	inventory := &orphanInventoryStub{objects: []ObjectCandidate{
		{Key: "tenant/doc/versions/old.txt", ModifiedAt: now.Add(-25 * time.Hour)},
		{Key: "tenant/doc/versions/live.txt", ModifiedAt: now.Add(-48 * time.Hour)},
		{Key: "tenant/doc/versions/new.txt", ModifiedAt: now.Add(-time.Hour)},
		{Key: "tenant/legacy-untracked.txt", ModifiedAt: now.Add(-72 * time.Hour)},
	}}
	references := objectReferencesStub{referenced: map[string]bool{"tenant/doc/versions/live.txt": true}}
	collector := OrphanCollector{Objects: inventory, References: references, GracePeriod: 24 * time.Hour, BatchSize: 100, Now: func() time.Time { return now }}

	deleted, err := collector.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if deleted != 1 || len(inventory.deleted) != 1 || inventory.deleted[0] != "tenant/doc/versions/old.txt" {
		t.Fatalf("deleted = %d, keys=%v", deleted, inventory.deleted)
	}
}

func TestOrphanCollectorFailsClosedWhenReferenceCheckFails(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	inventory := &orphanInventoryStub{objects: []ObjectCandidate{{Key: "tenant/doc/versions/old.txt", ModifiedAt: now.Add(-48 * time.Hour)}}}
	collector := OrphanCollector{
		Objects: inventory, References: objectReferencesStub{err: errors.New("postgres unavailable")},
		GracePeriod: 24 * time.Hour, Now: func() time.Time { return now },
	}

	if _, err := collector.RunOnce(context.Background()); err == nil {
		t.Fatal("expected reference lookup failure")
	}
	if len(inventory.deleted) != 0 {
		t.Fatalf("deleted on uncertain reference state: %v", inventory.deleted)
	}
}
