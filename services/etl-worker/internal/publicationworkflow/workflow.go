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

// Candidate is the immutable version/generation identity reviewed by an
// administrator. Tenant identity is supplied by the authenticated actor and
// is deliberately not accepted from tool arguments.
type Candidate struct {
	DocumentID          string `json:"document_id"`
	DocumentVersionID   string `json:"document_version_id"`
	GenerationID        string `json:"generation_id"`
	ExpectedChunkCount  int    `json:"expected_chunk_count"`
	ExpectedChunkDigest string `json:"expected_chunk_digest"`
	ReleaseRevision     int64  `json:"release_revision"`
}

type Assessment struct {
	DocumentID       string     `json:"document_id"`
	KnowledgeSpaceID string     `json:"knowledge_space_id"`
	Ready            bool       `json:"ready"`
	Blockers         []string   `json:"blockers"`
	Candidate        *Candidate `json:"candidate,omitempty"`
}

type PublicationResult struct {
	DocumentID        string `json:"document_id"`
	PublicationStatus string `json:"publication_status"`
}

type DocumentReader interface {
	Get(ctx context.Context, tenantID, docID string) (docstore.Document, bool, error)
}

type CandidateReader interface {
	CurrentCandidate(ctx context.Context, tenantID, docID string) (Candidate, bool, error)
}

type Publisher interface {
	Publish(ctx context.Context, actor Actor, candidate Candidate, idempotencyKey string) error
}

type Workflow struct {
	documents  DocumentReader
	candidates CandidateReader
	publisher  Publisher
}

func (w *Workflow) WithPublisher(publisher Publisher) *Workflow {
	w.publisher = publisher
	return w
}

func New(documents DocumentReader, candidates CandidateReader) *Workflow {
	return &Workflow{documents: documents, candidates: candidates}
}

func (w *Workflow) PublishApproved(ctx context.Context, actor Actor, candidate Candidate, idempotencyKey string) (PublicationResult, error) {
	if !strings.EqualFold(strings.TrimSpace(actor.Role), "admin") {
		return PublicationResult{}, ErrAdminRequired
	}
	if w == nil || w.publisher == nil {
		return PublicationResult{}, fmt.Errorf("publication publisher is not configured")
	}
	if !validCandidate(candidate) {
		return PublicationResult{}, fmt.Errorf("%w: invalid exact candidate", ErrNotReady)
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return PublicationResult{}, fmt.Errorf("idempotency key is required")
	}
	if err := w.publisher.Publish(ctx, actor, candidate, idempotencyKey); err != nil {
		return PublicationResult{}, err
	}
	return PublicationResult{DocumentID: candidate.DocumentID, PublicationStatus: "published"}, nil
}

func (w *Workflow) Assess(ctx context.Context, actor Actor, docID string) (Assessment, error) {
	if w == nil || w.documents == nil || w.candidates == nil {
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
	candidate, candidateFound, err := w.candidates.CurrentCandidate(ctx, tenantID, docID)
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
	if doc.PublicationStatus != "draft" && doc.PublicationStatus != "published" {
		blockers = append(blockers, "document_not_publishable")
	}
	if strings.TrimSpace(doc.Owner) == "" {
		blockers = append(blockers, "owner_required")
	}
	if doc.EffectiveDate.IsZero() {
		blockers = append(blockers, "effective_date_required")
	}
	if !candidateFound || !validCandidate(candidate) || candidate.DocumentID != doc.DocID {
		blockers = append(blockers, "exact_candidate_unavailable")
	}
	var readyCandidate *Candidate
	if len(blockers) == 0 {
		readyCandidate = &candidate
	}
	return Assessment{
		DocumentID:       doc.DocID,
		KnowledgeSpaceID: doc.KnowledgeSpaceID,
		Ready:            len(blockers) == 0,
		Blockers:         blockers,
		Candidate:        readyCandidate,
	}, nil
}

func validCandidate(candidate Candidate) bool {
	return strings.TrimSpace(candidate.DocumentID) != "" &&
		strings.TrimSpace(candidate.DocumentVersionID) != "" &&
		strings.TrimSpace(candidate.GenerationID) != "" &&
		candidate.ExpectedChunkCount > 0 &&
		strings.TrimSpace(candidate.ExpectedChunkDigest) != "" &&
		candidate.ReleaseRevision > 0
}
