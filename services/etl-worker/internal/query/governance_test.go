package query

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/retrieval"
)

// fakeGovernance serves a canned governance map and can simulate a registry
// outage.
type fakeGovernance struct {
	rows  map[string]docstore.Governance
	err   error
	calls int
}

func (f *fakeGovernance) GovernanceByDocIDs(_ context.Context, _ string, docIDs []string) (map[string]docstore.Governance, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]docstore.Governance{}
	for _, id := range docIDs {
		if g, ok := f.rows[id]; ok {
			out[id] = g
		}
	}
	return out, nil
}

func candidates(docIDs ...string) []retrieval.Candidate {
	out := make([]retrieval.Candidate, 0, len(docIDs))
	for i, id := range docIDs {
		out = append(out, retrieval.Candidate{ChunkID: id + "#0", DocID: id, Rank: i + 1})
	}
	return out
}

func docIDsOf(cs []retrieval.Candidate) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.DocID)
	}
	return out
}

func testService(g governanceLookup) *Service {
	s := NewService(config.Config{})
	if g != nil {
		s.WithGovernance(g)
	}
	return s
}

func TestApplyGovernance_DropsRetiredCandidates(t *testing.T) {
	gov := &fakeGovernance{rows: map[string]docstore.Governance{
		"doc-active":     {DocID: "doc-active", DocStatus: docstore.DocStatusActive},
		"doc-superseded": {DocID: "doc-superseded", DocStatus: docstore.DocStatusSuperseded},
		"doc-archived":   {DocID: "doc-archived", DocStatus: docstore.DocStatusArchived},
	}}
	s := testService(gov)

	out := s.applyGovernance(context.Background(), "acme",
		candidates("doc-active", "doc-superseded", "doc-archived"))

	if got := docIDsOf(out.candidates); len(got) != 1 || got[0] != "doc-active" {
		t.Fatalf("expected only doc-active to survive, got %v", got)
	}
	if out.retiredFiltered != 2 {
		t.Fatalf("expected 2 retired candidates counted, got %d", out.retiredFiltered)
	}
	if gov.calls != 1 {
		t.Fatalf("expected one batched lookup, got %d", gov.calls)
	}
}

// A doc_id with no registry row is cited normally: reconciliation backfills such
// rows, and dropping them would silently shrink the evidence set.
func TestApplyGovernance_UnknownDocIDTreatedActive(t *testing.T) {
	s := testService(&fakeGovernance{rows: map[string]docstore.Governance{}})

	out := s.applyGovernance(context.Background(), "acme", candidates("doc-1", "doc-2"))

	if len(out.candidates) != 2 || out.retiredFiltered != 0 {
		t.Fatalf("expected both candidates kept, got %v retired=%d", docIDsOf(out.candidates), out.retiredFiltered)
	}
}

// Governance is not the confidentiality control (that filters at the source), so
// a registry failure degrades to pre-governance behaviour instead of failing the
// query or dropping evidence.
func TestApplyGovernance_RegistryErrorKeepsAllCandidates(t *testing.T) {
	s := testService(&fakeGovernance{err: errors.New("registry down")})

	out := s.applyGovernance(context.Background(), "acme", candidates("doc-1", "doc-2"))

	if len(out.candidates) != 2 {
		t.Fatalf("expected all candidates kept on registry error, got %v", docIDsOf(out.candidates))
	}
	if out.retiredFiltered != 0 || out.conflicts != nil {
		t.Fatalf("registry error must report no governance findings, got retired=%d conflicts=%+v",
			out.retiredFiltered, out.conflicts)
	}
}

// Without the registry wired in (worker, tests), retrieval must behave exactly as
// it did before governance existed.
func TestApplyGovernance_NilGovernanceIsNoOp(t *testing.T) {
	s := testService(nil)
	in := candidates("doc-1", "doc-2")

	out := s.applyGovernance(context.Background(), "acme", in)

	if len(out.candidates) != 2 || out.retiredFiltered != 0 || out.conflicts != nil {
		t.Fatalf("nil governance must not change anything, got %+v", out)
	}
}

func TestDetectConflicts_SupersedesChainWithinEvidence(t *testing.T) {
	rows := map[string]docstore.Governance{
		"policy-v2": {DocID: "policy-v2", FileName: "差旅政策.docx", Supersedes: "policy-v1"},
		"policy-v1": {DocID: "policy-v1", FileName: "差旅政策-2023.docx"},
	}

	conflicts := detectConflicts(candidates("policy-v2", "policy-v1"), rows)

	if len(conflicts) != 2 {
		t.Fatalf("expected both sides of the chain disclosed, got %+v", conflicts)
	}
	// Sorted by doc_id for a deterministic response.
	if conflicts[0].DocID != "policy-v1" || conflicts[1].DocID != "policy-v2" {
		t.Fatalf("expected deterministic doc_id order, got %+v", conflicts)
	}
	if conflicts[1].Supersedes != "policy-v1" {
		t.Fatalf("expected supersedes link reported, got %+v", conflicts[1])
	}
}

// The superseded document must not be cited alongside its replacement, so when
// the chain is declared AND the old version is marked superseded, the filter
// removes it and there is nothing left to disclose.
func TestApplyGovernance_MarkedSupersededLeavesNoConflict(t *testing.T) {
	s := testService(&fakeGovernance{rows: map[string]docstore.Governance{
		"policy-v2": {DocID: "policy-v2", DocStatus: docstore.DocStatusActive, Supersedes: "policy-v1"},
		"policy-v1": {DocID: "policy-v1", DocStatus: docstore.DocStatusSuperseded},
	}})

	out := s.applyGovernance(context.Background(), "acme", candidates("policy-v2", "policy-v1"))

	if got := docIDsOf(out.candidates); len(got) != 1 || got[0] != "policy-v2" {
		t.Fatalf("expected only the current version, got %v", got)
	}
	if out.conflicts != nil {
		t.Fatalf("resolved supersession is not a conflict, got %+v", out.conflicts)
	}
}

func TestDetectConflicts_SameFileDifferentEffectiveDates(t *testing.T) {
	rows := map[string]docstore.Governance{
		"doc-a": {DocID: "doc-a", FileName: "报销标准.xlsx",
			EffectiveDate: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)},
		"doc-b": {DocID: "doc-b", FileName: "报销标准.xlsx",
			EffectiveDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}

	conflicts := detectConflicts(candidates("doc-a", "doc-b"), rows)

	if len(conflicts) != 2 {
		t.Fatalf("expected both versions disclosed, got %+v", conflicts)
	}
	if conflicts[0].EffectiveDate != "2025-01-01" || conflicts[1].EffectiveDate != "2026-01-01" {
		t.Fatalf("expected effective dates rendered for human adjudication, got %+v", conflicts)
	}
}

// Same file name and same (or uniformly untracked) date is one document cited
// through several chunks — not a conflict.
func TestDetectConflicts_SameFileSameDateIsNotConflict(t *testing.T) {
	effective := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rows := map[string]docstore.Governance{
		"doc-a": {DocID: "doc-a", FileName: "手册.docx", EffectiveDate: effective},
		"doc-b": {DocID: "doc-b", FileName: "手册.docx", EffectiveDate: effective},
		"doc-c": {DocID: "doc-c", FileName: "其它.docx"},
	}

	if conflicts := detectConflicts(candidates("doc-a", "doc-b", "doc-c"), rows); conflicts != nil {
		t.Fatalf("expected no conflict, got %+v", conflicts)
	}
}

// A supersedes pointer to a document that is NOT in the evidence set is not a
// conflict: nothing in this answer disagrees with anything else in it.
func TestDetectConflicts_SupersedesOutsideEvidenceIgnored(t *testing.T) {
	rows := map[string]docstore.Governance{
		"policy-v2": {DocID: "policy-v2", FileName: "a.docx", Supersedes: "policy-v1"},
		"unrelated": {DocID: "unrelated", FileName: "b.docx"},
	}

	if conflicts := detectConflicts(candidates("policy-v2", "unrelated"), rows); conflicts != nil {
		t.Fatalf("expected no conflict, got %+v", conflicts)
	}
}

func TestGovernanceOutcome_Annotate(t *testing.T) {
	out := governanceOutcome{
		retiredFiltered: 3,
		conflicts:       []ConflictingDoc{{DocID: "doc-a"}, {DocID: "doc-b"}},
	}
	info := out.annotate(&RetrievalInfo{})

	if info.RetiredFiltered != 3 {
		t.Errorf("expected retired count surfaced, got %d", info.RetiredFiltered)
	}
	if !info.ConflictDetected || len(info.ConflictingDocs) != 2 {
		t.Errorf("expected conflict disclosed, got %+v", info)
	}

	clean := governanceOutcome{}.annotate(&RetrievalInfo{})
	if clean.ConflictDetected || clean.RetiredFiltered != 0 {
		t.Errorf("clean outcome must not flag anything, got %+v", clean)
	}
	if nilInfo := (governanceOutcome{}).annotate(nil); nilInfo != nil {
		t.Error("annotate(nil) must stay nil")
	}
}

func TestUniqueDocIDs(t *testing.T) {
	in := []retrieval.Candidate{
		{DocID: "a"}, {DocID: "b"}, {DocID: "a"}, {DocID: ""}, {DocID: "c"},
	}
	got := uniqueDocIDs(in)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v (order preserved), got %v", want, got)
		}
	}
}
