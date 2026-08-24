package retrieval

import "strings"

// RequiredDocumentCoverage is an aggregate-only view of how many necessary
// documents reached one retrieval stage. It intentionally contains no document
// identifiers or candidate content so it is safe to expose in evaluation
// provenance and operational telemetry.
type RequiredDocumentCoverage struct {
	RequiredDocumentCount int  `json:"required_document_count"`
	HitDocumentCount      int  `json:"hit_document_count"`
	AllRequiredHit        bool `json:"all_required_hit"`
	AllRequiredMaxRank    int  `json:"all_required_max_rank"`
}

// StageDiagnostics records required-document coverage at each retrieval
// boundary. Backend keys are stable backend names; values contain counts only.
type StageDiagnostics struct {
	RequiredDocumentCount int                                 `json:"required_document_count"`
	Backend               map[string]RequiredDocumentCoverage `json:"backend,omitempty"`
	Fused                 RequiredDocumentCoverage            `json:"fused"`
	Selected              RequiredDocumentCoverage            `json:"selected"`
}

// DiagnoseStages compares required document ids against retrieval stages and
// returns aggregate counts. Required ids are deduplicated and blank values are
// ignored; candidate chunks from the same document count once per stage.
func DiagnoseStages(
	requiredDocIDs []string,
	backend map[string][]Candidate,
	fused []Candidate,
	selected []Candidate,
) StageDiagnostics {
	required := make(map[string]struct{}, len(requiredDocIDs))
	for _, docID := range requiredDocIDs {
		if value := strings.TrimSpace(docID); value != "" {
			required[value] = struct{}{}
		}
	}

	coverage := func(candidates []Candidate) RequiredDocumentCoverage {
		hit := make(map[string]int, len(required))
		for index, candidate := range candidates {
			docID := strings.TrimSpace(candidate.DocID)
			if _, ok := required[docID]; !ok {
				continue
			}
			if _, exists := hit[docID]; !exists {
				hit[docID] = index + 1
			}
		}
		allRequired := len(hit) == len(required)
		maxRank := 0
		if allRequired {
			for _, rank := range hit {
				if rank > maxRank {
					maxRank = rank
				}
			}
		}
		return RequiredDocumentCoverage{
			RequiredDocumentCount: len(required),
			HitDocumentCount:      len(hit),
			AllRequiredHit:        allRequired,
			AllRequiredMaxRank:    maxRank,
		}
	}

	backendCoverage := make(map[string]RequiredDocumentCoverage, len(backend))
	for name, candidates := range backend {
		backendCoverage[name] = coverage(candidates)
	}
	return StageDiagnostics{
		RequiredDocumentCount: len(required),
		Backend:               backendCoverage,
		Fused:                 coverage(fused),
		Selected:              coverage(selected),
	}
}
