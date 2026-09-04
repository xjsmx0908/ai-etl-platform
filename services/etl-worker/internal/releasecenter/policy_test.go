package releasecenter

import "testing"

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
