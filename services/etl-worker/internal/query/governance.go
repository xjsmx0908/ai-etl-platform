package query

import (
	"context"
	"log/slog"
	"sort"
	"strings"

	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/retrieval"
)

// governanceOutcome is the result of applying corpus governance to a candidate
// set: retired documents removed, and any remaining disagreement disclosed.
type governanceOutcome struct {
	candidates      []retrieval.Candidate
	retiredFiltered int
	conflicts       []ConflictingDoc
	documents       map[string]docstore.Governance
}

// applyGovernance drops candidates whose document is no longer authoritative and
// reports whether the surviving evidence may be self-contradictory.
//
// Why this runs after retrieval rather than as a Qdrant/ES filter: chunk payloads
// are written once during ETL and never updated in place, so marking a document
// superseded cannot be reflected in the index without re-ingesting it. Filtering
// here costs a single batched registry query and keeps "obsolete" a pure registry
// concern.
//
// A registry error is non-fatal: it degrades to the pre-governance behaviour
// (answer from all candidates) rather than failing the query, since the
// permission filtering that actually guards confidentiality happens at the
// source and is unaffected.
func (s *Service) applyGovernance(ctx context.Context, tenantID string, candidates []retrieval.Candidate) governanceOutcome {
	outcome := governanceOutcome{candidates: candidates}
	if s.governance == nil || len(candidates) == 0 {
		return outcome
	}

	docIDs := uniqueDocIDs(candidates)
	governance, err := s.governance.GovernanceByDocIDs(ctx, tenantID, docIDs)
	if err != nil {
		slog.Warn("document governance lookup failed; answering without governance filtering",
			"tenant_id", tenantID, "doc_ids", len(docIDs), "error", err)
		return outcome
	}

	kept := make([]retrieval.Candidate, 0, len(candidates))
	for _, c := range candidates {
		// A doc_id absent from the registry is treated as active: reconciliation
		// backfills those rows, and refusing to cite them would silently shrink
		// the evidence set for documents that are perfectly valid.
		if g, ok := governance[c.DocID]; ok && g.IsRetired() {
			outcome.retiredFiltered++
			continue
		}
		kept = append(kept, c)
	}
	outcome.candidates = kept
	outcome.conflicts = detectConflicts(kept, governance)
	outcome.documents = governance
	return outcome
}

// detectConflicts reports documents in the evidence set that may disagree. The
// criteria are deliberately conservative and explainable — no LLM judgement:
//
//  1. supersedes chain: doc A declares it replaces doc B and both are cited. The
//     older one survived the retired filter (nobody marked it superseded), so the
//     disagreement is real and unresolved.
//  2. same file, different effective dates: two documents sharing a file name but
//     carrying different effective dates are competing versions.
//
// Returns nil when nothing is detected, so the caller can treat non-empty as
// "disclose to the user".
func detectConflicts(candidates []retrieval.Candidate, governance map[string]docstore.Governance) []ConflictingDoc {
	if len(candidates) == 0 || len(governance) == 0 {
		return nil
	}

	cited := map[string]docstore.Governance{}
	for _, c := range candidates {
		if g, ok := governance[c.DocID]; ok {
			cited[c.DocID] = g
		}
	}
	if len(cited) < 2 {
		return nil
	}

	involved := map[string]bool{}
	for docID, g := range cited {
		// (1) supersedes chain within the cited set.
		if other := strings.TrimSpace(g.Supersedes); other != "" {
			if _, alsoCited := cited[other]; alsoCited {
				involved[docID] = true
				involved[other] = true
			}
		}
	}

	// (2) same file name, differing effective dates.
	byFileName := map[string][]docstore.Governance{}
	for _, g := range cited {
		name := strings.TrimSpace(strings.ToLower(g.FileName))
		if name == "" {
			continue
		}
		byFileName[name] = append(byFileName[name], g)
	}
	for _, group := range byFileName {
		if len(group) < 2 {
			continue
		}
		dates := map[string]bool{}
		for _, g := range group {
			dates[effectiveDateString(g)] = true
		}
		// Identical (or uniformly untracked) dates are not evidence of a conflict —
		// that is just the same document cited through several chunks.
		if len(dates) < 2 {
			continue
		}
		for _, g := range group {
			involved[g.DocID] = true
		}
	}

	if len(involved) == 0 {
		return nil
	}
	out := make([]ConflictingDoc, 0, len(involved))
	for docID := range involved {
		g := cited[docID]
		out = append(out, ConflictingDoc{
			DocID:         docID,
			FileName:      g.FileName,
			EffectiveDate: effectiveDateString(g),
			Supersedes:    g.Supersedes,
		})
	}
	// Stable order so the response (and its tests) do not depend on map iteration.
	sort.Slice(out, func(i, j int) bool { return out[i].DocID < out[j].DocID })
	return out
}

// annotate copies the governance outcome onto a RetrievalInfo. Applied to every
// response path (including refusals) so a shrunken or contradictory evidence set
// is always visible to the caller, not just on the happy path.
func (o governanceOutcome) annotate(info *RetrievalInfo) *RetrievalInfo {
	if info == nil {
		return info
	}
	info.RetiredFiltered = o.retiredFiltered
	info.ConflictingDocs = o.conflicts
	info.ConflictDetected = len(o.conflicts) > 0
	return info
}

func effectiveDateString(g docstore.Governance) string {
	if g.EffectiveDate.IsZero() {
		return ""
	}
	return g.EffectiveDate.UTC().Format("2006-01-02")
}

func uniqueDocIDs(candidates []retrieval.Candidate) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if c.DocID == "" || seen[c.DocID] {
			continue
		}
		seen[c.DocID] = true
		out = append(out, c.DocID)
	}
	return out
}
