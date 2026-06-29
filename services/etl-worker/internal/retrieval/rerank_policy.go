package retrieval

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
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

type rerankDecision struct {
	ShouldRerank        bool
	Reason              string
	ProtectExactMatches bool
	Evidence            exactEvidence
}

type exactEvidence struct {
	QueryTokens         []string
	MatchedTokens       []string
	MatchedCandidateIDs map[string]struct{}
	TopCandidateMatched bool
}

func shouldRerank(policy string, route Route, question string, candidates []Candidate) (bool, string) {
	decision := planRerank(policy, route, question, candidates)
	return decision.ShouldRerank, decision.Reason
}

func planRerank(policy string, route Route, question string, candidates []Candidate) rerankDecision {
	evidence := collectExactEvidence(question, candidates)
	if len(candidates) <= 1 {
		return rerankDecision{ShouldRerank: false, Reason: "not_enough_candidates", Evidence: evidence}
	}

	switch strings.ToLower(strings.TrimSpace(policy)) {
	case config.RerankPolicyAlways:
		return rerankDecision{ShouldRerank: true, Reason: "always", Evidence: evidence}
	case "", config.RerankPolicyAuto:
		q := strings.ToLower(strings.TrimSpace(question))
		if route.Strategy == StrategyExactKeyword || isExactKeywordIntent(q) {
			return rerankDecision{ShouldRerank: false, Reason: "exact_keyword_intent", Evidence: evidence}
		}
		if evidence.HasMatches() {
			return rerankDecision{
				ShouldRerank:        true,
				Reason:              "exact_candidate_match_protected",
				ProtectExactMatches: true,
				Evidence:            evidence,
			}
		}
		return rerankDecision{ShouldRerank: true, Reason: "auto", Evidence: evidence}
	default:
		return rerankDecision{ShouldRerank: false, Reason: "invalid_policy", Evidence: evidence}
	}
}

func hasExactCandidateEvidence(question string, candidates []Candidate) bool {
	return collectExactEvidence(question, candidates).HasMatches()
}

func collectExactEvidence(question string, candidates []Candidate) exactEvidence {
	tokens := extractExactTokens(question)
	evidence := exactEvidence{
		QueryTokens:         tokens,
		MatchedCandidateIDs: make(map[string]struct{}),
	}
	if len(tokens) == 0 {
		return evidence
	}

	seenTokens := make(map[string]struct{})
	for i, candidate := range candidates {
		matched := candidateMatchedExactTokens(candidate, tokens)
		if len(matched) == 0 {
			continue
		}
		evidence.MatchedCandidateIDs[candidateID(candidate)] = struct{}{}
		if i == 0 {
			evidence.TopCandidateMatched = true
		}
		for _, token := range matched {
			if _, ok := seenTokens[token]; ok {
				continue
			}
			seenTokens[token] = struct{}{}
			evidence.MatchedTokens = append(evidence.MatchedTokens, token)
		}
	}
	return evidence
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
	return len(candidateMatchedExactTokens(candidate, tokens)) > 0
}

func candidateMatchedExactTokens(candidate Candidate, tokens []string) []string {
	content := candidateExactContent(candidate)
	matched := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if exactTokenMatches(content, token) {
			matched = append(matched, token)
		}
	}
	return matched
}

func candidateExactContent(candidate Candidate) string {
	parts := []string{candidate.DocID, candidate.ChunkID, candidate.Content}
	if len(candidate.Metadata) > 0 {
		keys := make([]string, 0, len(candidate.Metadata))
		for key := range candidate.Metadata {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			parts = append(parts, candidate.Metadata[key])
		}
	}
	return strings.ToLower(strings.Join(parts, " "))
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

func protectExactMatches(reranked, fused []Candidate, evidence exactEvidence, topK int) []Candidate {
	if !evidence.HasMatches() {
		return topCandidates(reranked, topK)
	}

	seen := make(map[string]struct{}, len(reranked)+len(fused))
	exact := make([]Candidate, 0, len(reranked))
	other := make([]Candidate, 0, len(reranked))
	appendCandidate := func(candidate Candidate) {
		id := candidateID(candidate)
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		if evidence.MatchesCandidate(candidate) {
			exact = append(exact, candidate)
			return
		}
		other = append(other, candidate)
	}

	for _, candidate := range reranked {
		appendCandidate(candidate)
	}
	for _, candidate := range fused {
		appendCandidate(candidate)
	}

	out := append(exact, other...)
	if topK <= 0 || topK > len(out) {
		topK = len(out)
	}
	out = append([]Candidate(nil), out[:topK]...)
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

func (e exactEvidence) HasMatches() bool {
	return len(e.MatchedCandidateIDs) > 0
}

func (e exactEvidence) MatchesCandidate(candidate Candidate) bool {
	_, ok := e.MatchedCandidateIDs[candidateID(candidate)]
	return ok
}

func (e exactEvidence) MatchedCandidateCount() int {
	return len(e.MatchedCandidateIDs)
}

func (e exactEvidence) TokenHashes() []string {
	if len(e.MatchedTokens) == 0 {
		return nil
	}
	hashes := make([]string, 0, len(e.MatchedTokens))
	for _, token := range e.MatchedTokens {
		sum := sha256.Sum256([]byte(token))
		hashes = append(hashes, hex.EncodeToString(sum[:8]))
	}
	sort.Strings(hashes)
	return hashes
}

func candidateID(candidate Candidate) string {
	if candidate.ChunkID != "" {
		return candidate.ChunkID
	}
	if candidate.DocID != "" {
		return candidate.Source + ":" + candidate.DocID + ":" + candidate.Content
	}
	return candidate.Source + "::" + candidate.Content
}
