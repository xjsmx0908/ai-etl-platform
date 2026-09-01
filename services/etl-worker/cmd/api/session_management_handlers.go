package main

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"ai-etl-pipeline/internal/session"
)

type managedSessionView struct {
	Handle               string    `json:"handle"`
	AuthenticationMethod string    `json:"authentication_method"`
	CreatedAt            time.Time `json:"created_at"`
	LastActivityAt       time.Time `json:"last_activity_at"`
	ExpiresAt            time.Time `json:"expires_at"`
	Current              bool      `json:"current"`
}

func handleSessionManagement(sessions *session.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		credential, isSession := sessionCredentialFromRequest(r)
		if !isSession {
			if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
				writeError(w, http.StatusUnauthorized, "session credential required")
				return
			}
			writeError(w, http.StatusNotImplemented, "session management requires a platform session")
			return
		}
		if sessions == nil || credential == "" {
			writeError(w, http.StatusServiceUnavailable, "session management unavailable")
			return
		}
		switch r.Method {
		case http.MethodGet:
			if r.PathValue("handle") != "" {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			items, err := sessions.List(r.Context(), session.ListCommand{Credential: credential})
			if err != nil {
				writeSessionManagementError(w, err)
				return
			}
			views := make([]managedSessionView, 0, len(items))
			for _, item := range items {
				views = append(views, managedSessionView{
					Handle: string(item.Handle), AuthenticationMethod: string(item.AuthenticationMethod),
					CreatedAt: item.CreatedAt, LastActivityAt: item.LastActivityAt,
					ExpiresAt: item.ExpiresAt, Current: item.Current,
				})
			}
			writeJSON(w, http.StatusOK, map[string]any{"sessions": views})
		case http.MethodDelete:
			handle, err := session.ParseManagementHandle(r.PathValue("handle"))
			if err != nil {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			err = sessions.RevokeManaged(r.Context(), session.RevokeManagedCommand{
				Credential: credential, Handle: handle,
				CorrelationID: "session-device-revoke:" + uuid.NewString(),
			})
			if err != nil {
				writeSessionManagementError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	})
}

func writeSessionManagementError(w http.ResponseWriter, err error) {
	if errors.Is(err, session.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "session management unavailable")
		return
	}
	writeError(w, http.StatusUnauthorized, "invalid platform session")
}
