package releasecenter

import (
	"context"
	"testing"

	"ai-etl-pipeline/internal/publicationworkflow"
)

func TestApprovalPolicyMatchesMostSpecificRule(t *testing.T) {
	store := NewMemoryApprovalPolicyStore()
	if err := store.PutPolicy(context.Background(), ApprovalPolicy{ID: "default", TenantID: "acme", RequiredApprovals: 1, ApproverGroupID: "admins", Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPolicy(context.Background(), ApprovalPolicy{ID: "hr-confidential", TenantID: "acme", KnowledgeSpaceID: "hr", Permission: "confidential", RequiredApprovals: 2, ApproverGroupID: "hr-reviewers", Active: true}); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.ResolveApprovalPolicy(nil, "acme", "hr", "confidential", RiskLow)
	if err != nil || !found {
		t.Fatalf("policy found=%v err=%v", found, err)
	}
	if got.ID != "hr-confidential" || got.RequiredApprovals != 2 || got.ApproverGroupID != "hr-reviewers" {
		t.Fatalf("policy=%+v", got)
	}
}

func TestApprovalPolicyRejectsAgentRiskBelowConfiguredMinimum(t *testing.T) {
	store := NewMemoryApprovalPolicyStore()
	if err := store.PutPolicy(context.Background(), ApprovalPolicy{ID: "high-risk", TenantID: "acme", MinimumRisk: RiskHigh, RequiredApprovals: 2, ApproverGroupID: "risk", Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.ResolveApprovalPolicy(nil, "acme", "production", "internal", RiskLow); err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestApprovalGroupMembershipIsTenantScoped(t *testing.T) {
	store := NewMemoryApprovalPolicyStore()
	if err := store.PutGroup(context.Background(), ApprovalGroup{ID: "reviewers", TenantID: "acme", Name: "Reviewers", Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetGroupMember(context.Background(), "acme", "reviewers", "alice", true); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.IsApprovalGroupMember(context.Background(), "acme", "reviewers", "alice"); err != nil || !ok {
		t.Fatalf("member=%v err=%v", ok, err)
	}
	if ok, err := store.IsApprovalGroupMember(context.Background(), "other", "reviewers", "alice"); err != nil || ok {
		t.Fatalf("cross-tenant member=%v err=%v", ok, err)
	}
}

func TestApprovalPolicyValidatesConfiguration(t *testing.T) {
	store := NewMemoryApprovalPolicyStore()
	if err := store.PutPolicy(context.Background(), ApprovalPolicy{ID: "invalid", TenantID: "acme", RequiredApprovals: 3, ApproverGroupID: "group"}); err == nil {
		t.Fatal("expected invalid approval count")
	}
}

func TestAuthorizeApprovalRequiresConfiguredGroupMember(t *testing.T) {
	store := NewMemoryApprovalPolicyStore()
	if err := store.PutGroup(context.Background(), ApprovalGroup{ID: "reviewers", TenantID: "acme", Name: "Reviewers", Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetGroupMember(context.Background(), "acme", "reviewers", "alice", true); err != nil {
		t.Fatal(err)
	}
	request := ReleaseRequest{TenantID: "acme", RequestedBy: "requester", ApproverGroupID: "reviewers"}
	if err := AuthorizeApproval(context.Background(), store, publicationworkflow.Actor{TenantID: "acme", UserID: "bob"}, request); err == nil {
		t.Fatal("expected non-member to be rejected")
	}
	if err := AuthorizeApproval(context.Background(), store, publicationworkflow.Actor{TenantID: "acme", UserID: "alice"}, request); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalPolicyIsDeterministicAndCannotBeLoweredByAgent(t *testing.T) {
	checks := []struct {
		name       string
		permission string
		risk       RiskLevel
		available  bool
		want       int
		wantState  RequestState
	}{
		{"ordinary", "internal", RiskLow, true, 1, RequestApprovalPending},
		{"confidential", "confidential", RiskLow, true, 2, RequestApprovalPending},
		{"agent-high-risk", "internal", RiskHigh, true, 2, RequestApprovalPending},
		{"agent-unavailable", "internal", RiskLow, false, 1, RequestManualException},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluatePolicy(PolicyInput{Permission: tc.permission, Risk: tc.risk, AgentAvailable: tc.available})
			if got.RequiredApprovals != tc.want || got.State != tc.wantState {
				t.Fatalf("policy=%+v, want approvals=%d state=%s", got, tc.want, tc.wantState)
			}
		})
	}
}

func TestApprovalPolicyIgnoresUntrustedAgentLowRiskForConfidentialDocument(t *testing.T) {
	got := EvaluatePolicy(PolicyInput{Permission: "confidential", Risk: RiskLow, AgentAvailable: true})
	if got.RequiredApprovals != 2 {
		t.Fatalf("confidential policy was lowered: %+v", got)
	}
}

func TestProjectOverviewUsesDeterministicBusinessStates(t *testing.T) {
	tests := []struct {
		name  string
		input OverviewInput
		state string
		block string
	}{
		{"published", OverviewInput{PublicationStatus: "published"}, "published", ""},
		{"processing", OverviewInput{IngestionStatus: "processing"}, "checking", "ingestion_not_completed"},
		{"review blocked", OverviewInput{IngestionStatus: "completed", ReviewStatus: "failed"}, "review_blocked", "agent_review_unavailable"},
		{"approval", OverviewInput{IngestionStatus: "completed", RequestState: RequestApprovalPending}, "approval_pending", ""},
		{"needs info", OverviewInput{IngestionStatus: "completed", KnowledgeSpaceID: "production", EffectiveDatePresent: true}, "needs_info", "owner_required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ProjectOverview(tt.input)
			if got.State != tt.state {
				t.Fatalf("state=%q want %q", got.State, tt.state)
			}
			if tt.block != "" && (len(got.Blockers) == 0 || got.Blockers[0] != tt.block) {
				t.Fatalf("blockers=%v want first %q", got.Blockers, tt.block)
			}
		})
	}
}
