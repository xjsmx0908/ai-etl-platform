package main

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"ai-etl-pipeline/internal/audit"
	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/oidcauth"
	"ai-etl-pipeline/internal/session"
)

type logoutResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	State            string `json:"state"`
	ExpiresIn        int64  `json:"expires_in"`
}

type logoutCallbackRequest struct {
	State       string `json:"state"`
	CookieState string `json:"cookie_state"`
}

func handleLogout(sessions *session.Manager, oidcFlow *oidcauth.Flow, audits audit.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		credential, isSession := sessionCredentialFromRequest(r)
		if !isSession {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if sessions == nil || credential == "" {
			writeError(w, http.StatusServiceUnavailable, "logout unavailable")
			return
		}

		principal := auth.Principal{}
		correlationID := "logout:" + uuid.NewString()
		result, authenticateErr := sessions.Authenticate(r.Context(), credential, platformSessionRequestAction)
		if authenticateErr != nil {
			recordLogoutAudit(r, audits, principal, audit.ResultFailure, "session_store_unavailable", correlationID)
			writeError(w, http.StatusServiceUnavailable, "logout unavailable")
			return
		}
		if result.Decision == session.DecisionAllow || result.Decision == session.DecisionReauthenticate {
			principal = result.Principal
		}
		if err := sessions.Revoke(r.Context(), session.RevokeCommand{
			Credential: credential, Scope: session.RevokeCurrent, CorrelationID: correlationID,
		}); err != nil {
			recordLogoutAudit(r, audits, principal, audit.ResultFailure, "session_revocation_failed", correlationID)
			if errors.Is(err, session.ErrUnavailable) {
				writeError(w, http.StatusServiceUnavailable, "logout unavailable")
				return
			}
			writeError(w, http.StatusUnauthorized, "logout failed")
			return
		}
		recordLogoutAudit(r, audits, principal, audit.ResultSuccess, "local_session_revoked", correlationID)
		if principal.AuthenticationMethod == auth.AuthenticationMethodFederated && oidcFlow != nil {
			start, err := oidcFlow.StartLogout(r.Context(), r.URL.Query().Get("return_to"))
			if err == nil {
				writeJSON(w, http.StatusOK, logoutResponse{
					AuthorizationURL: start.AuthorizationURL, State: start.State, ExpiresIn: int64(start.ExpiresIn.Seconds()),
				})
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func handleLogoutCallback(oidcFlow *oidcauth.Flow) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if oidcFlow == nil {
			writeError(w, http.StatusServiceUnavailable, "logout callback unavailable")
			return
		}
		var request logoutCallbackRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || request.State == "" || request.CookieState == "" {
			writeError(w, http.StatusBadRequest, "invalid logout callback")
			return
		}
		returnTo, err := oidcFlow.CompleteLogout(r.Context(), request.CookieState, request.State)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "logout callback failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"return_to": returnTo})
	})
}

func recordLogoutAudit(r *http.Request, audits audit.Store, principal auth.Principal, result, reason, correlationID string) {
	recordAudit(r.Context(), audits, audit.Entry{
		TenantID: principal.TenantID, ActorUserID: principal.SubjectID,
		Action: "session_logout", Result: result,
		Detail: map[string]any{"reason": reason, "correlation_id": correlationID},
	})
}
