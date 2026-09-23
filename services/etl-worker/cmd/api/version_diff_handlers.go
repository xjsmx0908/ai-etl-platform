package main

import (
	"context"
	"log/slog"
	"net/http"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/indexmanifest"
	"ai-etl-pipeline/internal/publicationworkflow"
	"ai-etl-pipeline/internal/versiondiff"
)

// releasePairReader is the slice of the publication store this handler needs.
type releasePairReader interface {
	CurrentReleasePair(context.Context, string, string) (publicationworkflow.ReleasePair, bool, error)
}

// generationChunkLister is the slice of the keyword projection this handler
// needs. It returns identities, not text: what changed is decided by content
// hash, and the text is not needed to decide it.
type generationChunkLister interface {
	ListGenerationChunks(context.Context, indexmanifest.GenerationIdentity) ([]indexmanifest.ChunkIdentity, error)
}

type versionDiffSide struct {
	DocumentVersionID string `json:"document_version_id"`
	GenerationID      string `json:"generation_id"`
	Chunks            int    `json:"chunks"`
}

type versionDiffChange struct {
	Kind        string `json:"kind"`
	ChunkID     string `json:"chunk_id"`
	ContentHash string `json:"content_hash"`
	Index       int    `json:"index"`
	// PreviousIndex is set only for a moved block. A pointer rather than an
	// int-with-omitempty, because index 0 is a real position and would otherwise
	// be reported as "did not move".
	PreviousIndex *int `json:"previous_index,omitempty"`
}

type versionDiffResponse struct {
	DocID             string              `json:"doc_id"`
	PreviousAvailable bool                `json:"previous_available"`
	Reason            string              `json:"reason,omitempty"`
	Previous          versionDiffSide     `json:"previous"`
	Current           versionDiffSide     `json:"current"`
	Unchanged         int                 `json:"unchanged"`
	Counts            map[string]int      `json:"counts,omitempty"`
	Changes           []versionDiffChange `json:"changes,omitempty"`
}

// handleDocumentVersionDiff reports what changed between the published version of
// a document and the version it replaced.
//
// This is the deterministic half of "what did this release change": it reads two
// sets of content hashes and reports their difference. It deliberately does not
// ask a model, so the answer is reproducible and a reviewer can recompute it --
// which is what makes it evidence rather than an opinion.
//
// The three ways of having nothing to compare are reported separately. Collapsing
// them would turn "we cannot compare" into "nothing changed" or into "everything
// is new":
//   - no published version      -> 409; there is no current version to compare from
//   - first published version   -> 200, previous_available=false, reason names it
//   - predecessor reclaimed     -> 200, previous_available=false, reason names the version
func handleDocumentVersionDiff(releases releasePairReader, chunks generationChunkLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if chunks == nil {
			// The keyword projection is wired at startup. When it is missing the
			// route is still registered, so it has to answer rather than panic.
			writeError(w, http.StatusServiceUnavailable, "version diff is unavailable")
			return
		}
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		docID := r.PathValue("docID")
		if docID == "" {
			writeError(w, http.StatusBadRequest, "doc_id is required")
			return
		}
		tenantID := auth.GetTenantID(r.Context())
		if tenantID == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		pair, found, err := releases.CurrentReleasePair(r.Context(), tenantID, docID)
		if err != nil {
			slog.Error("version diff release lookup failed", "doc_id", docID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !found || pair.PublishedGenerationID == "" {
			writeError(w, http.StatusConflict, "document has no published version to compare")
			return
		}

		current, err := chunks.ListGenerationChunks(r.Context(), generationIdentity(tenantID, docID, pair.PublishedVersionID, pair.PublishedGenerationID))
		if err != nil {
			slog.Error("version diff could not read the published version", "doc_id", docID, "error", err)
			writeError(w, http.StatusBadGateway, "could not read the published version's blocks")
			return
		}

		response := versionDiffResponse{
			DocID:   docID,
			Current: versionDiffSide{DocumentVersionID: pair.PublishedVersionID, GenerationID: pair.PublishedGenerationID, Chunks: len(current)},
			Previous: versionDiffSide{
				DocumentVersionID: pair.PreviousVersionID,
				GenerationID:      pair.PreviousGenerationID,
			},
		}
		if pair.PreviousGenerationID == "" {
			response.Reason = noComparisonReason(pair.PreviousVersionID)
			writeJSON(w, http.StatusOK, response)
			return
		}

		previous, err := chunks.ListGenerationChunks(r.Context(), generationIdentity(tenantID, docID, pair.PreviousVersionID, pair.PreviousGenerationID))
		if err != nil {
			slog.Error("version diff could not read the previous version", "doc_id", docID, "error", err)
			writeError(w, http.StatusBadGateway, "could not read the previous version's blocks")
			return
		}
		response.Previous.Chunks = len(previous)
		if len(previous) == 0 {
			// A published version always has blocks: publication requires
			// expected_chunk_count > 0. So an empty read here means the blocks are
			// gone, not that the previous version was empty -- retention deletes a
			// generation's projections before it deletes the manifest row, and this
			// is exactly that window. Reporting it as an empty predecessor would
			// turn "reclaimed" into "everything is new", which is the one wrong
			// answer that matters here.
			response.Reason = noComparisonReason(pair.PreviousVersionID)
			writeJSON(w, http.StatusOK, response)
			return
		}

		result, err := versiondiff.Compare(toDiffChunks(previous), toDiffChunks(current))
		if err != nil {
			slog.Error("version diff comparison failed", "doc_id", docID, "error", err)
			writeError(w, http.StatusInternalServerError, "could not compare the two versions")
			return
		}
		response.PreviousAvailable = true
		response.Unchanged = result.Unchanged
		response.Counts = map[string]int{}
		for kind, count := range result.Counts() {
			response.Counts[string(kind)] = count
		}
		response.Changes = make([]versionDiffChange, 0, len(result.Changes))
		for _, change := range result.Changes {
			response.Changes = append(response.Changes, toDiffChange(change))
		}
		writeJSON(w, http.StatusOK, response)
	}
}

// noComparisonReason separates "there is nothing before this" from "what was
// before this can no longer be read". Both leave previous_available false, and a
// caller that read either as "no changes" would be reporting the opposite of
// what happened.
func noComparisonReason(previousVersionID string) string {
	if previousVersionID == "" {
		return "this is the document's first published version"
	}
	return "the previous version's index has been reclaimed, so its blocks can no longer be read"
}

func generationIdentity(tenantID, docID, versionID, generationID string) indexmanifest.GenerationIdentity {
	return indexmanifest.GenerationIdentity{
		VersionIdentity: indexmanifest.VersionIdentity{
			TenantID: tenantID, DocumentID: docID, DocumentVersionID: versionID,
		},
		GenerationID: generationID,
	}
}

func toDiffChunks(identities []indexmanifest.ChunkIdentity) []versiondiff.Chunk {
	chunks := make([]versiondiff.Chunk, len(identities))
	for i, identity := range identities {
		chunks[i] = versiondiff.Chunk{ChunkID: identity.ChunkID, Index: identity.Index, ContentHash: identity.ContentHash}
	}
	return chunks
}

func toDiffChange(change versiondiff.Change) versionDiffChange {
	out := versionDiffChange{
		Kind:        string(change.Kind),
		ChunkID:     change.ChunkID,
		ContentHash: change.ContentHash,
		Index:       change.Index,
	}
	if change.Kind == versiondiff.KindMoved {
		previous := change.PreviousIndex
		out.PreviousIndex = &previous
	}
	return out
}
