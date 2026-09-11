package retrieval

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

const (
	hubOverlapFloor = 0.08
	hubDemoteFactor = 0.15
)

var cjkUnigramStop = map[rune]struct{}{
	'的': {}, '了': {}, '在': {}, '是': {}, '和': {}, '或': {}, '与': {}, '等': {},
	'中': {}, '对': {}, '为': {}, '也': {}, '就': {}, '都': {}, '而': {}, '及': {},
	'其': {}, '被': {}, '把': {}, '从': {}, '到': {}, '吗': {}, '呢': {}, '啊': {},
	'请': {}, '问': {}, '这': {}, '那': {}, '有': {}, '不': {}, '要': {}, '会': {},
}

// stabilizeRanking demotes long hub documents that only weakly overlap the
// query, so unique-document-first diversity cannot promote them into Top-1.
func stabilizeRanking(query string, candidates []Candidate) []Candidate {
	if len(candidates) < 2 {
		return candidates
	}

	perDoc := make(map[string]int, len(candidates))
	for _, candidate := range candidates {
		if candidate.DocID == "" {
			continue
		}
		perDoc[candidate.DocID]++
	}

	queryTokens := lexicalTokens(query)
	overlaps := make([]float64, len(candidates))
	maxOverlap := 0.0
	for i, candidate := range candidates {
		overlaps[i] = lexicalOverlap(queryTokens, lexicalTokens(candidateLexicalText(candidate)))
		if overlaps[i] > maxOverlap {
			maxOverlap = overlaps[i]
		}
	}

	type scored struct {
		candidate Candidate
		score     float64
		orig      int
	}
	ranked := make([]scored, len(candidates))
	for i, candidate := range candidates {
		penalty := 1.0
		if maxOverlap >= hubOverlapFloor && overlaps[i] < hubOverlapFloor {
			penalty = hubDemoteFactor
			if perDoc[candidate.DocID] >= 3 {
				penalty *= hubPenalty(perDoc[candidate.DocID])
			}
		}
		ranked[i] = scored{candidate: candidate, score: candidate.Score * penalty, orig: i}
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			return ranked[i].orig < ranked[j].orig
		}
		return ranked[i].score > ranked[j].score
	})

	out := make([]Candidate, len(ranked))
	for i, item := range ranked {
		out[i] = item.candidate
		out[i].Rank = i + 1
	}
	return out
}

func hubPenalty(chunkCount int) float64 {
	if chunkCount < 3 {
		return 1
	}
	return 1 / math.Sqrt(float64(chunkCount))
}

func lexicalTokens(text string) map[string]struct{} {
	normalized := strings.ToLower(norm.NFKC.String(text))
	tokens := make(map[string]struct{})
	var ascii []rune
	var cjk []rune

	flushASCII := func() {
		if len(ascii) >= 2 {
			tokens[string(ascii)] = struct{}{}
		}
		ascii = ascii[:0]
	}
	flushCJK := func() {
		addCJKTokens(tokens, cjk)
		cjk = cjk[:0]
	}

	for _, r := range normalized {
		switch {
		case unicode.Is(unicode.Han, r):
			flushASCII()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == ':' || r == '.' || r == '/':
			flushCJK()
			ascii = append(ascii, unicode.ToLower(r))
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return tokens
}

func addCJKTokens(tokens map[string]struct{}, chars []rune) {
	if len(chars) == 0 {
		return
	}
	for _, r := range chars {
		if _, stop := cjkUnigramStop[r]; stop {
			continue
		}
		tokens[string(r)] = struct{}{}
	}
	for i := 0; i < len(chars)-1; i++ {
		tokens[string(chars[i:i+2])] = struct{}{}
	}
}

func lexicalOverlap(query, content map[string]struct{}) float64 {
	if len(query) == 0 {
		return 0
	}
	hits := 0
	for token := range query {
		if _, ok := content[token]; ok {
			hits++
		}
	}
	return float64(hits) / float64(len(query))
}

func candidateLexicalText(candidate Candidate) string {
	parts := []string{candidate.Content}
	if len(candidate.Metadata) == 0 {
		return strings.Join(parts, " ")
	}
	keys := make([]string, 0, len(candidate.Metadata))
	for key := range candidate.Metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts = append(parts, candidate.Metadata[key])
	}
	return strings.Join(parts, " ")
}

func attachCandidateFileNames(candidates []Candidate, names map[string]string) {
	if len(candidates) == 0 || len(names) == 0 {
		return
	}
	for i := range candidates {
		name := strings.TrimSpace(names[candidates[i].DocID])
		if name == "" {
			continue
		}
		if candidates[i].Metadata == nil {
			candidates[i].Metadata = map[string]string{}
		}
		if strings.TrimSpace(candidates[i].Metadata["file_name"]) == "" {
			candidates[i].Metadata["file_name"] = name
		}
	}
}

func candidateFileName(candidate Candidate) string {
	if len(candidate.Metadata) == 0 {
		return ""
	}
	return strings.TrimSpace(candidate.Metadata["file_name"])
}
