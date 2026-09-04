package releasecenter

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v5"

	"ai-etl-pipeline/internal/publicationworkflow"
)

func TestReconcileStaleRequestsPreservesAuditRows(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectExec("WITH stale AS").WithArgs(25).WillReturnResult(pgxmock.NewResult("UPDATE", 3))
	count, err := NewPostgresStore(mock).ReconcileStaleRequests(context.Background(), 25)
	if err != nil || count != 3 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSaveReviewRejectsReusedIDWithAlteredRecord(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectExec("INSERT INTO release_center_reviews").WillReturnResult(pgxmock.NewResult("INSERT", 0))
	report := ReviewReport{
		ID: "review-1", TenantID: "tenant-a", DocumentID: "doc-1",
		DocumentVersionID: "job-1", GenerationID: "gen-1", ReleaseRevision: 1,
		Status: "completed", Recommendation: "publish", RiskLevel: RiskLow,
		CreatedAt: time.Now().UTC(),
	}
	if err := NewPostgresStore(mock).SaveReview(context.Background(), report); err == nil {
		t.Fatal("expected altered review id replay to fail")
	}
}

func TestSaveRequestRejectsReusedIDWithAlteredCandidate(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectExec("INSERT INTO release_center_requests").WillReturnResult(pgxmock.NewResult("INSERT", 0))
	request := ReleaseRequest{
		ID: "request-1", TenantID: "tenant-a", DocumentID: "doc-1", ReviewID: "review-1",
		Candidate: publicationworkflow.Candidate{
			DocumentID: "doc-1", DocumentVersionID: "job-1", GenerationID: "gen-1",
			ExpectedChunkCount: 3, ExpectedChunkDigest: "sha256:ready", ReleaseRevision: 1,
		},
		RequiredApprovals: 1, State: RequestApprovalPending, RequestedBy: "admin-1",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := NewPostgresStore(mock).SaveRequest(context.Background(), request); err == nil {
		t.Fatal("expected altered request id replay to fail")
	}
}
