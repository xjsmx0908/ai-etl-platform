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
			got, reason := shouldRerank(tc.policy, tc.route, tc.question, candidates)
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
