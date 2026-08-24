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
	key := f.key(d.TenantID, d.DocID)
	// Mirror the real store's governance semantics: an empty governance field
	// means "not supplied" and preserves the stored value, so a plain re-upload
	// cannot silently clear an owner or an obsolescence marking. Without this the
	// fake would be more permissive than PostgreSQL and hide such a regression.
	if prev, ok := f.docs[key]; ok {
		if d.DocStatus == "" {
			d.DocStatus = prev.DocStatus
		}
		if d.EffectiveDate.IsZero() {
			d.EffectiveDate = prev.EffectiveDate
		}
		if d.Supersedes == "" {
			d.Supersedes = prev.Supersedes
		}
		if d.Owner == "" {
			d.Owner = prev.Owner
		}
	}
	if d.DocStatus == "" {
		d.DocStatus = docstore.DocStatusActive
	}
	if d.KnowledgeSpaceID == "" {
		d.KnowledgeSpaceID = "user-uploads"
	}
	if d.PublicationStatus == "" {
		d.PublicationStatus = "draft"
	}
	f.docs[key] = d
	return nil
}

func (f *fakeDocStore) Get(_ context.Context, tenantID, docID string) (docstore.Document, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.docs[f.key(tenantID, docID)]
	return d, ok, nil
}

// GetByHash mirrors PgStore: only completed documents count as duplicates.
func (f *fakeDocStore) GetByHash(_ context.Context, tenantID, knowledgeSpaceID, fileHash string) (docstore.Document, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if fileHash == "" {
		return docstore.Document{}, false, nil
	}
	for _, d := range f.docs {
		if d.TenantID == tenantID && d.KnowledgeSpaceID == knowledgeSpaceID && d.FileHash == fileHash && d.Status == docstore.StatusCompleted {
			return d, true, nil
		}
	}
	return docstore.Document{}, false, nil
}

func (f *fakeDocStore) GovernanceByDocIDs(_ context.Context, tenantID string, docIDs []string) (map[string]docstore.Governance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]docstore.Governance{}
	for _, id := range docIDs {
		d, ok := f.docs[f.key(tenantID, id)]
		if !ok {
			continue
		}
		out[id] = docstore.Governance{
			DocID: d.DocID, DocStatus: d.DocStatus, EffectiveDate: d.EffectiveDate,
			Supersedes: d.Supersedes, FileName: d.FileName,
		}
	}
	return out, nil
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
		if len(q.KnowledgeSpaceIDs) > 0 {
			allowed := false
			for _, id := range q.KnowledgeSpaceIDs {
				if d.KnowledgeSpaceID == id {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
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

func (f *fakeDocStore) UpdatePublication(_ context.Context, tenantID, docID, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := f.key(tenantID, docID)
	doc, ok := f.docs[key]
	if !ok || (status == "published" && (doc.Status != docstore.StatusCompleted || doc.DocStatus != docstore.DocStatusActive)) {
		return docstore.ErrNotFound
	}
	doc.PublicationStatus = status
	f.docs[key] = doc
	return nil
}
