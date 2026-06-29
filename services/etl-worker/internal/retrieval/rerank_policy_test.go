package retrieval

import (
	"testing"

	"ai-etl-pipeline/internal/config"
)

func TestShouldRerankPolicy(t *testing.T) {
	candidates := []Candidate{
		{ChunkID: "c1", Content: "first"},
		{ChunkID: "c2", Content: "second"},
	}

	tests := []struct {
		name     string
		policy   string
		route    Route
		question string
		want     bool
		reason   string
	}{
		{
			name:     "auto skips exact route",
			policy:   config.RerankPolicyAuto,
			route:    Route{Strategy: StrategyExactKeyword},
			question: "订单 A20240518001 的退款状态",
			want:     false,
			reason:   "exact_keyword_intent",
		},
		{
			name:     "auto skips exact intent even when route is semantic",
			policy:   config.RerankPolicyAuto,
			route:    Route{Strategy: StrategySemantic},
			question: "Find the omega-shared-window source for alpha040.",
			want:     false,
			reason:   "exact_keyword_intent",
		},
		{
			name:     "auto reranks semantic route",
			policy:   config.RerankPolicyAuto,
			route:    Route{Strategy: StrategySemantic},
			question: "公司的报销制度是什么",
			want:     true,
			reason:   "auto",
		},
		{
			name:     "auto skips semantic route when candidate has exact token evidence",
			policy:   config.RerankPolicyAuto,
			route:    Route{Strategy: StrategySemantic},
			question: "请解释客户参考号 x9k-77q-plum 的处理说明",
			want:     false,
			reason:   "exact_candidate_match",
		},
		{
			name:     "auto reranks hybrid route",
			policy:   config.RerankPolicyAuto,
			route:    Route{Strategy: StrategyHybrid},
			question: "policy reimbursement examples",
			want:     true,
			reason:   "auto",
		},
		{
			name:     "always reranks exact route",
			policy:   config.RerankPolicyAlways,
			route:    Route{Strategy: StrategyExactKeyword},
			question: "订单 A20240518001 的退款状态",
			want:     true,
			reason:   "always",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testCandidates := candidates
			if tc.reason == "exact_candidate_match" {
				testCandidates = []Candidate{
					{ChunkID: "c1", Content: "客户参考号 x9k-77q-plum 已完成处理。"},
					{ChunkID: "c2", Content: "客户参考号的通用处理说明。"},
				}
			}
			got, reason := shouldRerank(tc.policy, tc.route, tc.question, testCandidates)
			if got != tc.want || reason != tc.reason {
				t.Fatalf("expected (%v, %q), got (%v, %q)", tc.want, tc.reason, got, reason)
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
