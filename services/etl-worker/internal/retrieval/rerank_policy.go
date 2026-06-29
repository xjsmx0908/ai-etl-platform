package retrieval

import (
	"regexp"
	"strings"
	"unicode"

	"ai-etl-pipeline/internal/config"
)

var (
	emailExactTokenPattern      = regexp.MustCompile(`(?i)\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,}\b`)
	uuidExactTokenPattern       = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	phoneExactTokenPattern      = regexp.MustCompile(`(?i)(?:\+?\d[\d -]{7,}\d)`)
	candidateExactTokenPattern  = regexp.MustCompile(`(?i)\b[a-z0-9][a-z0-9._:/\-]{5,}[a-z0-9]\b`)
	exactTokenSeparatorReplacer = strings.NewReplacer(" ", "", "-", "", "_", "", ".", "", ":", "", "/", "")
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
		if hasExactCandidateEvidence(q, candidates) {
			return false, "exact_candidate_match"
		}
		return true, "auto"
	default:
		return false, "invalid_policy"
	}
}

func hasExactCandidateEvidence(question string, candidates []Candidate) bool {
	tokens := extractExactTokens(question)
	if len(tokens) == 0 {
		return false
	}
	for _, candidate := range candidates {
		if candidateMatchesAnyExactToken(candidate, tokens) {
			return true
		}
	}
	return false
}

func extractExactTokens(question string) []string {
	q := strings.ToLower(strings.TrimSpace(question))
	if q == "" {
		return nil
	}

	seen := make(map[string]struct{})
	tokens := make([]string, 0, 4)
	add := func(raw string) {
		token := strings.ToLower(strings.TrimSpace(raw))
		if !isStrongExactToken(token) {
			return
		}
		if _, ok := seen[token]; ok {
			return
		}
		seen[token] = struct{}{}
		tokens = append(tokens, token)
	}

	for _, match := range emailExactTokenPattern.FindAllString(q, -1) {
		add(match)
	}
	for _, match := range uuidExactTokenPattern.FindAllString(q, -1) {
		add(match)
	}
	for _, match := range phoneExactTokenPattern.FindAllString(q, -1) {
		add(match)
	}
	for _, match := range structuredTokenPattern.FindAllString(q, -1) {
		add(match)
	}
	for _, match := range candidateExactTokenPattern.FindAllString(q, -1) {
		add(match)
	}
	return tokens
}

func candidateMatchesAnyExactToken(candidate Candidate, tokens []string) bool {
	content := strings.ToLower(candidate.DocID + " " + candidate.ChunkID + " " + candidate.Content)
	for _, token := range tokens {
		if exactTokenMatches(content, token) {
			return true
		}
	}
	return false
}

func exactTokenMatches(content, token string) bool {
	if token == "" {
		return false
	}
	if strings.Contains(content, token) {
		return true
	}
	return strings.Contains(normalizeExactToken(content), normalizeExactToken(token))
}

func isStrongExactToken(token string) bool {
	normalized := normalizeExactToken(token)
	if len(normalized) < 6 {
		return false
	}
	hasDigit := false
	hasLetter := false
	for _, r := range normalized {
		if unicode.IsDigit(r) {
			hasDigit = true
		}
		if unicode.IsLetter(r) {
			hasLetter = true
		}
	}
	if strings.Contains(token, "@") {
		return hasLetter && strings.Contains(token, ".")
	}
	if strings.ContainsAny(token, "-_./:") {
		return hasDigit
	}
	return hasDigit && (hasLetter || len(normalized) >= 8)
}

func normalizeExactToken(raw string) string {
	return exactTokenSeparatorReplacer.Replace(strings.ToLower(strings.TrimSpace(raw)))
}
