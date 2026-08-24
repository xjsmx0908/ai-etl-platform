package query

import (
	"strings"

	"ai-etl-pipeline/internal/retrieval"
)

type scopeDecision struct {
	KnowledgeBaseID string
	ApplicableScope string
	Filtered        int
	Ambiguous       bool
}

// isolateCandidatesByScope prevents an unqualified query from synthesizing
// across unrelated knowledge spaces. Rank one selects the scope; candidates in
// other scopes are disclosed as filtered. Callers can avoid auto-selection by
// sending an explicit knowledge_base_id/applicable_scope retrieval filter.
func isolateCandidatesByScope(candidates []retrieval.Candidate) ([]retrieval.Candidate, scopeDecision) {
	if len(candidates) == 0 {
		return candidates, scopeDecision{}
	}
	selectedKB, selectedScope := candidateScope(candidates[0])
	decision := scopeDecision{KnowledgeBaseID: selectedKB, ApplicableScope: selectedScope}
	kept := make([]retrieval.Candidate, 0, len(candidates))
	seenScopes := make(map[string]struct{})
	for _, candidate := range candidates {
		kb, scope := candidateScope(candidate)
		seenScopes[kb+"\x00"+scope] = struct{}{}
		if kb != selectedKB || scope != selectedScope {
			decision.Filtered++
			continue
		}
		kept = append(kept, candidate)
	}
	decision.Ambiguous = len(seenScopes) > 1
	return kept, decision
}

func candidateScope(candidate retrieval.Candidate) (string, string) {
	kb := strings.TrimSpace(candidate.Metadata["knowledge_base_id"])
	scope := strings.TrimSpace(candidate.Metadata["applicable_scope"])
	if kb == "" {
		kb = "legacy"
	}
	if scope == "" {
		scope = "unspecified"
	}
	return kb, scope
}

func (d scopeDecision) annotate(info *RetrievalInfo) *RetrievalInfo {
	if info == nil {
		return nil
	}
	info.SelectedKnowledgeBaseID = d.KnowledgeBaseID
	info.SelectedApplicableScope = d.ApplicableScope
	info.CrossScopeFiltered = d.Filtered
	info.ScopeAmbiguous = d.Ambiguous
	return info
}
