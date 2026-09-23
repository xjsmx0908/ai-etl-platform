package publicationrelease

import (
	"context"
	"reflect"
	"testing"

	"ai-etl-pipeline/internal/indexmanifest"

	pgxmock "github.com/pashagolub/pgxmock/v5"
)

func TestPostgresStoreResolvesOnlyHealthyPublishedReleaseInOneBatch(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT r.document_id,r.published_version_id,r.published_generation_id").
		WithArgs("acme", []string{"doc-replacement", "doc-draft", "doc-unresolved", "doc-unmanaged"}).
		WillReturnRows(pgxmock.NewRows([]string{"document_id", "published_version_id", "published_generation_id"}).
			AddRow("doc-replacement", "job-old", "gen-old").
			AddRow("doc-draft", nil, nil).
			AddRow("doc-unresolved", nil, nil))
	mock.ExpectQuery("SELECT doc_id FROM documents").
		WithArgs("acme", []string{"doc-draft", "doc-unresolved", "doc-unmanaged"}).
		WillReturnRows(pgxmock.NewRows([]string{"doc_id"}))

	refs := []indexmanifest.GenerationReference{
		{DocumentID: "doc-replacement", DocumentVersionID: "job-old", GenerationID: "gen-old"},
		{DocumentID: "doc-replacement", DocumentVersionID: "job-new", GenerationID: "gen-new"},
		{DocumentID: "doc-draft", DocumentVersionID: "job-draft", GenerationID: "gen-draft"},
		{DocumentID: "doc-unresolved", DocumentVersionID: "job-legacy", GenerationID: "gen-legacy"},
		{DocumentID: "doc-unmanaged"},
		{DocumentID: "doc-replacement", GenerationID: "gen-old"},
		{},
	}
	got, err := NewPostgresStore(mock).ResolveVisibility(context.Background(), "acme", refs)
	if err != nil {
		t.Fatal(err)
	}
	want := []bool{true, false, false, false, false, false, false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visibility=%v, want %v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresStoreDocumentContentKeepsChunksOfNeverPublishedDocuments pins the
// one place where the document-detail policy differs from the retrieval policy.
//
// Reusing the retrieval policy on GET /v1/documents/{docID}/chunks made every
// document that had not been through the release centre render an empty chunk
// list on its own detail page — the page said "暂无切块" while the chunks were
// sitting in the index. The fixture below is the same one the retrieval test
// uses, so the two expectations can be read side by side: same rows, same
// queries, one deliberate difference.
func TestPostgresStoreDocumentContentKeepsChunksOfNeverPublishedDocuments(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT r.document_id,r.published_version_id,r.published_generation_id").
		WithArgs("acme", []string{"doc-replacement", "doc-draft", "doc-unresolved", "doc-unmanaged"}).
		WillReturnRows(pgxmock.NewRows([]string{"document_id", "published_version_id", "published_generation_id"}).
			AddRow("doc-replacement", "job-old", "gen-old").
			AddRow("doc-draft", nil, nil).
			AddRow("doc-unresolved", nil, nil))
	mock.ExpectQuery("SELECT doc_id FROM documents").
		WithArgs("acme", []string{"doc-draft", "doc-unresolved", "doc-unmanaged"}).
		WillReturnRows(pgxmock.NewRows([]string{"doc_id"}).AddRow("doc-unmanaged"))

	refs := []indexmanifest.GenerationReference{
		{DocumentID: "doc-replacement", DocumentVersionID: "job-old", GenerationID: "gen-old"},
		{DocumentID: "doc-replacement", DocumentVersionID: "job-new", GenerationID: "gen-new"},
		{DocumentID: "doc-draft", DocumentVersionID: "job-draft", GenerationID: "gen-draft"},
		{DocumentID: "doc-unresolved", DocumentVersionID: "job-legacy", GenerationID: "gen-legacy"},
		{DocumentID: "doc-unmanaged"},
		{DocumentID: "doc-replacement", GenerationID: "gen-old"},
		{},
	}
	got, err := NewPostgresStore(mock).ResolveDocumentContentVisibility(context.Background(), "acme", refs)
	if err != nil {
		t.Fatal(err)
	}
	// Only the superseded replacement generation stays hidden. Everything else is
	// this document's current content.
	want := []bool{true, false, true, true, true, false, false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("content visibility=%v, want %v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreAllowsPublishedDocumentsWithoutGenerationIdentity(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT r.document_id,r.published_version_id,r.published_generation_id").
		WithArgs("acme", []string{"doc-legacy"}).
		WillReturnRows(pgxmock.NewRows([]string{"document_id", "published_version_id", "published_generation_id"}))
	mock.ExpectQuery("SELECT doc_id FROM documents").
		WithArgs("acme", []string{"doc-legacy"}).
		WillReturnRows(pgxmock.NewRows([]string{"doc_id"}).AddRow("doc-legacy"))

	refs := []indexmanifest.GenerationReference{
		{DocumentID: "doc-legacy"},
		{DocumentID: "doc-legacy", DocumentVersionID: "job-legacy", GenerationID: "gen-legacy"},
	}
	got, err := NewPostgresStore(mock).ResolveVisibility(context.Background(), "acme", refs)
	if err != nil {
		t.Fatal(err)
	}
	want := []bool{true, false}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visibility=%v, want %v", got, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
