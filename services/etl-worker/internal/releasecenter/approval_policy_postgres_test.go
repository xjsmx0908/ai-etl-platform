package releasecenter

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v5"
)

func TestPostgresApprovalPolicyStorePersistsGroupMemberAndPolicy(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	store := NewPostgresStore(mock)
	mock.ExpectExec("INSERT INTO release_center_approval_groups").WithArgs("acme", "legal", "Legal", true).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := store.PutGroup(context.Background(), ApprovalGroup{TenantID: "acme", ID: "legal", Name: "Legal", Active: true}); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("INSERT INTO release_center_approval_group_members").WithArgs("acme", "legal", "alice", true).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := store.SetGroupMember(context.Background(), "acme", "legal", "alice", true); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("INSERT INTO release_center_approval_policies").WithArgs("acme", "legal-policy", "hr", "confidential", RiskLow, 2, "legal", false, 0, true).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := store.PutPolicy(context.Background(), ApprovalPolicy{TenantID: "acme", ID: "legal-policy", KnowledgeSpaceID: "hr", Permission: "confidential", RequiredApprovals: 2, ApproverGroupID: "legal", Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresApprovalPolicyStoreResolvesAndChecksMembership(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	store := NewPostgresStore(mock)
	mock.ExpectQuery("FROM release_center_approval_policies").WithArgs("acme", "hr", "confidential", string(RiskLow)).WillReturnRows(pgxmock.NewRows([]string{
		"policy_id", "tenant_id", "knowledge_space_id", "permission", "minimum_risk", "required_approvals", "approver_group_id", "allow_requester_approval", "priority", "active",
	}).AddRow("legal-policy", "acme", "hr", "confidential", RiskLow, 2, "legal", false, 10, true))
	policy, found, err := store.ResolveApprovalPolicy(context.Background(), "acme", "hr", "confidential", RiskLow)
	if err != nil || !found || policy.ID != "legal-policy" || policy.RequiredApprovals != 2 {
		t.Fatalf("policy=%+v found=%v err=%v", policy, found, err)
	}
	mock.ExpectQuery("SELECT EXISTS").WithArgs("acme", "legal", "alice").WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	member, err := store.IsApprovalGroupMember(context.Background(), "acme", "legal", "alice")
	if err != nil || !member {
		t.Fatalf("member=%v err=%v", member, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
