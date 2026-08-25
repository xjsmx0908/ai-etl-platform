package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"ai-etl-pipeline/internal/audit"
	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/publicationworkflow"
)

type publicationDocumentStore interface {
	Get(ctx context.Context, tenantID, docID string) (docstore.Document, bool, error)
	UpdatePublication(ctx context.Context, tenantID, docID, status string) error
}

type publicationCacheInvalidator interface {
	InvalidateSemanticCache(context.Context) error
}

type documentPublisher struct {
	documents publicationDocumentStore
	cache     publicationCacheInvalidator
	audits    audit.Store
}

func newDocumentPublisher(documents publicationDocumentStore, cache publicationCacheInvalidator, audits audit.Store) *documentPublisher {
	return &documentPublisher{documents: documents, cache: cache, audits: audits}
}

func (p *documentPublisher) Publish(ctx context.Context, actor publicationworkflow.Actor, docID, idempotencyKey string) error {
	if !strings.EqualFold(strings.TrimSpace(actor.Role), "admin") {
		return publicationworkflow.ErrAdminRequired
	}
	if p == nil || p.documents == nil {
		return fmt.Errorf("publication document store is not configured")
	}
	tenantID := strings.TrimSpace(actor.TenantID)
	docID = strings.TrimSpace(docID)
	if tenantID == "" || docID == "" {
		return publicationworkflow.ErrNotFound
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return fmt.Errorf("idempotency key is required")
	}
	doc, found, err := p.documents.Get(ctx, tenantID, docID)
	if err != nil {
		return err
	}
	if !found || doc.TenantID != tenantID {
		return publicationworkflow.ErrNotFound
	}
	if doc.KnowledgeSpaceID == "" || doc.KnowledgeSpaceID == "user-uploads" {
		return publicationworkflow.ErrNotReady
	}
	if doc.PublicationStatus == "published" {
		return nil
	}
	if err := p.documents.UpdatePublication(ctx, tenantID, docID, "published"); err != nil {
		return err
	}
	if p.cache != nil {
		if err := p.cache.InvalidateSemanticCache(ctx); err != nil {
			slog.Warn("semantic cache flush failed after governed publication", "doc_id", docID, "error", err)
		}
	}
	recordAudit(ctx, p.audits, audit.Entry{
		TenantID: tenantID, ActorUserID: actor.UserID, ActorRole: actor.Role,
		Action: "document.publication.update", ResourceType: "document", ResourceID: docID,
		Result: audit.ResultSuccess, Detail: map[string]any{
			"publication_status": "published",
			"idempotency_key":    idempotencyKey,
			"source":             "document_publication_agent",
		},
	})
	return nil
}

var _ publicationworkflow.Publisher = (*documentPublisher)(nil)
