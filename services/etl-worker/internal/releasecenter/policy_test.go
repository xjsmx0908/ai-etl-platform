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
