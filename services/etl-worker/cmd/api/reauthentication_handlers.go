package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"ai-etl-pipeline/internal/audit"
	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/oidcauth"
	"ai-etl-pipeline/internal/session"
	"ai-etl-pipeline/internal/userstore"
)

type reauthenticationStartRequest struct {
	Action   string `json:"action"`
	ReturnTo string `json:"return_to"`
}

type reauthenticationStartResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	State            string `json:"state"`
	ExpiresIn        int64  `json:"expires_in"`
}

type reauthenticationCallbackRequest struct {
	Code        string `json:"code"`
	State       string `json:"state"`
	CookieState string `json:"cookie_state"`
}

type reauthenticationCallbackResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Action    string    `json:"action"`
	ReturnTo  string    `json:"return_to"`
}

func handleReauthenticationStart(flow *oidcauth.Flow, sessions *session.Manager, users userstore.Store, audits audit.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if flow == nil || sessions == nil || users == nil {
			writeError(w, http.StatusServiceUnavailable, "reauthentication unavailable")
			return
		}
		credential, isSession := sessionCredentialFromRequest(r)
		if !isSession || credential == "" {
			writeError(w, http.StatusUnauthorized, "valid platform session required")
			return
		}
		var request reauthenticationStartRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || strings.TrimSpace(request.Action) != request.Action ||
			platformSessionPolicy().ActionRisks[request.Action] != session.RiskHigh {
			writeError(w, http.StatusBadRequest, "registered high-risk action required")
			return
		}
		result, err := sessions.Authenticate(r.Context(), credential, request.Action)
		if err != nil || result.Decision == session.DecisionDeny {
			writeError(w, http.StatusUnauthorized, "valid platform session required")
			return
		}
		if result.Decision != session.DecisionReauthenticate ||
			result.Principal.AuthenticationMethod != auth.AuthenticationMethodFederated {
			writeError(w, http.StatusConflict, "reauthentication is not available for this session")
			return
		}
		user, found, err := users.GetByID(r.Context(), result.Principal.SubjectID)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "reauthentication unavailable")
			return
		}
		if !found || !user.Active || user.TenantID != result.Principal.TenantID {
			writeError(w, http.StatusUnauthorized, "valid platform session required")
			return
		}
		start, err := flow.StartReauthentication(r.Context(), oidcauth.ReauthenticationStartCommand{
			Credential: credential, Action: request.Action, ReturnTo: request.ReturnTo,
			Principal: result.Principal,
		})
		if err != nil {
			recordReauthenticationAudit(r, audits, user, request.Action, audit.ResultFailure, "transaction_unavailable", "")
			writeError(w, http.StatusServiceUnavailable, "reauthentication unavailable")
			return
		}
		recordReauthenticationAudit(r, audits, user, request.Action, audit.ResultSuccess, "started", reauthenticationCorrelationID(start.State))
		writeJSON(w, http.StatusOK, reauthenticationStartResponse{
			AuthorizationURL: start.AuthorizationURL, State: start.State,
			ExpiresIn: int64(start.ExpiresIn.Seconds()),
		})
	})
}

func handleReauthenticationCallback(flow *oidcauth.Flow, sessions *session.Manager, users userstore.Store, audits audit.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if flow == nil || sessions == nil || users == nil {
			writeError(w, http.StatusServiceUnavailable, "reauthentication unavailable")
			return
		}
		credential, isSession := sessionCredentialFromRequest(r)
		if !isSession || credential == "" {
			writeError(w, http.StatusUnauthorized, "valid platform session required")
			return
		}
		var request reauthenticationCallbackRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || request.Code == "" || request.State == "" || request.CookieState == "" {
			writeError(w, http.StatusBadRequest, "invalid reauthentication callback")
			return
		}
		result, err := flow.CompleteReauthentication(r.Context(), oidcauth.ReauthenticationCompleteCommand{
			CookieState: request.CookieState, CallbackState: request.State,
			Code: request.Code, CurrentCredential: credential,
		})
		if err != nil {
			recordReauthenticationAudit(r, audits, userstore.User{}, "", audit.ResultFailure, "authentication_failed", reauthenticationCorrelationID(request.State))
			writeError(w, http.StatusUnauthorized, "reauthentication failed")
			return
		}
		if platformSessionPolicy().ActionRisks[result.Action] != session.RiskHigh {
			writeError(w, http.StatusUnauthorized, "reauthentication failed")
			return
		}
		user, found, err := users.GetByID(r.Context(), result.Principal.SubjectID)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "reauthentication unavailable")
			return
		}
		if !found || !user.Active || user.TenantID != result.Principal.TenantID {
			writeError(w, http.StatusUnauthorized, "reauthentication failed")
			return
		}
		rotated, err := sessions.Establish(r.Context(), session.EstablishCommand{
			Principal: result.Principal, Evidence: result.Evidence, ReplacesCredential: credential,
			CorrelationID: reauthenticationCorrelationID(request.State),
		})
		if err != nil {
			recordReauthenticationAudit(r, audits, user, result.Action, audit.ResultFailure, "session_rotation_failed", reauthenticationCorrelationID(request.State))
			if errors.Is(err, session.ErrUnavailable) {
				writeError(w, http.StatusServiceUnavailable, "reauthentication unavailable")
				return
			}
			writeError(w, http.StatusUnauthorized, "reauthentication failed")
			return
		}
		recordReauthenticationAudit(r, audits, user, result.Action, audit.ResultSuccess, "completed", reauthenticationCorrelationID(request.State))
		writeJSON(w, http.StatusOK, reauthenticationCallbackResponse{
			Token: platformSessionCredentialPrefix + rotated.Token, ExpiresAt: rotated.ExpiresAt.UTC(),
			Action: result.Action, ReturnTo: result.ReturnTo,
		})
	})
}

func reauthenticationCorrelationID(state string) string {
	digest := sha256.Sum256([]byte(state))
	return "reauth:" + base64.RawURLEncoding.EncodeToString(digest[:])
}

func recordReauthenticationAudit(r *http.Request, audits audit.Store, user userstore.User, action, result, reason, correlationID string) {
	recordAudit(r.Context(), audits, audit.Entry{
		TenantID: user.TenantID, ActorUserID: user.ID, ActorRole: user.Role,
		Action: "session_reauthentication", Result: result,
		Detail: map[string]any{"policy_action": action, "reason": reason, "correlation_id": correlationID},
	})
}
