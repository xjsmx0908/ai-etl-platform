package docstore

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v5"
)

// docCols matches the leading part of the production column list, which is
// defined once in documentColumns.
const docCols = "tenant_id, doc_id, file_name, object_key, file_hash, file_size"

func tPtr(t time.Time) *time.Time { return &t }

func docRowColumns() []string {
	return []string{"tenant_id", "doc_id", "file_name", "object_key", "file_hash", "file_size",
		"content_type", "permission", "status", "stage", "chunks_done", "chunks_total", "error",
		"metadata", "uploaded_by", "created_at", "updated_at", "completed_at",
		"doc_status", "effective_date", "supersedes", "owner"}
}

func docRow() *pgxmock.Rows {
	return pgxmock.NewRows(docRowColumns()).
		AddRow("acme", "doc-1", "a.pdf", "acme/doc-1.pdf", "abc123", int64(1024), "application/pdf",
			"internal", "completed", "completed", 4, 4, "", map[string]string{"k": "v"},
			"u-1", time.Now(), time.Now(), tPtr(time.Now()),
			"active", tPtr(time.Now()), "", "owner-hr")
}

func TestUpsert(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	args := make([]any, 22)
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
			"u-1", time.Now(), time.Now(), tPtr(time.Now()),
			"superseded", tPtr(time.Now()), "doc-1", ""))
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

func TestGetByHash(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT "+docCols).
		WithArgs("acme", "abc123").
		WillReturnRows(docRow())

	s := New(mock)
	d, found, err := s.GetByHash(context.Background(), "acme", "abc123")
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if !found || d.DocID != "doc-1" {
		t.Fatalf("expected doc-1, got found=%v doc=%+v", found, d)
	}
}

// An empty hash must not query at all: every legacy row has file_hash=” and
// would otherwise match, making unrelated uploads look like duplicates.
func TestGetByHash_EmptyHashSkipsQuery(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()

	s := New(mock)
	_, found, err := s.GetByHash(context.Background(), "acme", "  ")
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if found {
		t.Fatal("empty hash must never report a duplicate")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestGovernanceByDocIDs(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	effective := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT doc_id, doc_status").
		WithArgs("acme", []string{"doc-1", "doc-2"}).
		WillReturnRows(pgxmock.NewRows([]string{"doc_id", "doc_status", "effective_date", "supersedes", "file_name"}).
			AddRow("doc-1", "active", (*time.Time)(nil), "", "a.pdf").
			AddRow("doc-2", "superseded", tPtr(effective), "doc-1", "a.pdf"))

	s := New(mock)
	got, err := s.GovernanceByDocIDs(context.Background(), "acme", []string{"doc-1", "doc-2"})
	if err != nil {
		t.Fatalf("GovernanceByDocIDs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %+v", got)
	}
	if got["doc-1"].IsRetired() {
		t.Error("active document must not be reported retired")
	}
	if !got["doc-2"].IsRetired() {
		t.Error("superseded document must be reported retired")
	}
	if !got["doc-2"].EffectiveDate.Equal(effective) {
		t.Errorf("expected effective date %v, got %v", effective, got["doc-2"].EffectiveDate)
	}
	if got["doc-1"].EffectiveDate != (time.Time{}) {
		t.Errorf("NULL effective_date must scan to zero time, got %v", got["doc-1"].EffectiveDate)
	}
}

// No doc_ids means no query — a query with an empty ANY() array is pure overhead
// on the retrieval hot path.
func TestGovernanceByDocIDs_EmptyInput(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()

	s := New(mock)
	got, err := s.GovernanceByDocIDs(context.Background(), "acme", nil)
	if err != nil {
		t.Fatalf("GovernanceByDocIDs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty map, got %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// A document row predating migration 0004 scans doc_status='active' (the column
// default), so IsRetired must be false and retrieval unchanged.
func TestGovernance_UnsetStatusIsActive(t *testing.T) {
	if (Governance{DocID: "doc-1"}).IsRetired() {
		t.Fatal("empty doc_status must be treated as active")
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
