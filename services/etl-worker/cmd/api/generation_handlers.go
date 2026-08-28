package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"ai-etl-pipeline/internal/audit"
	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/indexmanifest"
	"ai-etl-pipeline/internal/query"
)

type generationRollbacker interface {
	Rollback(context.Context, indexmanifest.RollbackRequest) error
}

func handleGenerationRollback(rollbacker generationRollbacker, qs *query.Service, audits audit.Store, window time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if auth.GetPermission(r.Context()) != "admin" || !hasScope(auth.GetScopes(r.Context()), auth.ScopeAdmin) {
			writeError(w, http.StatusForbidden, "admin role required")
			return
		}
		if rollbacker == nil || window <= 0 {
			writeError(w, http.StatusServiceUnavailable, "generation rollback unavailable")
			return
		}
		var input struct {
			DocumentID                 string `json:"document_id"`
			DocumentVersionID          string `json:"document_version_id"`
			TargetGenerationID         string `json:"target_generation_id"`
			ExpectedActiveGenerationID string `json:"expected_active_generation_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		request := indexmanifest.RollbackRequest{
			Version: indexmanifest.VersionIdentity{
				TenantID: auth.GetTenantID(r.Context()), DocumentID: strings.TrimSpace(input.DocumentID),
				DocumentVersionID: strings.TrimSpace(input.DocumentVersionID),
			},
			TargetGenerationID:         strings.TrimSpace(input.TargetGenerationID),
			ExpectedActiveGenerationID: strings.TrimSpace(input.ExpectedActiveGenerationID),
			Window:                     window,
		}
		if request.Version.TenantID == "" || request.Version.DocumentID == "" || request.Version.DocumentVersionID == "" ||
			request.TargetGenerationID == "" || request.ExpectedActiveGenerationID == "" ||
			request.TargetGenerationID == request.ExpectedActiveGenerationID {
			writeError(w, http.StatusBadRequest, "complete and distinct generation identities are required")
			return
		}
		if err := rollbacker.Rollback(r.Context(), request); err != nil {
			switch {
			case errors.Is(err, indexmanifest.ErrInvalidManifest):
				writeError(w, http.StatusBadRequest, "invalid rollback request")
			case errors.Is(err, indexmanifest.ErrConflict), errors.Is(err, indexmanifest.ErrNotReady):
				writeError(w, http.StatusConflict, "generation cannot be rolled back")
			default:
				writeError(w, http.StatusServiceUnavailable, "generation rollback failed")
			}
			return
		}
		if qs != nil {
			_ = qs.InvalidateSemanticCache(r.Context())
		}
		recordAudit(r.Context(), audits, audit.Entry{
			TenantID: request.Version.TenantID, ActorUserID: auth.GetUserID(r.Context()), ActorRole: "admin",
			Action: "index_generation.rollback", ResourceType: "index_generation", ResourceID: request.TargetGenerationID,
			Result: audit.ResultSuccess, Detail: map[string]any{
				"document_id": request.Version.DocumentID, "document_version_id": request.Version.DocumentVersionID,
				"previous_active_generation_id": request.ExpectedActiveGenerationID,
			},
		})
		writeJSON(w, http.StatusOK, map[string]string{"active_generation_id": request.TargetGenerationID})
	}
}
