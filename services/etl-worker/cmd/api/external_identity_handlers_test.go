package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/externalidentity"
)

type identityManagerStub struct {
	items      []externalidentity.Binding
	listErr    error
	bindResult externalidentity.Binding
	bindErr    error
	deleteErr  error
	actor      auth.Principal
	userID     string
	bindingID  string
	request    externalidentity.BindRequest
}

func (s *identityManagerStub) List(_ context.Context, actor auth.Principal, userID string) ([]externalidentity.Binding, error) {
	s.actor, s.userID = actor, userID
	return s.items, s.listErr
}
func (s *identityManagerStub) Bind(_ context.Context, actor auth.Principal, request externalidentity.BindRequest) (externalidentity.Binding, error) {
	s.actor, s.userID, s.request = actor, request.InternalUserID, request
	return s.bindResult, s.bindErr
}
func (s *identityManagerStub) Delete(_ context.Context, actor auth.Principal, userID, bindingID string) error {
	s.actor, s.userID, s.bindingID = actor, userID, bindingID
	return s.deleteErr
}

func identityAdminContext() context.Context {
	return context.WithValue(context.Background(), auth.CtxPrincipal, auth.Principal{
		TenantID: "acme", SubjectID: "admin-1", Role: "admin", Capabilities: []string{auth.ScopeAdmin},
	})
}

func TestExternalIdentityHandlerUsesAuthenticatedPrincipal(t *testing.T) {
	stub := &identityManagerStub{bindResult: externalidentity.Binding{
		ID: "binding-1", ExternalIdentity: externalidentity.ExternalIdentity{Issuer: "https://idp.example.com", Subject: "alice"},
		InternalUserID: "user-1", TenantID: "acme",
	}}
	rec := doRequest(handleExternalIdentities(stub), http.MethodPost,
		"/v1/users/user-1/external-identities", externalIdentityRequest{Issuer: "https://idp.example.com", Subject: "alice"}, identityAdminContext())
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if stub.actor.SubjectID != "admin-1" || stub.actor.TenantID != "acme" || stub.userID != "user-1" || stub.request.Identity.Subject != "alice" {
		t.Fatalf("handler did not pass authenticated principal and target: %+v user=%q request=%+v", stub.actor, stub.userID, stub.request)
	}
	var view externalIdentityView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || view.Subject != "alice" || view.ID != "binding-1" {
		t.Fatalf("view=%+v err=%v", view, err)
	}
}

func TestExternalIdentityHandlerRoutesAndMapsSafeErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		method     string
		target     string
		managerErr error
		want       int
	}{
		"invalid body": {method: http.MethodPost, target: "/v1/users/user-1/external-identities", want: http.StatusBadRequest},
		"conflict":     {method: http.MethodPost, target: "/v1/users/user-1/external-identities", managerErr: externalidentity.ErrConflict, want: http.StatusConflict},
		"cross tenant": {method: http.MethodGet, target: "/v1/users/user-1/external-identities", managerErr: externalidentity.ErrNotFound, want: http.StatusNotFound},
		"delete":       {method: http.MethodDelete, target: "/v1/users/user-1/external-identities/binding-1", want: http.StatusNoContent},
		"unavailable":  {method: http.MethodGet, target: "/v1/users/user-1/external-identities", managerErr: externalidentity.ErrUnavailable, want: http.StatusServiceUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			stub := &identityManagerStub{}
			var body any
			switch tc.method {
			case http.MethodPost:
				stub.bindErr = tc.managerErr
				if name != "invalid body" {
					body = externalIdentityRequest{Issuer: "https://idp.example.com", Subject: "alice"}
				} else {
					body = map[string]string{"issuer": ""}
				}
			case http.MethodGet:
				stub.listErr = tc.managerErr
			case http.MethodDelete:
				stub.deleteErr = tc.managerErr
			}
			rec := doRequest(handleExternalIdentities(stub), tc.method, tc.target, body, identityAdminContext())
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
			if tc.method == http.MethodDelete && stub.bindingID != "binding-1" {
				t.Fatalf("binding id=%q", stub.bindingID)
			}
		})
	}
}

func TestExternalIdentityHandlerRejectsMissingPrincipal(t *testing.T) {
	stub := &identityManagerStub{bindErr: errors.New("must not be reached")}
	rec := doRequest(handleExternalIdentities(stub), http.MethodGet,
		"/v1/users/user-1/external-identities", nil, context.Background())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type httpAuthenticatorStub struct {
	principal auth.Principal
	err       error
}

func (s httpAuthenticatorStub) Authenticate(*http.Request) (auth.Principal, error) {
	return s.principal, s.err
}

func TestExternalIdentityHTTPSeamRequiresAuthenticationAndAdminCapability(t *testing.T) {
	stub := &identityManagerStub{}
	endpoint := requireScopes(auth.ScopeAdmin)(handleExternalIdentities(stub))
	for name, tc := range map[string]struct {
		authenticator auth.Authenticator
		want          int
	}{
		"authentication failure": {authenticator: httpAuthenticatorStub{err: errors.New("invalid credential")}, want: http.StatusUnauthorized},
		"non-admin capability": {authenticator: httpAuthenticatorStub{principal: auth.Principal{
			TenantID: "acme", SubjectID: "user-1", Role: "user", Capabilities: []string{auth.ScopeQuery},
		}}, want: http.StatusForbidden},
		"admin": {authenticator: httpAuthenticatorStub{principal: auth.Principal{
			TenantID: "acme", SubjectID: "admin-1", Role: "admin", Capabilities: []string{auth.ScopeAdmin},
		}}, want: http.StatusOK},
	} {
		t.Run(name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, "/v1/users/user-1/external-identities", nil)
			rec := doRequest(auth.Middleware(tc.authenticator)(endpoint), req.Method, req.URL.String(), nil, req.Context())
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}
