package main

import (
	"context"
	"sync"

	"ai-etl-pipeline/internal/docstore"
)

// fakeDocStore is an in-memory docstore.Store for handler-level tests.
type fakeDocStore struct {
	mu   sync.Mutex
	docs map[string]docstore.Document // key: tenantID + "/" + docID
}

func newFakeDocStore() *fakeDocStore {
	return &fakeDocStore{docs: map[string]docstore.Document{}}
}

var _ docstore.Store = (*fakeDocStore)(nil)

func (f *fakeDocStore) key(tenantID, docID string) string { return tenantID + "/" + docID }

func (f *fakeDocStore) Upsert(_ context.Context, d docstore.Document) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.docs[f.key(d.TenantID, d.DocID)] = d
	return nil
}

func (f *fakeDocStore) Get(_ context.Context, tenantID, docID string) (docstore.Document, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.docs[f.key(tenantID, docID)]
	return d, ok, nil
}

func (f *fakeDocStore) Delete(_ context.Context, tenantID, docID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.docs, f.key(tenantID, docID))
	return nil
}

func (f *fakeDocStore) List(_ context.Context, q docstore.ListQuery) ([]docstore.Document, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []docstore.Document
	for _, d := range f.docs {
		if d.TenantID != q.TenantID {
			continue
		}
		if len(q.Permissions) > 0 {
			allowed := false
			for _, p := range q.Permissions {
				if p == d.Permission {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
		}
		if q.Status != "" && d.Status != q.Status {
			continue
		}
		out = append(out, d)
	}
	return out, len(out), nil
}

func (f *fakeDocStore) ReconcileUpsert(_ context.Context, tenantID, docID, permission string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := f.key(tenantID, docID)
	if _, ok := f.docs[key]; !ok {
		f.docs[key] = docstore.Document{
			TenantID: tenantID, DocID: docID, Permission: permission,
			Status: docstore.StatusCompleted, Metadata: map[string]string{},
		}
	}
	return nil
}

func (f *fakeDocStore) UpsertStatus(_ context.Context, tenantID, docID string, d docstore.Document) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := f.key(tenantID, docID)
	existing := f.docs[key]
	existing.Status = d.Status
	existing.Stage = d.Stage
	existing.ChunksDone = d.ChunksDone
	existing.ChunksTotal = d.ChunksTotal
	existing.Error = d.Error
	if !d.CompletedAt.IsZero() {
		existing.CompletedAt = d.CompletedAt
	}
	f.docs[key] = existing
	return nil
}
