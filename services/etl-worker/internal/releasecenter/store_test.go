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
	now := time.Now().UTC()
	rows := pgxmock.NewRows([]string{
		"request_id", "tenant_id", "document_id", "document_version_id", "generation_id",
		"expected_chunk_count", "expected_chunk_digest", "release_revision", "review_id",
		"policy_id", "approver_group_id", "allow_requester_approval", "required_approvals",
		"state", "requested_by", "created_at", "updated_at",
	}).AddRow("request-1", "acme", "doc-1", "job-1", "gen-1", 3, "sha256:ready", int64(1), "review-1",
		"", "", false, 1, RequestNeedsInfo, "admin-1", now, now)
	mock.ExpectQuery("WITH stale AS").WithArgs(25).WillReturnRows(rows)
	updated, err := NewPostgresStore(mock).ReconcileStaleRequests(context.Background(), 25)
	if err != nil || len(updated) != 1 || updated[0].State != RequestNeedsInfo {
		t.Fatalf("updated=%+v err=%v", updated, err)
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

func TestExpireDueReviewsSQL(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	rows := pgxmock.NewRows([]string{
		"request_id", "tenant_id", "document_id", "document_version_id", "generation_id",
		"expected_chunk_count", "expected_chunk_digest", "release_revision", "review_id",
		"policy_id", "approver_group_id", "allow_requester_approval", "required_approvals",
		"state", "requested_by", "created_at", "updated_at",
	}).AddRow("request-1", "acme", "doc-1", "job-1", "gen-1", 3, "sha256:ready", int64(1), "review-1",
		"", "", false, 1, RequestNeedsInfo, "admin-1", now, now)
	mock.ExpectQuery("WITH due AS").WithArgs(now, 25).WillReturnRows(rows)
	updated, err := NewPostgresStore(mock).ExpireDueReviews(context.Background(), now, 25)
	if err != nil || len(updated) != 1 || updated[0].State != RequestNeedsInfo {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListReviewJobsIncludesExpiredNeedsInfo(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	rows := pgxmock.NewRows([]string{
		"tenant_id", "doc_id", "uploaded_by", "current_version_id", "generation_id",
		"expected_chunk_count", "expected_chunk_digest", "revision",
	}).AddRow("acme", "doc-1", "uploader", "job-1", "gen-1", 3, "sha256:ready", int64(1))
	mock.ExpectQuery("rv.status='expired'").WithArgs(10).WillReturnRows(rows)
	jobs, err := NewPostgresStore(mock).ListReviewJobs(context.Background(), 10)
	if err != nil || len(jobs) != 1 || jobs[0].DocumentID != "doc-1" || jobs[0].Candidate.GenerationID != "gen-1" {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSaveRequestAllowsReviewIDRotationForSameCandidate(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	createdAt := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	mock.ExpectExec("INSERT INTO release_center_requests").WithArgs(
		"request-1", "tenant-a", "doc-1", "job-1", "gen-1", 3, "sha256:ready", int64(1), "review-2",
		nil, nil, false, 1, RequestApprovalPending, "admin-1", createdAt, updatedAt,
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	request := ReleaseRequest{
		ID: "request-1", TenantID: "tenant-a", DocumentID: "doc-1", ReviewID: "review-2",
		Candidate: publicationworkflow.Candidate{
			DocumentID: "doc-1", DocumentVersionID: "job-1", GenerationID: "gen-1",
			ExpectedChunkCount: 3, ExpectedChunkDigest: "sha256:ready", ReleaseRevision: 1,
		},
		RequiredApprovals: 1, State: RequestApprovalPending, RequestedBy: "admin-1",
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
	if err := NewPostgresStore(mock).SaveRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeExpiredReviewsDeletesUnreferenced(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	retention := 24 * time.Hour
	mock.ExpectExec("DELETE FROM release_center_reviews").WithArgs(now.Add(-retention), 20).WillReturnResult(pgxmock.NewResult("DELETE", 2))
	deleted, err := NewPostgresStore(mock).PurgeExpiredReviews(context.Background(), now, retention, 20)
	if err != nil || deleted != 2 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResetDecisionsSQL(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectExec("DELETE FROM release_center_decisions").WithArgs("acme", "request-1").WillReturnResult(pgxmock.NewResult("DELETE", 1))
	if err := NewPostgresStore(mock).ResetDecisions(context.Background(), "acme", "request-1"); err != nil {
		t.Fatal(err)
	}
}
