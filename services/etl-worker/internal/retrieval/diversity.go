package retrieval

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

const defaultMaxChunksPerDocument = 2

type diversityStats struct {
	DeduplicatedCount   int
	UniqueDocumentCount int
}

// diversifyCandidates selects context in rank order while preventing repeated
// text and a single document from hiding useful alternatives. A second pass
// fills unused slots, so questions that genuinely need several chunks from one
// document still receive them when no other documents are available.
func diversifyCandidates(candidates []Candidate, topK, maxPerDoc int) ([]Candidate, diversityStats) {
	if topK <= 0 || topK > len(candidates) {
		topK = len(candidates)
	}
	if maxPerDoc <= 0 {
		maxPerDoc = defaultMaxChunksPerDocument
	}

	deduplicated := make([]Candidate, 0, len(candidates))
	seenContent := make(map[string]struct{}, len(candidates))
	canonicalDocByFile := make(map[string]string)
	for _, candidate := range candidates {
		if fileHash := strings.TrimSpace(candidate.Metadata["file_hash"]); fileHash != "" {
			canonicalDoc, exists := canonicalDocByFile[fileHash]
			if !exists {
				canonicalDocByFile[fileHash] = candidate.DocID
			} else if canonicalDoc != candidate.DocID {
				continue
			}
		}
		key := normalizedContentKey(candidate.Content)
		if key == "" {
			key = "chunk:" + candidateID(candidate)
		}
		if _, exists := seenContent[key]; exists {
			continue
		}
		seenContent[key] = struct{}{}
		deduplicated = append(deduplicated, candidate)
	}

	selected := make([]Candidate, 0, topK)
	deferred := make([]Candidate, 0)
	perDoc := make(map[string]int)
	availableDocs := make(map[string]struct{})
	for _, candidate := range deduplicated {
		availableDocs[candidate.DocID] = struct{}{}
	}
	for _, candidate := range deduplicated {
		if len(selected) >= topK {
			break
		}
		if perDoc[candidate.DocID] >= maxPerDoc {
			deferred = append(deferred, candidate)
			continue
		}
		selected = append(selected, candidate)
		perDoc[candidate.DocID]++
	}
	if len(availableDocs) == 1 {
		for _, candidate := range deferred {
			if len(selected) >= topK {
				break
			}
			selected = append(selected, candidate)
		}
	}
	for i := range selected {
		selected[i].Rank = i + 1
	}
	selectedDocs := make(map[string]struct{}, len(selected))
	for _, candidate := range selected {
		selectedDocs[candidate.DocID] = struct{}{}
	}

	return selected, diversityStats{
		DeduplicatedCount:   len(deduplicated),
		UniqueDocumentCount: len(selectedDocs),
	}
}

func normalizedContentKey(content string) string {
	return strings.Join(strings.Fields(norm.NFKC.String(content)), " ")
}
