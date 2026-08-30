package scim

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ai-etl-pipeline/internal/identitylifecycle"
)

type lifecycleStub struct {
	mu       sync.Mutex
	results  map[string]identitylifecycle.LifecycleResult
	calls    []identitylifecycle.LifecycleCommand
	applyErr error
}

func newLifecycleStub() *lifecycleStub {
	return &lifecycleStub{results: map[string]identitylifecycle.LifecycleResult{}}
}

func (s *lifecycleStub) Apply(_ context.Context, command identitylifecycle.LifecycleCommand) (identitylifecycle.LifecycleResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, command)
	if s.applyErr != nil {
		return identitylifecycle.LifecycleResult{}, s.applyErr
	}
	if command.Operation == identitylifecycle.OperationCreate {
		result := identitylifecycle.LifecycleResult{ID: "lifecycle-1", InternalUserID: "user-1", ProviderResourceID: command.ProviderResourceID,
			TenantID: "acme", Username: command.Username, DisplayName: commandString(command.DisplayName), Email: commandString(command.Email),
			Role: "readonly", Active: *command.Active, SourceVersion: command.SourceVersion}
		s.results[result.ProviderResourceID] = result
		return result, nil
	}
	result, found := s.results[command.ProviderResourceID]
	if !found {
		return identitylifecycle.LifecycleResult{}, identitylifecycle.ErrNotFound
	}
	if command.Operation == identitylifecycle.OperationDelete {
		result.Active, result.Deleted = false, true
	} else {
		if command.Username != "" {
			result.Username = command.Username
		}
		if command.Active != nil {
			result.Active = *command.Active
		}
		if command.DisplayName != nil {
			result.DisplayName = *command.DisplayName
		}
		if command.Email != nil {
			result.Email = *command.Email
		}
	}
	s.results[result.ProviderResourceID] = result
	return result, nil
}

func TestUsersHTTPPutGetConflictAndBearerRotation(t *testing.T) {
	adapter := newLifecycleStub()
	handler, err := NewHandler(Config{
		ConnectorID: "workforce", Issuer: "https://idp.example.com/realms/acme",
		SubjectAttribute: "externalId", BearerTokens: []string{"retiring-secret", "current-secret"}, MaxBodyBytes: 32 << 10,
	}, adapter, adapter)
	if err != nil {
		t.Fatal(err)
	}
	if handler.config.BearerTokens != nil {
		t.Fatal("handler retained raw bearer credentials")
	}
	created := scimRequest(t, handler, http.MethodPost, "/scim/v2/Users", `{
		"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
		"userName":"alice@example.com","externalId":"oidc-subject-42","active":true}`, "retiring-secret")
	if created.Code != http.StatusCreated {
		t.Fatalf("create with retiring credential status=%d body=%s", created.Code, created.Body.String())
	}
	var createdUser User
	if err := json.Unmarshal(created.Body.Bytes(), &createdUser); err != nil {
		t.Fatal(err)
	}
	updated := scimRequest(t, handler, http.MethodPut, "/scim/v2/Users/"+createdUser.ID, `{
		"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
		"userName":"alice.renamed@example.com","externalId":"oidc-subject-42",
		"displayName":"Alice Renamed","active":true,
		"emails":[{"value":"alice.renamed@example.com","primary":true}]}`, "current-secret")
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"userName":"alice.renamed@example.com"`) {
		t.Fatalf("put status=%d body=%s", updated.Code, updated.Body.String())
	}
	fetched := scimRequest(t, handler, http.MethodGet, "/scim/v2/Users/"+createdUser.ID, "", "current-secret")
	if fetched.Code != http.StatusOK || !strings.Contains(fetched.Body.String(), `"displayName":"Alice Renamed"`) {
		t.Fatalf("get status=%d body=%s", fetched.Code, fetched.Body.String())
	}
	cleared := scimRequest(t, handler, http.MethodPut, "/scim/v2/Users/"+createdUser.ID, `{
		"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
		"userName":"alice.renamed@example.com","externalId":"oidc-subject-42","active":true}`, "current-secret")
	if cleared.Code != http.StatusOK || strings.Contains(cleared.Body.String(), `"displayName"`) || strings.Contains(cleared.Body.String(), `"emails"`) {
		t.Fatalf("put did not clear omitted profile fields status=%d body=%s", cleared.Code, cleared.Body.String())
	}
	adapter.applyErr = identitylifecycle.ErrConflict
	conflict := scimRequest(t, handler, http.MethodPatch, "/scim/v2/Users/"+createdUser.ID,
		`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}`, "current-secret")
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), `"status":"409"`) {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
}

func TestUsersHTTPRejectsUnboundedIdentifiers(t *testing.T) {
	adapter := newLifecycleStub()
	handler, err := NewHandler(Config{
		ConnectorID: "workforce", Issuer: "https://idp.example.com", SubjectAttribute: "externalId",
		BearerTokens: []string{"scim-secret"}, MaxBodyBytes: 32 << 10,
	}, adapter, adapter)
	if err != nil {
		t.Fatal(err)
	}
	invalidPath := scimRequest(t, handler, http.MethodGet, "/scim/v2/Users/not-a-resource-id", "", "scim-secret")
	if invalidPath.Code != http.StatusBadRequest {
		t.Fatalf("invalid resource path status=%d body=%s", invalidPath.Code, invalidPath.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, "/scim/v2/Users", bytes.NewBufferString(`{
		"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
		"userName":"alice@example.com","externalId":"subject","active":true}`))
	request.Header.Set("Authorization", "Bearer scim-secret")
	request.Header.Set("Idempotency-Key", strings.Repeat("k", 257))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversized idempotency key status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestUsersHTTPConcurrentCreateReplayConverges(t *testing.T) {
	adapter := newLifecycleStub()
	handler, err := NewHandler(Config{
		ConnectorID: "workforce", Issuer: "https://idp.example.com", SubjectAttribute: "externalId",
		BearerTokens: []string{"scim-secret"}, MaxBodyBytes: 32 << 10,
	}, adapter, adapter)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"alice","externalId":"subject","active":true}`
	responses := make([]*httptest.ResponseRecorder, 2)
	var wait sync.WaitGroup
	for index := range responses {
		wait.Add(1)
		go func() {
			defer wait.Done()
			responses[index] = scimRequest(t, handler, http.MethodPost, "/scim/v2/Users", body, "scim-secret")
		}()
	}
	wait.Wait()
	for _, response := range responses {
		if response.Code != http.StatusCreated {
			t.Fatalf("concurrent replay status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if len(adapter.calls) != 2 || adapter.calls[0].IdempotencyKey != adapter.calls[1].IdempotencyKey ||
		adapter.calls[0].ProviderResourceID != adapter.calls[1].ProviderResourceID {
		t.Fatalf("concurrent replay commands=%+v", adapter.calls)
	}
}

func (s *lifecycleStub) Get(_ context.Context, _ string, id string) (identitylifecycle.LifecycleResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, found := s.results[id]
	if !found {
		return identitylifecycle.LifecycleResult{}, identitylifecycle.ErrNotFound
	}
	return result, nil
}

func (s *lifecycleStub) FindByUsername(_ context.Context, _ string, username string) ([]identitylifecycle.LifecycleResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, result := range s.results {
		if strings.EqualFold(result.Username, username) {
			return []identitylifecycle.LifecycleResult{result}, nil
		}
	}
	return []identitylifecycle.LifecycleResult{}, nil
}

func TestUsersHTTPCreateLookupPatchDelete(t *testing.T) {
	adapter := newLifecycleStub()
	handler, err := NewHandler(Config{
		ConnectorID: "workforce", Issuer: "https://idp.example.com/realms/acme",
		SubjectAttribute: "externalId", BearerTokens: []string{"scim-secret"}, MaxBodyBytes: 32 << 10,
	}, adapter, adapter)
	if err != nil {
		t.Fatal(err)
	}
	created := scimRequest(t, handler, http.MethodPost, "/scim/v2/Users", `{
		"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
		"userName":"alice@example.com","externalId":"oidc-subject-42","displayName":"Alice",
		"active":true,"emails":[{"value":"alice@example.com","primary":true}]}`, "scim-secret")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var user User
	if err := json.Unmarshal(created.Body.Bytes(), &user); err != nil {
		t.Fatal(err)
	}
	if user.ID == "" || user.UserName != "alice@example.com" || user.ExternalID != "" || !user.Active {
		t.Fatalf("unexpected SCIM user: %+v", user)
	}
	replay := scimRequest(t, handler, http.MethodPost, "/scim/v2/Users", `{
		"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],
		"userName":"alice@example.com","externalId":"oidc-subject-42","displayName":"Alice",
		"active":true,"emails":[{"value":"alice@example.com","primary":true}]}`, "scim-secret")
	if replay.Code != http.StatusCreated {
		t.Fatalf("create replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	lookup := scimRequest(t, handler, http.MethodGet, `/scim/v2/Users?filter=userName%20eq%20%22alice%40example.com%22`, "", "scim-secret")
	if lookup.Code != http.StatusOK || !strings.Contains(lookup.Body.String(), `"totalResults":1`) {
		t.Fatalf("lookup status=%d body=%s", lookup.Code, lookup.Body.String())
	}
	patch := scimRequest(t, handler, http.MethodPatch, "/scim/v2/Users/"+user.ID,
		`{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"active","value":false}]}`, "scim-secret")
	if patch.Code != http.StatusOK || !strings.Contains(patch.Body.String(), `"active":false`) {
		t.Fatalf("patch status=%d body=%s", patch.Code, patch.Body.String())
	}
	deleted := scimRequest(t, handler, http.MethodDelete, "/scim/v2/Users/"+user.ID, "", "scim-secret")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	deletedGet := scimRequest(t, handler, http.MethodGet, "/scim/v2/Users/"+user.ID, "", "scim-secret")
	if deletedGet.Code != http.StatusNotFound {
		t.Fatalf("deleted get status=%d body=%s", deletedGet.Code, deletedGet.Body.String())
	}
	deletedLookup := scimRequest(t, handler, http.MethodGet, `/scim/v2/Users?filter=userName%20eq%20%22alice%40example.com%22`, "", "scim-secret")
	if deletedLookup.Code != http.StatusOK || !strings.Contains(deletedLookup.Body.String(), `"totalResults":0`) {
		t.Fatalf("deleted lookup status=%d body=%s", deletedLookup.Code, deletedLookup.Body.String())
	}
	if len(adapter.calls) != 4 || adapter.calls[0].ProviderResourceID != adapter.calls[1].ProviderResourceID ||
		adapter.calls[0].IdempotencyKey != adapter.calls[1].IdempotencyKey || adapter.calls[0].Identity.Subject != "oidc-subject-42" ||
		adapter.calls[2].Active == nil || *adapter.calls[2].Active {
		t.Fatalf("unexpected lifecycle calls: %+v", adapter.calls)
	}
}

func TestUsersHTTPRejectsUnauthorizedUnsupportedAndOversizedRequests(t *testing.T) {
	adapter := newLifecycleStub()
	handler, err := NewHandler(Config{
		ConnectorID: "workforce", Issuer: "https://idp.example.com", SubjectAttribute: "externalId",
		BearerTokens: []string{"scim-secret"}, MaxBodyBytes: 512,
	}, adapter, adapter)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, method, target, body, token string
		want                              int
	}{
		{name: "missing bearer", method: http.MethodGet, target: "/scim/v2/Users?filter=userName%20eq%20%22alice%22", want: http.StatusUnauthorized},
		{name: "role injection", method: http.MethodPost, target: "/scim/v2/Users", token: "scim-secret", want: http.StatusBadRequest,
			body: `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"alice","externalId":"subject","active":true,"roles":[{"value":"admin"}]}`},
		{name: "unsupported filter", method: http.MethodGet, target: "/scim/v2/Users?filter=emails.value%20eq%20%22alice%22", token: "scim-secret", want: http.StatusBadRequest},
		{name: "oversized", method: http.MethodPost, target: "/scim/v2/Users", token: "scim-secret", body: `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],"userName":"` + strings.Repeat("x", 513) + `"}`, want: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := scimRequest(t, handler, test.method, test.target, test.body, test.token)
			if response.Code != test.want || response.Header().Get("Content-Type") != SCIMContentType {
				t.Fatalf("status=%d content-type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
			}
			if strings.Contains(response.Body.String(), "scim-secret") || strings.Contains(response.Body.String(), "subject") {
				t.Fatalf("SCIM error leaked credential or subject: %s", response.Body.String())
			}
		})
	}
}

func scimRequest(t *testing.T, handler http.Handler, method, target, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func commandString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
