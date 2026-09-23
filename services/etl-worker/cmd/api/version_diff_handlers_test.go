package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/indexmanifest"
	"ai-etl-pipeline/internal/publicationworkflow"
)

type stubReleasePairs struct {
	pair  publicationworkflow.ReleasePair
	found bool
	err   error
}

func (s stubReleasePairs) CurrentReleasePair(context.Context, string, string) (publicationworkflow.ReleasePair, bool, error) {
	return s.pair, s.found, s.err
}

type stubGenerationChunks struct {
	byGeneration map[string][]indexmanifest.ChunkIdentity
	err          error
}

func (s stubGenerationChunks) ListGenerationChunks(_ context.Context, identity indexmanifest.GenerationIdentity) ([]indexmanifest.ChunkIdentity, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.byGeneration[identity.GenerationID], nil
}

func versionDiffRequest(docID string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/v1/documents/"+docID+"/version-diff", nil)
	request = request.WithContext(context.WithValue(request.Context(), auth.CtxTenantID, "acme"))
	request.SetPathValue("docID", docID)
	return request
}

func decodeVersionDiff(t *testing.T, recorder *httptest.ResponseRecorder) versionDiffResponse {
	t.Helper()
	var body versionDiffResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return body
}

// TestVersionDiffReportsAReclaimedPredecessorSeparately covers the case that
// would otherwise read as "everything is new". Retention deletes a retired
// generation's blocks and nulls its manifest row, so the previous version is
// still nameable while its blocks are gone -- and a handler that treated that
// empty read as a real predecessor would tell the reviewer the whole document
// was rewritten.
func TestVersionDiffReportsAReclaimedPredecessorSeparately(t *testing.T) {
	handler := handleDocumentVersionDiff(
		stubReleasePairs{found: true, pair: publicationworkflow.ReleasePair{
			PublishedVersionID: "job-2", PublishedGenerationID: "gen-2",
			PreviousVersionID: "job-1", PreviousGenerationID: "",
		}},
		stubGenerationChunks{byGeneration: map[string][]indexmanifest.ChunkIdentity{
			"gen-2": {{ChunkID: "c0", Index: 0, ContentHash: "sha256:a"}},
		}},
	)
	recorder := httptest.NewRecorder()
	handler(recorder, versionDiffRequest("policy-1"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	body := decodeVersionDiff(t, recorder)
	if body.PreviousAvailable {
		t.Fatal("previous_available = true for a reclaimed predecessor")
	}
	if !strings.Contains(body.Reason, "reclaimed") {
		t.Fatalf("reason = %q, want it to say the predecessor was reclaimed", body.Reason)
	}
	if body.Previous.DocumentVersionID != "job-1" {
		t.Fatalf("previous version = %q, want job-1 named even though its blocks are gone", body.Previous.DocumentVersionID)
	}
	if len(body.Changes) != 0 {
		t.Fatalf("changes = %v, want none: there was nothing to compare against", body.Changes)
	}
}

// TestVersionDiffReportsAFirstReleaseSeparately is the other half of the pair:
// "nothing came before this" is not the same fact as "the predecessor was
// reclaimed", and a caller that showed one message for both would be wrong half
// the time.
func TestVersionDiffReportsAFirstReleaseSeparately(t *testing.T) {
	handler := handleDocumentVersionDiff(
		stubReleasePairs{found: true, pair: publicationworkflow.ReleasePair{
			PublishedVersionID: "job-1", PublishedGenerationID: "gen-1",
		}},
		stubGenerationChunks{},
	)
	recorder := httptest.NewRecorder()
	handler(recorder, versionDiffRequest("policy-1"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	body := decodeVersionDiff(t, recorder)
	if body.PreviousAvailable {
		t.Fatal("previous_available = true for a first release")
	}
	if !strings.Contains(body.Reason, "first published version") {
		t.Fatalf("reason = %q, want it to say this is the first published version", body.Reason)
	}
}

func TestVersionDiffReportsWhatChanged(t *testing.T) {
	handler := handleDocumentVersionDiff(
		stubReleasePairs{found: true, pair: publicationworkflow.ReleasePair{
			PublishedVersionID: "job-2", PublishedGenerationID: "gen-2",
			PreviousVersionID: "job-1", PreviousGenerationID: "gen-1",
		}},
		stubGenerationChunks{byGeneration: map[string][]indexmanifest.ChunkIdentity{
			"gen-1": {
				{ChunkID: "a", Index: 0, ContentHash: "sha256:a"},
				{ChunkID: "b", Index: 1, ContentHash: "sha256:b"},
			},
			"gen-2": {
				{ChunkID: "a", Index: 0, ContentHash: "sha256:a"},
				{ChunkID: "x", Index: 1, ContentHash: "sha256:x"},
				{ChunkID: "b", Index: 2, ContentHash: "sha256:b"},
			},
		}},
	)
	recorder := httptest.NewRecorder()
	handler(recorder, versionDiffRequest("policy-1"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	body := decodeVersionDiff(t, recorder)
	if !body.PreviousAvailable {
		t.Fatalf("previous_available = false, reason %q", body.Reason)
	}
	if body.Previous.Chunks != 2 || body.Current.Chunks != 3 {
		t.Fatalf("chunks previous=%d current=%d, want 2 and 3", body.Previous.Chunks, body.Current.Chunks)
	}
	if body.Counts["added"] != 1 {
		t.Fatalf("added = %d, want 1 (the inserted block)", body.Counts["added"])
	}
	if body.Counts["modified"] != 0 {
		t.Fatalf("modified = %d, want 0: the insertion must not read as a rewrite", body.Counts["modified"])
	}
	if body.Counts["moved"] != 1 {
		t.Fatalf("moved = %d, want 1 (the block pushed down by the insertion)", body.Counts["moved"])
	}
	if body.Unchanged != 1 {
		t.Fatalf("unchanged = %d, want 1", body.Unchanged)
	}
	for _, change := range body.Changes {
		if change.Kind != "moved" {
			continue
		}
		if change.PreviousIndex == nil {
			t.Fatal("a moved change came back without previous_index")
		}
		if *change.PreviousIndex != 1 {
			t.Fatalf("previous_index = %d, want 1", *change.PreviousIndex)
		}
	}
}

func TestVersionDiffRefusesADocumentWithNoPublishedVersion(t *testing.T) {
	handler := handleDocumentVersionDiff(stubReleasePairs{found: true}, stubGenerationChunks{})
	recorder := httptest.NewRecorder()
	handler(recorder, versionDiffRequest("policy-1"))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", recorder.Code)
	}
}

func TestVersionDiffReportsAReadFailureInsteadOfAnEmptyComparison(t *testing.T) {
	handler := handleDocumentVersionDiff(
		stubReleasePairs{found: true, pair: publicationworkflow.ReleasePair{
			PublishedVersionID: "job-2", PublishedGenerationID: "gen-2",
			PreviousVersionID: "job-1", PreviousGenerationID: "gen-1",
		}},
		stubGenerationChunks{err: errors.New("projection unreachable")},
	)
	recorder := httptest.NewRecorder()
	handler(recorder, versionDiffRequest("policy-1"))

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: an unreadable projection is not an empty version", recorder.Code)
	}
}

// TestVersionDiffTreatsAnEmptyPredecessorAsReclaimed covers the window retention
// opens: it deletes a generation's projections before it deletes the manifest
// row, so for a moment the row points at blocks that are already gone. A
// published version always has blocks (publication requires a non-zero chunk
// count), so an empty read cannot mean "the previous version was empty" -- and
// reading it as one would report the whole document as newly added.
func TestVersionDiffTreatsAnEmptyPredecessorAsReclaimed(t *testing.T) {
	handler := handleDocumentVersionDiff(
		stubReleasePairs{found: true, pair: publicationworkflow.ReleasePair{
			PublishedVersionID: "job-2", PublishedGenerationID: "gen-2",
			PreviousVersionID: "job-1", PreviousGenerationID: "gen-1",
		}},
		stubGenerationChunks{byGeneration: map[string][]indexmanifest.ChunkIdentity{
			"gen-2": {{ChunkID: "c0", Index: 0, ContentHash: "sha256:a"}},
		}},
	)
	recorder := httptest.NewRecorder()
	handler(recorder, versionDiffRequest("policy-1"))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	body := decodeVersionDiff(t, recorder)
	if body.PreviousAvailable {
		t.Fatal("previous_available = true with no blocks to compare against")
	}
	if !strings.Contains(body.Reason, "reclaimed") {
		t.Fatalf("reason = %q, want it to say the predecessor was reclaimed", body.Reason)
	}
	if len(body.Changes) != 0 {
		t.Fatalf("changes = %v, want none: reporting the whole document as added would be a false alarm", body.Changes)
	}
}

func TestVersionDiffIsUnavailableWithoutAProjection(t *testing.T) {
	handler := handleDocumentVersionDiff(stubReleasePairs{}, nil)
	recorder := httptest.NewRecorder()
	handler(recorder, versionDiffRequest("policy-1"))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
}

func TestVersionDiffRejectsNonGet(t *testing.T) {
	handler := handleDocumentVersionDiff(stubReleasePairs{}, stubGenerationChunks{})
	request := httptest.NewRequest(http.MethodPost, "/v1/documents/policy-1/version-diff", nil)
	request.SetPathValue("docID", "policy-1")
	recorder := httptest.NewRecorder()
	handler(recorder, request)

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
}
