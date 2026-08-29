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
