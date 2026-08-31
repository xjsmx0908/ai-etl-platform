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
	identityBindingChangeAction     = "identity.binding.change"
	identityUserChangeAction        = "identity.user.change"
	identityTenantChangeAction      = "identity.tenant.change"
	documentDeleteAction            = "document.delete"
	documentPublicationChangeAction = "document.publication.change"
	agentExecutionApproveAction     = "agent.execution.approve"
	indexGenerationRollbackAction   = "index_generation.rollback"
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
	result, err := a.sessions.Authenticate(request.Context(), credential, platformSessionAction(request))
	if err != nil {
		return auth.Principal{}, fmt.Errorf("invalid session credential")
	}
	if result.Decision != session.DecisionAllow && result.Decision != session.DecisionReauthenticate {
		return auth.Principal{}, fmt.Errorf("invalid session credential")
	}
	user, found, err := a.users.GetByID(request.Context(), result.Principal.SubjectID)
	if err != nil {
		return auth.Principal{}, fmt.Errorf("session authority lookup failed")
	}
	if !found || !user.Active || user.TenantID != result.Principal.TenantID {
		return auth.Principal{}, fmt.Errorf("invalid session authority")
	}
	if result.Decision == session.DecisionReauthenticate {
		return auth.Principal{}, auth.ReauthenticationRequiredError{Action: platformSessionAction(request)}
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
		HighRiskAssurance: "demo-mfa",
		EstablishmentAssurances: map[auth.AuthenticationMethod]string{
			auth.AuthenticationMethodFederated: "demo-mfa",
			auth.AuthenticationMethodLocal:     "local-password",
		},
		ActionRisks: map[string]session.Risk{
			platformSessionRequestAction:    session.RiskStandard,
			identityBindingChangeAction:     session.RiskHigh,
			identityUserChangeAction:        session.RiskHigh,
			identityTenantChangeAction:      session.RiskHigh,
			documentDeleteAction:            session.RiskHigh,
			documentPublicationChangeAction: session.RiskHigh,
			agentExecutionApproveAction:     session.RiskHigh,
			indexGenerationRollbackAction:   session.RiskHigh,
		},
		Revision: "personal-demo-v1",
	}
}

func platformSessionAction(request *http.Request) string {
	path := strings.TrimSuffix(request.URL.Path, "/")
	method := request.Method
	segments := pathSegments(path)
	switch {
	case len(segments) == 4 && segments[0] == "v1" && segments[1] == "users" &&
		segments[2] != "" && segments[3] == "external-identities" && method == http.MethodPost,
		len(segments) == 5 && segments[0] == "v1" && segments[1] == "users" &&
			segments[2] != "" && segments[3] == "external-identities" && segments[4] != "" && method == http.MethodDelete:
		return identityBindingChangeAction
	case path == "/v1/users" && method == http.MethodPost,
		len(segments) == 3 && segments[0] == "v1" && segments[1] == "users" && segments[2] != "" &&
			(method == http.MethodPut || method == http.MethodDelete),
		len(segments) == 4 && segments[0] == "v1" && segments[1] == "users" && segments[2] != "" &&
			segments[3] == "password" && method == http.MethodPost:
		return identityUserChangeAction
	case path == "/v1/tenants" && method == http.MethodPost:
		return identityTenantChangeAction
	case documentItemPath(segments) && method == http.MethodDelete:
		return documentDeleteAction
	case documentItemPath(segments) && method == http.MethodPatch:
		return documentPublicationChangeAction
	case len(segments) == 5 && segments[0] == "v1" && segments[1] == "agent" && segments[2] == "runs" &&
		segments[3] != "" && segments[4] == "approve" && method == http.MethodPost:
		return agentExecutionApproveAction
	case path == "/v1/index-generations/rollback" && method == http.MethodPost:
		return indexGenerationRollbackAction
	default:
		return platformSessionRequestAction
	}
}

func pathSegments(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func documentItemPath(segments []string) bool {
	return len(segments) == 3 && segments[0] == "v1" && segments[1] == "documents" && segments[2] != ""
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
