package docstore

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v5"
)

const docCols = "tenant_id, doc_id, file_name, object_key, file_hash, file_size, content_type, " +
	"permission, status, stage, chunks_done, chunks_total, error, metadata, uploaded_by, " +
	"created_at, updated_at, completed_at"

func tPtr(t time.Time) *time.Time { return &t }

func docRow() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"tenant_id", "doc_id", "file_name", "object_key", "file_hash", "file_size",
		"content_type", "permission", "status", "stage", "chunks_done", "chunks_total", "error",
		"metadata", "uploaded_by", "created_at", "updated_at", "completed_at"}).
		AddRow("acme", "doc-1", "a.pdf", "acme/doc-1.pdf", "abc123", int64(1024), "application/pdf",
			"internal", "completed", "completed", 4, 4, "", map[string]string{"k": "v"},
			"u-1", time.Now(), time.Now(), tPtr(time.Now()))
}

func TestUpsert(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	args := make([]any, 18)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	mock.ExpectExec("INSERT INTO documents").
		WithArgs(args...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	s := New(mock)
	err = s.Upsert(context.Background(), Document{TenantID: "acme", DocID: "doc-1", Status: StatusQueued})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}

func TestGet(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT "+docCols).
		WithArgs("acme", "doc-1").
		WillReturnRows(docRow())

	s := New(mock)
	d, found, err := s.Get(context.Background(), "acme", "doc-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found || d.DocID != "doc-1" || d.Permission != "internal" || d.Metadata["k"] != "v" {
		t.Fatalf("unexpected doc: found=%v doc=%+v", found, d)
	}
	if d.FileSize != 1024 || d.Status != StatusCompleted || d.CompletedAt.IsZero() {
		t.Fatalf("unexpected scan values: size=%d status=%s completed=%v", d.FileSize, d.Status, d.CompletedAt)
	}
}

func TestGet_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT "+docCols).
		WithArgs("acme", "missing").
		WillReturnRows(pgxmock.NewRows([]string{"tenant_id"}))

	s := New(mock)
	_, found, err := s.Get(context.Background(), "acme", "missing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Fatal("expected not found")
	}
}

func TestList_WithFilters(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT "+docCols).
		WithArgs("acme", "completed", []string{"internal"}, "%contract%", 20, 0).
		WillReturnRows(docRow().AddRow("acme", "doc-2", "b.docx", "acme/doc-2.docx", "def", int64(2048),
			"application/docx", "internal", "completed", "completed", 2, 2, "", map[string]string{},
			"u-1", time.Now(), time.Now(), tPtr(time.Now())))
	mock.ExpectQuery("SELECT count").WithArgs("acme", "completed", []string{"internal"}, "%contract%").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))

	s := New(mock)
	docs, total, err := s.List(context.Background(), ListQuery{
		TenantID: "acme", Status: "completed", Permissions: []string{"internal"}, Search: "contract", Limit: 20,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(docs) != 2 {
		t.Fatalf("expected 2/2, got %d/%d", len(docs), total)
	}
}

func TestDelete(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	mock.ExpectExec("DELETE FROM documents").WithArgs("acme", "doc-1").
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	s := New(mock)
	if err := s.Delete(context.Background(), "acme", "doc-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestUpsertStatus(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	mock.ExpectExec("UPDATE documents SET").
		WithArgs("acme", "doc-1", "processing", "embedding", 3, 5, "", "").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	s := New(mock)
	err = s.UpsertStatus(context.Background(), "acme", "doc-1", Document{
		Status: "processing", Stage: "embedding", ChunksDone: 3, ChunksTotal: 5,
	})
	if err != nil {
		t.Fatalf("UpsertStatus: %v", err)
	}
}
