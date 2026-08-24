package main

import (
	"encoding/json"
	"errors"
	"net/http"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/knowledgecatalog"
)

func handleKnowledgeSpaces(catalog *knowledgecatalog.Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal := knowledgecatalog.Principal{
			TenantID: auth.GetTenantID(r.Context()),
			UserID:   auth.GetUserID(r.Context()),
			Role:     auth.GetPermission(r.Context()),
		}
		switch r.Method {
		case http.MethodGet:
			spaces, err := catalog.List(r.Context(), principal)
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, "knowledge catalog unavailable")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": spaces})
		case http.MethodPost:
			var input struct {
				ID   string                     `json:"id"`
				Name string                     `json:"name"`
				Kind knowledgecatalog.SpaceKind `json:"kind"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body")
				return
			}
			space, err := catalog.Create(r.Context(), principal, knowledgecatalog.Space{ID: input.ID, Name: input.Name, Kind: input.Kind})
			switch {
			case err == nil:
				writeJSON(w, http.StatusCreated, space)
			case errors.Is(err, knowledgecatalog.ErrForbidden):
				writeError(w, http.StatusForbidden, "admin role required")
			case errors.Is(err, knowledgecatalog.ErrConflict):
				writeError(w, http.StatusConflict, "knowledge space already exists")
			case errors.Is(err, knowledgecatalog.ErrNotFound):
				writeError(w, http.StatusBadRequest, "invalid knowledge space")
			default:
				writeError(w, http.StatusServiceUnavailable, "knowledge catalog unavailable")
			}
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}
