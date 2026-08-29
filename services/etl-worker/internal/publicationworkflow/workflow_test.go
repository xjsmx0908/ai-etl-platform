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
		PublicationStatus: "retired",
	}
	workflow := New(documentReaderStub{document: doc, found: true}, candidateReaderStub{})

	assessment, err := workflow.Assess(context.Background(), Actor{TenantID: "acme", UserID: "user-1"}, "doc-2")
	if err != nil {
		t.Fatalf("Assess: %v", err)
	}
	want := []string{
		"managed_space_required",
		"ingestion_not_completed",
		"document_not_active",
		"document_not_publishable",
		"owner_required",
		"effective_date_required",
		"exact_candidate_unavailable",
	}
	if assessment.Ready || !reflect.DeepEqual(assessment.Blockers, want) {
		t.Fatalf("blockers = %#v, want %#v", assessment.Blockers, want)
	}
}

func (s documentReaderStub) Get(context.Context, string, string) (docstore.Document, bool, error) {
	return s.document, s.found, s.err
}

type publisherStub struct {
	calls     int
	candidate Candidate
}

func (s *publisherStub) Publish(_ context.Context, _ Actor, candidate Candidate, _ string) error {
	s.calls++
	s.candidate = candidate
	return nil
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
	want := Candidate{DocumentID: "doc-1", DocumentVersionID: "job-1", GenerationID: "gen-1", ExpectedChunkCount: 3, ExpectedChunkDigest: "sha256:ready", ReleaseRevision: 1}
	workflow := New(documentReaderStub{document: doc, found: true}, candidateReaderStub{candidate: want, found: true})

	assessment, err := workflow.Assess(context.Background(), Actor{
		TenantID: "acme", UserID: "user-1", Role: "user",
	}, "doc-1")
	if err != nil {
		t.Fatalf("Assess: %v", err)
	}
	if !assessment.Ready || len(assessment.Blockers) != 0 {
		t.Fatalf("expected ready assessment, got %+v", assessment)
	}
	if assessment.DocumentID != "doc-1" || assessment.KnowledgeSpaceID != "policies" || !reflect.DeepEqual(assessment.Candidate, &want) {
		t.Fatalf("unexpected assessment: %+v", assessment)
	}
}

func TestAssessBindsReadyDecisionToExactActiveGeneration(t *testing.T) {
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
	want := Candidate{
		DocumentID:          "doc-1",
		DocumentVersionID:   "job-2",
		GenerationID:        "gen-2",
		ExpectedChunkCount:  4,
		ExpectedChunkDigest: "sha256:approved",
		ReleaseRevision:     7,
	}
	workflow := New(documentReaderStub{document: doc, found: true}, candidateReaderStub{
		candidate: want, found: true,
	})

	assessment, err := workflow.Assess(context.Background(), Actor{
		TenantID: "acme", UserID: "user-1", Role: "user",
	}, "doc-1")
	if err != nil {
		t.Fatalf("Assess: %v", err)
	}
	if !assessment.Ready || !reflect.DeepEqual(assessment.Candidate, &want) {
		t.Fatalf("assessment = %+v, want exact candidate %+v", assessment, want)
	}
}

func TestAssessAllowsPublishedCatalogStateForPendingReplacement(t *testing.T) {
	doc := docstore.Document{
		TenantID: "acme", DocID: "doc-1", Status: docstore.StatusCompleted,
		DocStatus: docstore.DocStatusActive, KnowledgeSpaceID: "policies",
		PublicationStatus: "published", Owner: "hr",
		EffectiveDate: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}
	candidate := Candidate{
		DocumentID: "doc-1", DocumentVersionID: "job-2", GenerationID: "gen-2",
		ExpectedChunkCount: 4, ExpectedChunkDigest: "sha256:replacement", ReleaseRevision: 7,
	}
	assessment, err := New(documentReaderStub{document: doc, found: true}, candidateReaderStub{
		candidate: candidate, found: true,
	}).Assess(context.Background(), Actor{TenantID: "acme", UserID: "reviewer"}, "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if !assessment.Ready || !reflect.DeepEqual(assessment.Candidate, &candidate) {
		t.Fatalf("replacement assessment=%+v, want ready exact candidate", assessment)
	}
}

type candidateReaderStub struct {
	candidate Candidate
	found     bool
	err       error
}

func (s candidateReaderStub) CurrentCandidate(context.Context, string, string) (Candidate, bool, error) {
	return s.candidate, s.found, s.err
}

func TestPublishApprovedRequiresAdministratorAndReadyAssessment(t *testing.T) {
	doc := docstore.Document{
		TenantID: "acme", DocID: "doc-3", Status: docstore.StatusCompleted,
		DocStatus: docstore.DocStatusActive, KnowledgeSpaceID: "policies",
		PublicationStatus: "draft", Owner: "legal",
		EffectiveDate: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}
	publisher := &publisherStub{}
	candidate := Candidate{DocumentID: "doc-3", DocumentVersionID: "job-3", GenerationID: "gen-3", ExpectedChunkCount: 4, ExpectedChunkDigest: "sha256:ready", ReleaseRevision: 2}
	workflow := New(documentReaderStub{document: doc, found: true}, candidateReaderStub{candidate: candidate, found: true}).WithPublisher(publisher)

	if _, err := workflow.PublishApproved(context.Background(), Actor{
		TenantID: "acme", UserID: "author", Role: "user",
	}, candidate, "agent:run-1:2:publish_document"); !errors.Is(err, ErrAdminRequired) {
		t.Fatalf("expected ErrAdminRequired, got %v", err)
	}
	result, err := workflow.PublishApproved(context.Background(), Actor{
		TenantID: "acme", UserID: "reviewer", Role: "admin",
	}, candidate, "agent:run-1:2:publish_document")
	if err != nil {
		t.Fatalf("PublishApproved: %v", err)
	}
	if publisher.calls != 1 || publisher.candidate != candidate || result.PublicationStatus != "published" {
		t.Fatalf("unexpected publish result=%+v calls=%d", result, publisher.calls)
	}
}
