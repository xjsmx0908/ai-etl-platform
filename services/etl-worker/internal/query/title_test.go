package query

import (
	"context"
	"testing"

	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/docstore"
)

func TestTitleMatchedDocIDsFindsPublishedFileNameInQuestion(t *testing.T) {
	docs := []docstore.Document{
		{DocID: "doc-gang", FileName: "赴港流程 .docx", PublicationStatus: "published"},
		{DocID: "doc-other", FileName: "会议室预订指南.pdf", PublicationStatus: "published"},
		{DocID: "doc-draft", FileName: "赴港流程草稿.docx", PublicationStatus: "draft"},
	}

	got := titleMatchedDocIDs("赴港流程是什么？", docs)
	if len(got) != 1 || got[0] != "doc-gang" {
		t.Fatalf("expected published 赴港流程 doc, got %v", got)
	}
}

func TestTitleMatchedDocIDsIgnoresGenericQuestionWithoutFileName(t *testing.T) {
	docs := []docstore.Document{
		{DocID: "doc-gang", FileName: "赴港流程 .docx", PublicationStatus: "published"},
	}
	if got := titleMatchedDocIDs("今天的制度是什么？", docs); len(got) != 0 {
		t.Fatalf("expected no title match, got %v", got)
	}
}

type fakeDocuments struct {
	docs []docstore.Document
	q    docstore.ListQuery
}

func (f *fakeDocuments) List(_ context.Context, q docstore.ListQuery) ([]docstore.Document, int, error) {
	f.q = q
	return f.docs, len(f.docs), nil
}

func TestServiceTitleMatchDocIDsUsesPublishedFileNames(t *testing.T) {
	fake := &fakeDocuments{docs: []docstore.Document{
		{DocID: "doc-gang", FileName: "赴港流程 .docx", PublicationStatus: "published"},
	}}
	svc := NewService(config.Config{})
	svc.documents = fake
	got := svc.titleMatchDocIDs(context.Background(), "acme", "user-uploads", "赴港流程是什么？", []string{"public"})
	if len(got) != 1 || got[0] != "doc-gang" {
		t.Fatalf("expected published filename match, got %v", got)
	}
	if fake.q.TenantID != "acme" || len(fake.q.KnowledgeSpaceIDs) != 1 || fake.q.KnowledgeSpaceIDs[0] != "user-uploads" {
		t.Fatalf("unexpected list query: %+v", fake.q)
	}
}
