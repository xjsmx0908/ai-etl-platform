package ingestion

import (
	"context"
	"errors"
	"testing"

	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/model"

	"github.com/pashagolub/pgxmock/v5"
)

func TestPostgresStoreAdmitCommitsDocumentJobAndEventTogether(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	expectAdmissionWrites(mock)
	mock.ExpectCommit()

	receipt, err := NewPostgresStore(mock).Admit(context.Background(), testSubmission())
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if receipt.JobID != "job-1" || receipt.EventID != "event-1" || receipt.DocID != "doc-1" {
		t.Fatalf("receipt = %+v", receipt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreAdmitRollsBackWhenOutboxWriteFails(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("tenant-a/doc-1").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT job_id,event_id,tenant_id,doc_id,request_signature FROM ingestion_jobs").
		WithArgs("job-1").WillReturnRows(pgxmock.NewRows([]string{"job_id", "event_id", "tenant_id", "doc_id", "request_signature"}))
	mock.ExpectExec("INSERT INTO documents").WithArgs(anyArgs(24)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO ingestion_jobs").
		WithArgs("job-1", "event-1", "tenant-a", "doc-1", "sha256:request-1", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO ingestion_outbox").
		WithArgs("event-1", "job-1", "tenant-a", "doc-1", pgxmock.AnyArg()).
		WillReturnError(errors.New("disk full"))
	mock.ExpectRollback()

	if _, err := NewPostgresStore(mock).Admit(context.Background(), testSubmission()); err == nil {
		t.Fatal("expected admission failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreAdmitReturnsCommittedReceiptOnRetry(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("tenant-a/doc-1").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT job_id,event_id,tenant_id,doc_id,request_signature FROM ingestion_jobs").
		WithArgs("job-1").WillReturnRows(pgxmock.NewRows([]string{
		"job_id", "event_id", "tenant_id", "doc_id", "request_signature",
	}).AddRow("job-1", "event-1", "tenant-a", "doc-1", "sha256:request-1"))
	mock.ExpectRollback()

	receipt, err := NewPostgresStore(mock).Admit(context.Background(), testSubmission())
	if err != nil {
		t.Fatalf("retry admission: %v", err)
	}
	if receipt != (Receipt{JobID: "job-1", EventID: "event-1", TenantID: "tenant-a", DocID: "doc-1"}) {
		t.Fatalf("retry receipt = %+v", receipt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectAdmissionWrites(mock pgxmock.PgxPoolIface) {
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").
		WithArgs("tenant-a/doc-1").WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectQuery("SELECT job_id,event_id,tenant_id,doc_id,request_signature FROM ingestion_jobs").
		WithArgs("job-1").WillReturnRows(pgxmock.NewRows([]string{"job_id", "event_id", "tenant_id", "doc_id", "request_signature"}))
	mock.ExpectExec("INSERT INTO documents").WithArgs(anyArgs(24)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO ingestion_jobs").
		WithArgs("job-1", "event-1", "tenant-a", "doc-1", "sha256:request-1", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO ingestion_outbox").
		WithArgs("event-1", "job-1", "tenant-a", "doc-1", pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
}

func anyArgs(count int) []any {
	args := make([]any, count)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

func testSubmission() Submission {
	return Submission{
		JobID: "job-1", EventID: "event-1",
		RequestSignature: "sha256:request-1",
		Document:         docstore.Document{TenantID: "tenant-a", DocID: "doc-1", Status: docstore.StatusQueued},
		Task:             model.Task{JobID: "job-1", EventID: "event-1", TenantID: "tenant-a", DocID: "doc-1"},
	}
}
