package retrieval

import (
	"strings"

	"ai-etl-pipeline/internal/config"
)

func shouldRerank(policy string, route Route, question string, candidates []Candidate) (bool, string) {
	if len(candidates) <= 1 {
		return false, "not_enough_candidates"
	}

	switch strings.ToLower(strings.TrimSpace(policy)) {
	case config.RerankPolicyAlways:
		return true, "always"
	case "", config.RerankPolicyAuto:
		q := strings.ToLower(strings.TrimSpace(question))
		if route.Strategy == StrategyExactKeyword || isExactKeywordIntent(q) {
			return false, "exact_keyword_intent"
		}
		return true, "auto"
	default:
		return false, "invalid_policy"
	}
}
