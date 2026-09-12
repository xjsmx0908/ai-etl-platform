package main

import (
	"errors"
	"net/http"
	"strings"

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
				ID      string                     `json:"id"`
				Name    string                     `json:"name"`
				Kind    knowledgecatalog.SpaceKind `json:"kind"`
				Purpose string                     `json:"purpose"`
			}
			if !decodeJSONBody(w, r, &input, defaultJSONBodyBytes, "invalid request body") {
				return
			}
			space, err := catalog.Create(r.Context(), principal, knowledgecatalog.Space{ID: input.ID, Name: input.Name, Kind: input.Kind, Purpose: input.Purpose})
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

func handleKnowledgeSpace(catalog *knowledgecatalog.Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal := knowledgecatalog.Principal{
			TenantID: auth.GetTenantID(r.Context()),
			UserID:   auth.GetUserID(r.Context()),
			Role:     auth.GetPermission(r.Context()),
		}
		spaceID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/knowledge-spaces/"), "/")
		if spaceID == "" || strings.Contains(spaceID, "/") {
			writeError(w, http.StatusBadRequest, "invalid knowledge space")
			return
		}
		if r.Method != http.MethodPatch {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var input struct {
			Purpose string `json:"purpose"`
		}
		if !decodeJSONBody(w, r, &input, defaultJSONBodyBytes, "invalid request body") {
			return
		}
		space, err := catalog.UpdatePurpose(r.Context(), principal, spaceID, input.Purpose)
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, space)
		case errors.Is(err, knowledgecatalog.ErrForbidden):
			writeError(w, http.StatusForbidden, "admin role required")
		case errors.Is(err, knowledgecatalog.ErrNotFound):
			writeError(w, http.StatusNotFound, "knowledge space not found")
		default:
			writeError(w, http.StatusServiceUnavailable, "knowledge catalog unavailable")
		}
	}
}
