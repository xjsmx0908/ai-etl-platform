package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/session"
)

func TestSessionManagementHTTPListsAndRevokesOwnedSession(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	manager, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{
		TenantID: "demo-tenant", SubjectID: "user-42",
		AuthenticationMethod: auth.AuthenticationMethodFederated,
	}
	establish := func(correlation string) session.Credential {
		t.Helper()
		credential, establishErr := manager.Establish(context.Background(), session.EstablishCommand{
			Principal:     principal,
			Evidence:      session.AuthenticationEvidence{Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now},
			CorrelationID: correlation,
		})
		if establishErr != nil {
			t.Fatal(establishErr)
		}
		return credential
	}
	current := establish("http-session-current")
	target := establish("http-session-target")
	handler := handleSessionManagement(manager)

	request := httptest.NewRequest(http.MethodGet, "/v1/auth/sessions", nil)
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+current.Token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Sessions []struct {
			Handle               string    `json:"handle"`
			AuthenticationMethod string    `json:"authentication_method"`
			CreatedAt            time.Time `json:"created_at"`
			LastActivityAt       time.Time `json:"last_activity_at"`
			ExpiresAt            time.Time `json:"expires_at"`
			Current              bool      `json:"current"`
		} `json:"sessions"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if len(response.Sessions) != 2 {
		t.Fatalf("sessions=%+v", response.Sessions)
	}
	var targetHandle string
	for _, item := range response.Sessions {
		if item.Handle == "" || item.AuthenticationMethod != "federated" || item.CreatedAt.IsZero() ||
			item.LastActivityAt.IsZero() || item.ExpiresAt.IsZero() {
			t.Fatalf("invalid public view: %+v", item)
		}
		if !item.Current {
			targetHandle = item.Handle
		}
	}

	request = httptest.NewRequest(http.MethodDelete, "/v1/auth/sessions/"+targetHandle, nil)
	request.SetPathValue("handle", targetHandle)
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+current.Token)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	assertManagedHTTPDecision(t, manager, target.Token, session.DecisionDeny)
}

func TestSessionManagementHTTPRejectsLegacyAndUnavailableSessions(t *testing.T) {
	tests := []struct {
		name       string
		manager    *session.Manager
		authorize  string
		wantStatus int
	}{
		{name: "legacy JWT is unsupported", manager: nil, authorize: "Bearer legacy.jwt", wantStatus: http.StatusNotImplemented},
		{name: "missing credential", manager: nil, wantStatus: http.StatusUnauthorized},
		{name: "session store unavailable", manager: mustUnavailableSessionManager(t), authorize: "Bearer ps1_opaque", wantStatus: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/v1/auth/sessions", nil)
			request.Header.Set("Authorization", test.authorize)
			recorder := httptest.NewRecorder()
			handleSessionManagement(test.manager).ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
		})
	}
}

func mustUnavailableSessionManager(t *testing.T) *session.Manager {
	t.Helper()
	manager, err := session.New(session.NewPostgresStore(nil), platformSessionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func assertManagedHTTPDecision(t *testing.T, manager *session.Manager, credential string, expected session.Decision) {
	t.Helper()
	result, err := manager.Authenticate(context.Background(), credential, platformSessionRequestAction)
	if err != nil || result.Decision != expected {
		t.Fatalf("decision=%q want=%q err=%v", result.Decision, expected, err)
	}
}
