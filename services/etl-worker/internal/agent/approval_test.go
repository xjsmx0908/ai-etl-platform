package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestMemoryApprovalStoreDecidesAndIsolatesByTenant(t *testing.T) {
	store := NewMemoryApprovalStore()
	requestedAt := time.Now().UTC()
	approval := ApprovalRequest{
		ID:            ApprovalIDForStep("run-1", 1),
		RunID:         "run-1",
		TenantID:      "tenant-a",
		StepIndex:     1,
		ToolName:      "publish_report",
		ToolArguments: json.RawMessage(`{"title":"quarterly"}`),
		Status:        ApprovalPending,
		RequestedBy:   "user-a",
		RequestedAt:   requestedAt,
	}
	if err := store.CreateApproval(context.Background(), approval); err != nil {
		t.Fatalf("create approval: %v", err)
	}

	if _, err := store.LoadApproval(context.Background(), "tenant-b", approval.ID); err == nil {
		t.Fatal("expected tenant-b to be unable to load tenant-a approval")
	}

	listed, err := store.ListRunApprovals(context.Background(), "tenant-a", "run-1")
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected one approval, got %+v", listed)
	}
	listed[0].ToolArguments[0] = '['

	loaded, err := store.LoadApproval(context.Background(), "tenant-a", approval.ID)
	if err != nil {
		t.Fatalf("load approval: %v", err)
	}
	if string(loaded.ToolArguments) != `{"title":"quarterly"}` {
		t.Fatalf("expected cloned tool arguments, got %s", loaded.ToolArguments)
	}

	decided, err := store.DecideApproval(context.Background(), "tenant-a", approval.ID, ApprovalApproved, "admin-a", "approved", requestedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("decide approval: %v", err)
	}
	if decided.Status != ApprovalApproved || decided.DecidedBy != "admin-a" || decided.Reason != "approved" {
		t.Fatalf("unexpected decided approval: %+v", decided)
	}
	if _, err := store.DecideApproval(context.Background(), "tenant-a", approval.ID, ApprovalRejected, "admin-b", "late reject", requestedAt.Add(2*time.Minute)); err == nil {
		t.Fatal("expected second decision to fail")
	}
}
