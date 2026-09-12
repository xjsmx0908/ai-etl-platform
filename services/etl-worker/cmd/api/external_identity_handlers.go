package main

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/externalidentity"
)

type externalIdentityRequest struct {
	Issuer  string `json:"issuer"`
	Subject string `json:"subject"`
}

type externalIdentityView struct {
	ID             string    `json:"id"`
	Issuer         string    `json:"issuer"`
	Subject        string    `json:"subject"`
	InternalUserID string    `json:"internal_user_id"`
	TenantID       string    `json:"tenant_id"`
	CreatedAt      time.Time `json:"created_at"`
}

func toExternalIdentityView(binding externalidentity.Binding) externalIdentityView {
	return externalIdentityView{
		ID: binding.ID, Issuer: binding.Issuer, Subject: binding.Subject,
		InternalUserID: binding.InternalUserID, TenantID: binding.TenantID, CreatedAt: binding.CreatedAt,
	}
}

func handleExternalIdentities(manager externalidentity.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal := auth.GetPrincipal(r.Context())
		if principal.SubjectID == "" || principal.TenantID == "" || principal.Role != "admin" {
			writeError(w, http.StatusForbidden, "administrator required")
			return
		}
		userID, bindingID, ok := externalIdentityPath(r.URL.Path)
		if !ok {
			writeError(w, http.StatusNotFound, "external identity binding not found")
			return
		}
		switch {
		case r.Method == http.MethodGet && bindingID == "":
			items, err := manager.List(r.Context(), principal, userID)
			if err != nil {
				writeExternalIdentityError(w, err)
				return
			}
			views := make([]externalIdentityView, 0, len(items))
			for _, item := range items {
				views = append(views, toExternalIdentityView(item))
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": views})
		case r.Method == http.MethodPost && bindingID == "":
			var request externalIdentityRequest
			if !decodeJSONBody(w, r, &request, defaultJSONBodyBytes, "issuer and subject are required") {
				return
			}
			if request.Issuer == "" || request.Subject == "" {
				writeError(w, http.StatusBadRequest, "issuer and subject are required")
				return
			}
			binding, err := manager.Bind(r.Context(), principal, externalidentity.BindRequest{
				InternalUserID: userID,
				Identity:       externalidentity.ExternalIdentity{Issuer: request.Issuer, Subject: request.Subject},
			})
			if err != nil {
				writeExternalIdentityError(w, err)
				return
			}
			writeJSON(w, http.StatusCreated, toExternalIdentityView(binding))
		case r.Method == http.MethodDelete && bindingID != "":
			if err := manager.Delete(r.Context(), principal, userID, bindingID); err != nil {
				writeExternalIdentityError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func externalIdentityPath(path string) (userID, bindingID string, ok bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 4 && len(parts) != 5 {
		return "", "", false
	}
	if parts[0] != "v1" || parts[1] != "users" || parts[2] == "" || parts[3] != "external-identities" {
		return "", "", false
	}
	if len(parts) == 5 {
		if parts[4] == "" {
			return "", "", false
		}
		bindingID = parts[4]
	}
	return parts[2], bindingID, true
}

func writeExternalIdentityError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, externalidentity.ErrInvalidIdentity):
		writeError(w, http.StatusBadRequest, "invalid external identity")
	case errors.Is(err, externalidentity.ErrConflict):
		writeError(w, http.StatusConflict, "external identity is already bound")
	case errors.Is(err, externalidentity.ErrNotFound):
		writeError(w, http.StatusNotFound, "external identity binding not found")
	case errors.Is(err, externalidentity.ErrForbidden):
		writeError(w, http.StatusForbidden, "administrator required")
	default:
		writeError(w, http.StatusServiceUnavailable, "external identity directory unavailable")
	}
}
