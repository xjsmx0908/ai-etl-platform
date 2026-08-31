package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/db"
	"ai-etl-pipeline/internal/session"
	"ai-etl-pipeline/internal/userstore"
)

const (
	platformSessionCredentialPrefix = "ps1_"
	platformSessionRequestAction    = "platform.request"
)

type sessionCredentialAuthenticator struct {
	legacy   auth.Authenticator
	sessions *session.Manager
	users    userstore.Store
}

func newSessionCredentialAuthenticator(legacy auth.Authenticator, sessions *session.Manager, users userstore.Store) auth.Authenticator {
	return &sessionCredentialAuthenticator{legacy: legacy, sessions: sessions, users: users}
}

func (a *sessionCredentialAuthenticator) Authenticate(request *http.Request) (auth.Principal, error) {
	credential, isSession := sessionCredentialFromRequest(request)
	if !isSession {
		if a.legacy == nil {
			return auth.Principal{}, fmt.Errorf("legacy authentication is unavailable")
		}
		return a.legacy.Authenticate(request)
	}
	if a.sessions == nil || a.users == nil || credential == "" {
		return auth.Principal{}, fmt.Errorf("invalid session credential")
	}
	result, err := a.sessions.Authenticate(request.Context(), credential, platformSessionRequestAction)
	if err != nil || result.Decision != session.DecisionAllow {
		return auth.Principal{}, fmt.Errorf("invalid session credential")
	}
	user, found, err := a.users.GetByID(request.Context(), result.Principal.SubjectID)
	if err != nil {
		return auth.Principal{}, fmt.Errorf("session authority lookup failed")
	}
	if !found || !user.Active || user.TenantID != result.Principal.TenantID {
		return auth.Principal{}, fmt.Errorf("invalid session authority")
	}
	return auth.Principal{
		TenantID:             user.TenantID,
		SubjectID:            user.ID,
		Role:                 user.Role,
		AuthenticationMethod: result.Principal.AuthenticationMethod,
		Capabilities:         auth.ScopesForRole(user.Role),
	}, nil
}

func sessionCredentialFromRequest(request *http.Request) (string, bool) {
	authorization := request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		return "", false
	}
	credential := strings.TrimPrefix(authorization, "Bearer ")
	if !strings.HasPrefix(credential, platformSessionCredentialPrefix) {
		return "", false
	}
	return strings.TrimPrefix(credential, platformSessionCredentialPrefix), true
}

func platformSessionPolicy() session.Policy {
	return session.Policy{
		IdleTimeout:       30 * time.Minute,
		AbsoluteLifetime:  8 * time.Hour,
		HighRiskFreshness: 10 * time.Minute,
		RequiredAssurance: "demo-mfa",
		ActionRisks: map[string]session.Risk{
			platformSessionRequestAction: session.RiskStandard,
		},
		Revision: "personal-demo-v1",
	}
}

func newPlatformSessionManager(cfg config.Config, database db.Querier) (*session.Manager, error) {
	if !cfg.SessionCoreEnabled {
		return nil, nil
	}
	if !cfg.IsDev() || cfg.IdentityPolicyProfile != "personal-demo-v1" {
		return nil, fmt.Errorf("session credential authentication requires the personal-demo-v1 dev profile")
	}
	manager, err := session.New(session.NewPostgresStore(database), platformSessionPolicy())
	if err != nil {
		return nil, fmt.Errorf("construct session manager: %w", err)
	}
	return manager, nil
}
