package agent

import (
	"context"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v5"
)

func TestPostgresApprovalStoreLifecycle(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	store := NewPostgresApprovalStore(mock)
	requestedAt := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	approval := ApprovalRequest{
		ID: "approval-1", RunID: "run-1", TenantID: "acme", StepIndex: 2,
		ToolName: "publish_document", ToolArguments: []byte(`{"document_id":"doc-1"}`),
		Status: ApprovalPending, RequestedBy: "author", RequestedAt: requestedAt,
	}
	mock.ExpectExec("INSERT INTO agent_approvals").
		WithArgs(approval.ID, approval.RunID, approval.TenantID, approval.StepIndex, approval.ToolName,
			approval.ToolArguments, approval.Status, approval.RequestedBy, approval.RequestedAt).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := store.CreateApproval(context.Background(), approval); err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}

	decidedAt := requestedAt.Add(time.Minute)
	rows := pgxmock.NewRows([]string{
		"id", "run_id", "tenant_id", "step_index", "tool_name", "tool_arguments", "status",
		"requested_by", "requested_at", "decided_by", "decided_at", "reason",
	}).AddRow(approval.ID, approval.RunID, approval.TenantID, approval.StepIndex, approval.ToolName,
		approval.ToolArguments, ApprovalApproved, approval.RequestedBy, approval.RequestedAt,
		"reviewer", decidedAt, "ready")
	mock.ExpectQuery("UPDATE agent_approvals").
		WithArgs("acme", "approval-1", ApprovalApproved, "reviewer", "ready", decidedAt).
		WillReturnRows(rows)
	decided, err := store.DecideApproval(context.Background(), "acme", "approval-1", ApprovalApproved, "reviewer", "ready", decidedAt)
	if err != nil {
		t.Fatalf("DecideApproval: %v", err)
	}
	if decided.Status != ApprovalApproved || decided.DecidedBy != "reviewer" {
		t.Fatalf("unexpected approval: %+v", decided)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
