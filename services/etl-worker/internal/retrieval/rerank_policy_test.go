package retrieval

import (
	"testing"

	"ai-etl-pipeline/internal/config"
)

func TestPlanRerankPolicy(t *testing.T) {
	candidates := []Candidate{
		{ChunkID: "c1", Content: "first"},
		{ChunkID: "c2", Content: "second"},
	}

	tests := []struct {
		name        string
		policy      string
		route       Route
		question    string
		wantRerank  bool
		wantProtect bool
		reason      string
	}{
		{
			name:       "auto skips exact route",
			policy:     config.RerankPolicyAuto,
			route:      Route{Strategy: StrategyExactKeyword},
			question:   "订单 A20240518001 的退款状态",
			wantRerank: false,
			reason:     "exact_keyword_intent",
		},
		{
			name:       "auto skips exact intent even when route is semantic",
			policy:     config.RerankPolicyAuto,
			route:      Route{Strategy: StrategySemantic},
			question:   "Find the omega-shared-window source for alpha040.",
			wantRerank: false,
			reason:     "exact_keyword_intent",
		},
		{
			name:       "auto reranks semantic route",
			policy:     config.RerankPolicyAuto,
			route:      Route{Strategy: StrategySemantic},
			question:   "公司的报销制度是什么",
			wantRerank: true,
			reason:     "auto",
		},
		{
			name:        "auto protects semantic route when candidate has exact token evidence",
			policy:      config.RerankPolicyAuto,
			route:       Route{Strategy: StrategySemantic},
			question:    "请解释客户参考号 x9k-77q-plum 的处理说明",
			wantRerank:  true,
			wantProtect: true,
			reason:      "exact_candidate_match_protected",
		},
		{
			name:       "auto reranks hybrid route",
			policy:     config.RerankPolicyAuto,
			route:      Route{Strategy: StrategyHybrid},
			question:   "policy reimbursement examples",
			wantRerank: true,
			reason:     "auto",
		},
		{
			name:       "always reranks exact route",
			policy:     config.RerankPolicyAlways,
			route:      Route{Strategy: StrategyExactKeyword},
			question:   "订单 A20240518001 的退款状态",
			wantRerank: true,
			reason:     "always",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testCandidates := candidates
			if tc.reason == "exact_candidate_match_protected" {
				testCandidates = []Candidate{
					{ChunkID: "c1", Content: "客户参考号 x9k-77q-plum 已完成处理。"},
					{ChunkID: "c2", Content: "客户参考号的通用处理说明。"},
				}
			}
			decision := planRerank(tc.policy, tc.route, tc.question, testCandidates)
			if decision.ShouldRerank != tc.wantRerank || decision.ProtectExactMatches != tc.wantProtect || decision.Reason != tc.reason {
				t.Fatalf("unexpected decision: %+v", decision)
			}
		})
	}
}

func TestShouldRerankPolicy_SkipsSingleCandidate(t *testing.T) {
	got, reason := shouldRerank(config.RerankPolicyAlways, Route{Strategy: StrategySemantic}, "why", []Candidate{{ChunkID: "c1"}})
	if got || reason != "not_enough_candidates" {
		t.Fatalf("expected single candidate skip, got (%v, %q)", got, reason)
	}
}

func TestExtractExactTokens(t *testing.T) {
	tokens := extractExactTokens("联系 user.audit-01@example.com，工单 x9k-77q-plum，手机号 138 0013 8000，普通短语 omega-shared-window 不应保护")
	want := map[string]bool{
		"user.audit-01@example.com": true,
		"x9k-77q-plum":              true,
		"138 0013 8000":             true,
	}
	for _, token := range tokens {
		delete(want, token)
		if token == "omega-shared-window" {
			t.Fatalf("did not expect ordinary hyphenated phrase as exact token: %v", tokens)
		}
	}
	if len(want) > 0 {
		t.Fatalf("missing expected exact tokens: %v from %v", want, tokens)
	}
}

func TestHasExactCandidateEvidence_NormalizesSeparators(t *testing.T) {
	candidates := []Candidate{
		{ChunkID: "c1", Content: "客户参考号 x9k77qplum 已完成处理。"},
	}
	if !hasExactCandidateEvidence("请解释客户参考号 x9k-77q-plum 的处理说明", candidates) {
		t.Fatal("expected exact evidence when candidate contains normalized token")
	}
}

func TestExactCandidateEvidence_UsesSchemaMetadata(t *testing.T) {
	candidates := []Candidate{
		{ChunkID: "c1", Content: "合同处理说明。", Metadata: map[string]string{"contract_no": "CN-2026-0001"}},
	}
	evidence := collectExactEvidence("请查合同 CN-2026-0001 的审批状态", candidates)
	if !evidence.HasMatches() {
		t.Fatal("expected exact evidence from candidate metadata")
	}
	if !evidence.TopCandidateMatched {
		t.Fatal("expected top candidate exact evidence")
	}
}

func TestProtectExactMatchesPinsExactCandidates(t *testing.T) {
	fused := []Candidate{
		{ChunkID: "exact", Content: "客户参考号 x9k-77q-plum 已完成处理。", Rank: 1},
		{ChunkID: "general", Content: "客户参考号通用处理说明。", Rank: 2},
	}
	evidence := collectExactEvidence("请解释客户参考号 x9k-77q-plum 的处理说明", fused)
	reranked := []Candidate{
		{ChunkID: "general", Content: "客户参考号通用处理说明。", Score: 0.99, Rank: 1},
		{ChunkID: "exact", Content: "客户参考号 x9k-77q-plum 已完成处理。", Score: 0.10, Rank: 2},
	}

	got := protectExactMatches(reranked, fused, evidence, 2)
	if got[0].ChunkID != "exact" || got[1].ChunkID != "general" {
		t.Fatalf("expected exact candidate pinned before non-exact, got %+v", got)
	}
}
