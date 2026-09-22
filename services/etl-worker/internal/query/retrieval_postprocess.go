package query

import (
	"strings"

	"ai-etl-pipeline/internal/retrieval"
)

// gateByRelevance drops candidates whose raw backend similarity falls below the
// floor, returning the kept candidates and the number dropped.
//
// Only Qdrant cosine scores are compared: BM25 is unbounded and corpus-dependent,
// so it shares no threshold with cosine. A candidate carrying a BM25-only
// relevance signal is kept — gating it on a cosine floor would reject valid
// keyword matches. Candidates with no relevance signal at all are also kept, so
// that a backend which stops reporting scores degrades to today's behaviour
// instead of silently refusing every query.
func gateByRelevance(candidates []retrieval.Candidate, minRelevance float64) ([]retrieval.Candidate, int) {
	if minRelevance <= 0 || len(candidates) == 0 {
		return candidates, 0
	}
	kept := make([]retrieval.Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.RelevanceSource == retrieval.SourceQdrant && c.Relevance < minRelevance {
			continue
		}
		kept = append(kept, c)
	}
	return kept, len(candidates) - len(kept)
}

func retrievalInfoFromResult(r retrieval.Result, candidates []retrieval.Candidate, role string, allowedPermissions []string) *RetrievalInfo {
	backends := make([]string, 0, 2)
	if r.Route.UseQdrant {
		backends = append(backends, "qdrant")
	}
	if r.Route.UseElastic {
		backends = append(backends, "elasticsearch")
	}
	maxRelevance := maxSourceRelevance(r.Sources)
	candidateCount := len(candidates)
	return &RetrievalInfo{
		Strategy:                   string(r.Route.Strategy),
		CacheHit:                   r.CacheHit,
		Backends:                   backends,
		BackendCandidateCounts:     r.BackendCandidateCounts,
		FusedCandidateCount:        r.FusedCandidateCount,
		DeduplicatedCandidateCount: r.DeduplicatedCandidateCount,
		StageDiagnostics:           r.StageDiagnostics,
		SelectedContextCount:       candidateCount,
		UniqueDocumentCount:        countUniqueDocuments(candidates),
		CandidateCount:             candidateCount,
		DurationMs:                 r.Duration.Milliseconds(),
		MaxRelevance:               maxRelevance,
		AllowedPermissions:         allowedPermissions,
		PermissionRole:             role,
		PartialErrors:              r.PartialErrors,
	}
}

func diagnosticRequiredDocIDs(enabled bool, ids []string) []string {
	if !enabled || len(ids) == 0 {
		return nil
	}
	// Keep this boundary aggregate-safe: retrieval diagnostics only need
	// document membership and should not retain caller-owned slices.
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func countUniqueDocuments(candidates []retrieval.Candidate) int {
	documents := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		documents[candidate.DocID] = struct{}{}
	}
	return len(documents)
}

// maxSourceRelevance returns the highest Qdrant cosine score among candidates.
func maxSourceRelevance(candidates []retrieval.Candidate) float64 {
	var maxRel float64
	for _, c := range candidates {
		if c.RelevanceSource == retrieval.SourceQdrant && c.Relevance > maxRel {
			maxRel = c.Relevance
		}
	}
	return maxRel
}
