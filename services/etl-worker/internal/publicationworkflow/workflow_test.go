package publicationworkflow

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"ai-etl-pipeline/internal/docstore"
)

type documentReaderStub struct {
	document docstore.Document
	found    bool
	err      error
}

func TestAssessFailsClosedWithDeterministicBlockers(t *testing.T) {
	doc := docstore.Document{
		TenantID:          "acme",
		DocID:             "doc-2",
		Status:            docstore.StatusProcessing,
		DocStatus:         docstore.DocStatusSuperseded,
		KnowledgeSpaceID:  "user-uploads",
		PublicationStatus: "published",
	}
	workflow := New(documentReaderStub{document: doc, found: true}, indexInspectorStub{
		counts: IndexCounts{Vector: 0, Text: 2},
	})

	assessment, err := workflow.Assess(context.Background(), Actor{TenantID: "acme", UserID: "user-1"}, "doc-2")
	if err != nil {
		t.Fatalf("Assess: %v", err)
	}
	want := []string{
		"managed_space_required",
		"ingestion_not_completed",
		"document_not_active",
		"document_not_draft",
		"owner_required",
		"effective_date_required",
		"vector_index_missing",
		"index_count_mismatch",
	}
	if assessment.Ready || !reflect.DeepEqual(assessment.Blockers, want) {
		t.Fatalf("blockers = %#v, want %#v", assessment.Blockers, want)
	}
}

func (s documentReaderStub) Get(context.Context, string, string) (docstore.Document, bool, error) {
	return s.document, s.found, s.err
}

type indexInspectorStub struct {
	counts IndexCounts
	err    error
}

type publisherStub struct {
	calls int
}

func (s *publisherStub) Publish(context.Context, Actor, string, string) error {
	s.calls++
	return nil
}

func (s indexInspectorStub) CountDocumentChunks(context.Context, string, string) (IndexCounts, error) {
	return s.counts, s.err
}

func TestAssessReturnsReadyForCompleteManagedDraft(t *testing.T) {
	doc := docstore.Document{
		TenantID:          "acme",
		DocID:             "doc-1",
		Status:            docstore.StatusCompleted,
		DocStatus:         docstore.DocStatusActive,
		KnowledgeSpaceID:  "policies",
		PublicationStatus: "draft",
		Owner:             "hr",
		EffectiveDate:     time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}
	workflow := New(documentReaderStub{document: doc, found: true}, indexInspectorStub{
		counts: IndexCounts{Vector: 3, Text: 3},
	})

	assessment, err := workflow.Assess(context.Background(), Actor{
		TenantID: "acme", UserID: "user-1", Role: "user",
	}, "doc-1")
	if err != nil {
		t.Fatalf("Assess: %v", err)
	}
	if !assessment.Ready || len(assessment.Blockers) != 0 {
		t.Fatalf("expected ready assessment, got %+v", assessment)
	}
	if assessment.DocumentID != "doc-1" || assessment.KnowledgeSpaceID != "policies" || assessment.IndexCounts.Vector != 3 {
		t.Fatalf("unexpected assessment: %+v", assessment)
	}
}

func TestPublishApprovedRequiresAdministratorAndReadyAssessment(t *testing.T) {
	doc := docstore.Document{
		TenantID: "acme", DocID: "doc-3", Status: docstore.StatusCompleted,
		DocStatus: docstore.DocStatusActive, KnowledgeSpaceID: "policies",
		PublicationStatus: "draft", Owner: "legal",
		EffectiveDate: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}
	publisher := &publisherStub{}
	workflow := New(documentReaderStub{document: doc, found: true}, indexInspectorStub{
		counts: IndexCounts{Vector: 4, Text: 4},
	}).WithPublisher(publisher)

	if _, err := workflow.PublishApproved(context.Background(), Actor{
		TenantID: "acme", UserID: "author", Role: "user",
	}, "doc-3", "agent:run-1:2:publish_document"); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("expected ErrAdminRequired, got %v", err)
	}
	result, err := workflow.PublishApproved(context.Background(), Actor{
		TenantID: "acme", UserID: "reviewer", Role: "admin",
	}, "doc-3", "agent:run-1:2:publish_document")
	if err != nil {
		t.Fatalf("PublishApproved: %v", err)
	}
	if publisher.calls != 1 || result.PublicationStatus != "published" {
		t.Fatalf("unexpected publish result=%+v calls=%d", result, publisher.calls)
	}
}
