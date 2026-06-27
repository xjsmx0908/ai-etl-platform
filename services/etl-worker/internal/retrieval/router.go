package retrieval

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	structuredTokenPattern = regexp.MustCompile(`(?i)(\b[A-Z]{1,10}[-_]?\d{4,}\b|\b\d{6,}\b|\b[0-9a-f]{8,}\b)`)
	exactIntentKeywords    = []string{"订单", "工单", "合同", "编号", "错误码", "trace", "traceid", "request id", "email", "手机号"}
	semanticIntentKeywords = []string{"是什么", "如何", "怎么", "为什么", "流程", "制度", "原则", "说明", "解释", "对比"}
)

// RouteQuery chooses a retrieval route with conservative, deterministic rules.
func RouteQuery(question string, elasticEnabled bool) Route {
	q := strings.ToLower(strings.TrimSpace(question))
	if !elasticEnabled {
		return Route{
			Strategy:      StrategySemantic,
			UseQdrant:     true,
			UseElastic:    false,
			QdrantWeight:  1,
			ElasticWeight: 0,
		}
	}

	if isExactKeywordIntent(q) {
		return Route{
			Strategy:      StrategyExactKeyword,
			UseQdrant:     true,
			UseElastic:    true,
			QdrantWeight:  0.25,
			ElasticWeight: 0.75,
		}
	}

	if isSemanticIntent(q) {
		return Route{
			Strategy:      StrategySemantic,
			UseQdrant:     true,
			UseElastic:    true,
			QdrantWeight:  0.80,
			ElasticWeight: 0.20,
		}
	}

	return Route{
		Strategy:      StrategyHybrid,
		UseQdrant:     true,
		UseElastic:    true,
		QdrantWeight:  0.55,
		ElasticWeight: 0.45,
	}
}

func isExactKeywordIntent(q string) bool {
	if structuredTokenPattern.MatchString(q) {
		return true
	}
	for _, kw := range exactIntentKeywords {
		if strings.Contains(q, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

func isSemanticIntent(q string) bool {
	if utf8.RuneCountInString(q) > 80 {
		return true
	}
	for _, kw := range semanticIntentKeywords {
		if strings.Contains(q, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}
