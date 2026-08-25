package main

import (
	"context"
	"testing"
	"time"

	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/publicationworkflow"
)

type recordingCacheInvalidator struct {
	calls int
}

func (c *recordingCacheInvalidator) InvalidateSemanticCache(context.Context) error {
	c.calls++
	return nil
}

func TestDocumentPublisherPublishesOnceWithCacheAndAudit(t *testing.T) {
	docs := newFakeDocStore()
	if err := docs.Upsert(context.Background(), docstore.Document{
		TenantID: "tenant-a", DocID: "doc-1", FileName: "policy.docx",
		Status: docstore.StatusCompleted, DocStatus: docstore.DocStatusActive,
		KnowledgeSpaceID: "policies", PublicationStatus: "draft",
		Owner: "legal", EffectiveDate: time.Now(),
	}); err != nil {
		t.Fatalf("seed document: %v", err)
	}
	cache := &recordingCacheInvalidator{}
	audits := newFakeAuditStore()
	publisher := newDocumentPublisher(docs, cache, audits)
	actor := publicationworkflow.Actor{TenantID: "tenant-a", UserID: "admin-a", Role: "admin"}

	if err := publisher.Publish(context.Background(), actor, "doc-1", "run-1:publish"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := publisher.Publish(context.Background(), actor, "doc-1", "run-1:publish"); err != nil {
		t.Fatalf("replay publish: %v", err)
	}
	doc, _, _ := docs.Get(context.Background(), "tenant-a", "doc-1")
	if doc.PublicationStatus != "published" || cache.calls != 1 {
		t.Fatalf("document=%+v cache_calls=%d", doc, cache.calls)
	}
	if len(audits.entries) != 1 {
		t.Fatalf("expected one audit entry, got %+v", audits.entries)
	}
	entry := audits.entries[0]
	if entry.Action != "document.publication.update" || entry.ActorUserID != "admin-a" || entry.Detail["idempotency_key"] != "run-1:publish" {
		t.Fatalf("unexpected audit entry: %+v", entry)
	}
}
