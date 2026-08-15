package main

import (
	"context"
	"sync"
	"time"

	"ai-etl-pipeline/internal/audit"
)

// fakeAuditStore is an in-memory audit.Store for handler-level tests.
type fakeAuditStore struct {
	mu      sync.Mutex
	entries []audit.Entry
}

func newFakeAuditStore() *fakeAuditStore {
	return &fakeAuditStore{}
}

var _ audit.Store = (*fakeAuditStore)(nil)

func (f *fakeAuditStore) Record(_ context.Context, e audit.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeAuditStore) List(_ context.Context, q audit.ListQuery) ([]audit.Entry, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []audit.Entry
	for _, e := range f.entries {
		if e.TenantID != q.TenantID {
			continue
		}
		if q.Action != "" && e.Action != q.Action {
			continue
		}
		out = append(out, e)
	}
	return out, len(out), nil
}
