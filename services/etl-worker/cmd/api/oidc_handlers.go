package main

import (
	"encoding/json"
	"net/http"

	"ai-etl-pipeline/internal/audit"
	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/oidcauth"
	"ai-etl-pipeline/internal/userstore"
)

type oidcCallbackRequest struct {
	Code        string `json:"code"`
	State       string `json:"state"`
	CookieState string `json:"cookie_state"`
}

type oidcStartResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	State            string `json:"state"`
	ExpiresIn        int64  `json:"expires_in"`
}

type oidcCallbackResponse struct {
	loginResponse
	ReturnTo string `json:"return_to"`
}

func handleOIDCStart(flow *oidcauth.Flow) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if flow == nil {
			writeError(w, http.StatusNotFound, "OIDC authentication unavailable")
			return
		}
		start, err := flow.Start(r.Context(), r.URL.Query().Get("return_to"))
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "OIDC authentication unavailable")
			return
		}
		writeJSON(w, http.StatusOK, oidcStartResponse{
			AuthorizationURL: start.AuthorizationURL, State: start.State,
			ExpiresIn: int64(start.ExpiresIn.Seconds()),
		})
	})
}

func handleOIDCCallback(cfg config.Config, flow *oidcauth.Flow, users userstore.Store, audits audit.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if flow == nil || users == nil {
			writeError(w, http.StatusServiceUnavailable, "OIDC authentication unavailable")
			return
		}
		var request oidcCallbackRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || request.Code == "" || request.State == "" || request.CookieState == "" {
			writeError(w, http.StatusBadRequest, "invalid OIDC callback")
			return
		}
		principal, returnTo, err := flow.Complete(r.Context(), request.CookieState, request.State, request.Code)
		if err != nil {
			recordAudit(r.Context(), audits, audit.Entry{
				Action: "oidc_login", Result: audit.ResultFailure,
				Detail: map[string]any{"reason": "authentication_failed"},
			})
			writeError(w, http.StatusUnauthorized, "OIDC authentication failed")
			return
		}
		user, found, err := users.GetByID(r.Context(), principal.SubjectID)
		if err != nil || !found || !user.Active || user.TenantID != principal.TenantID || user.Role != principal.Role {
			recordAudit(r.Context(), audits, audit.Entry{
				TenantID: principal.TenantID, ActorUserID: principal.SubjectID,
				Action: "oidc_login", Result: audit.ResultFailure,
				Detail: map[string]any{"reason": "internal_authority_changed"},
			})
			writeError(w, http.StatusUnauthorized, "OIDC identity is not authorized")
			return
		}
		token, expiresAt, err := auth.IssueFederatedToken(
			cfg.JWTSecret, user.ID, user.Username, user.Role, user.TenantID, user.TokenVersion,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		recordAudit(r.Context(), audits, audit.Entry{
			TenantID: user.TenantID, ActorUserID: user.ID, ActorRole: user.Role,
			Action: "oidc_login", Result: audit.ResultSuccess,
			Detail: map[string]any{"authentication_method": string(auth.AuthenticationMethodFederated)},
		})
		writeJSON(w, http.StatusOK, oidcCallbackResponse{
			ReturnTo: returnTo,
			loginResponse: loginResponse{
				Token: token, ExpiresAt: expiresAt.UTC(),
				User: loginUser{
					ID: user.ID, Username: user.Username, Role: user.Role,
					TenantID: user.TenantID, Active: user.Active,
				},
			},
		})
	})
}
