// Package publicationworkflow owns the governed document publication rules
// shared by HTTP handlers and Agent tools.
package publicationworkflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ai-etl-pipeline/internal/docstore"
)

var (
	ErrNotFound      = errors.New("publication workflow: document not found")
	ErrAdminRequired = errors.New("publication workflow: administrator required")
	ErrNotReady      = errors.New("publication workflow: document is not ready")
)

type Actor struct {
	TenantID string
	UserID   string
	Role     string
}

type IndexCounts struct {
	Vector int `json:"vector"`
	Text   int `json:"text"`
}

type Assessment struct {
	DocumentID       string      `json:"document_id"`
	KnowledgeSpaceID string      `json:"knowledge_space_id"`
	Ready            bool        `json:"ready"`
	Blockers         []string    `json:"blockers"`
	IndexCounts      IndexCounts `json:"index_counts"`
}

type PublicationResult struct {
	DocumentID        string `json:"document_id"`
	PublicationStatus string `json:"publication_status"`
}

type DocumentReader interface {
	Get(ctx context.Context, tenantID, docID string) (docstore.Document, bool, error)
}

type IndexInspector interface {
	CountDocumentChunks(ctx context.Context, tenantID, docID string) (IndexCounts, error)
}

type Publisher interface {
	Publish(ctx context.Context, actor Actor, docID, idempotencyKey string) error
}

type Workflow struct {
	documents DocumentReader
	indexes   IndexInspector
	publisher Publisher
}

func (w *Workflow) WithPublisher(publisher Publisher) *Workflow {
	w.publisher = publisher
	return w
}

func New(documents DocumentReader, indexes IndexInspector) *Workflow {
	return &Workflow{documents: documents, indexes: indexes}
}

func (w *Workflow) PublishApproved(ctx context.Context, actor Actor, docID, idempotencyKey string) (PublicationResult, error) {
	if !strings.EqualFold(strings.TrimSpace(actor.Role), "admin") {
		return PublicationResult{}, ErrAdminRequired
	}
	if w == nil || w.publisher == nil {
		return PublicationResult{}, fmt.Errorf("publication publisher is not configured")
	}
	assessment, err := w.Assess(ctx, actor, docID)
	if err != nil {
		return PublicationResult{}, err
	}
	if !assessment.Ready {
		return PublicationResult{}, fmt.Errorf("%w: %s", ErrNotReady, strings.Join(assessment.Blockers, ","))
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return PublicationResult{}, fmt.Errorf("idempotency key is required")
	}
	if err := w.publisher.Publish(ctx, actor, assessment.DocumentID, idempotencyKey); err != nil {
		return PublicationResult{}, err
	}
	return PublicationResult{DocumentID: assessment.DocumentID, PublicationStatus: "published"}, nil
}

func (w *Workflow) Assess(ctx context.Context, actor Actor, docID string) (Assessment, error) {
	if w == nil || w.documents == nil || w.indexes == nil {
		return Assessment{}, fmt.Errorf("publication workflow is not configured")
	}
	tenantID := strings.TrimSpace(actor.TenantID)
	docID = strings.TrimSpace(docID)
	if tenantID == "" || docID == "" {
		return Assessment{}, ErrNotFound
	}
	doc, found, err := w.documents.Get(ctx, tenantID, docID)
	if err != nil {
		return Assessment{}, err
	}
	if !found || doc.TenantID != tenantID {
		return Assessment{}, ErrNotFound
	}
	counts, err := w.indexes.CountDocumentChunks(ctx, tenantID, docID)
	if err != nil {
		return Assessment{}, err
	}
	blockers := make([]string, 0, 8)
	if doc.KnowledgeSpaceID == "user-uploads" || strings.TrimSpace(doc.KnowledgeSpaceID) == "" {
		blockers = append(blockers, "managed_space_required")
	}
	if doc.Status != docstore.StatusCompleted {
		blockers = append(blockers, "ingestion_not_completed")
	}
	if doc.DocStatus != "" && doc.DocStatus != docstore.DocStatusActive {
		blockers = append(blockers, "document_not_active")
	}
	if doc.PublicationStatus != "draft" {
		blockers = append(blockers, "document_not_draft")
	}
	if strings.TrimSpace(doc.Owner) == "" {
		blockers = append(blockers, "owner_required")
	}
	if doc.EffectiveDate.IsZero() {
		blockers = append(blockers, "effective_date_required")
	}
	if counts.Vector <= 0 {
		blockers = append(blockers, "vector_index_missing")
	}
	if counts.Text <= 0 {
		blockers = append(blockers, "text_index_missing")
	}
	if counts.Vector != counts.Text {
		blockers = append(blockers, "index_count_mismatch")
	}
	return Assessment{
		DocumentID:       doc.DocID,
		KnowledgeSpaceID: doc.KnowledgeSpaceID,
		Ready:            len(blockers) == 0,
		Blockers:         blockers,
		IndexCounts:      counts,
	}, nil
}
