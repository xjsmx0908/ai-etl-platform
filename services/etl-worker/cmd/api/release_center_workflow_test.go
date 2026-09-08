package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/publicationworkflow"
	"ai-etl-pipeline/internal/releasecenter"
	"ai-etl-pipeline/internal/userstore"
)

func TestReleaseCenterWorkflowDecisionPublishesThroughExistingApproval(t *testing.T) {
	candidate := functionalCandidate()
	workflow := &functionalReleaseWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newFunctionalReleaseStore()
	coordinator := releasecenter.NewCoordinator(
		workflow,
		functionalReleaseDocuments{document: docstore.Document{TenantID: "tenant-functional", DocID: candidate.DocumentID, Permission: "internal"}},
		functionalReleaseReviewer{review: releasecenter.AgentReview{Status: "completed", Recommendation: "publish", RiskLevel: releasecenter.RiskHigh}},
		store,
	)
	created := doRequest(handleReleaseCenterReview(coordinator), http.MethodPost, "/v1/release-center/reviews/"+candidate.DocumentID, nil, functionalAdminContext("requester"))
	if created.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", created.Code, created.Body.String())
	}
	var body struct {
		Request releasecenter.ReleaseRequest `json:"request"`
	}
	if err := json.NewDecoder(created.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Request.RequiredApprovals != 2 {
		t.Fatalf("expected dual approval, got %+v", body.Request)
	}

	users := newFakeUserStore()
	seedWorkflowUser(users, userstore.User{ID: "admin-1", Username: "alice", Role: userstore.RoleAdmin, TenantID: "tenant-functional", Active: true})
	seedWorkflowUser(users, userstore.User{ID: "admin-2", Username: "bob", Role: userstore.RoleAdmin, TenantID: "tenant-functional", Active: true})
	handler := handleReleaseCenterWorkflowDecision(store, workflow, users, nil, "workflow-token")

	first := doWorkflowCallback(handler, "workflow-token", map[string]string{
		"tenant_id": "tenant-functional", "request_id": body.Request.ID, "actor_user_id": "alice", "decision": "approved",
	})
	if first.Code != http.StatusOK || workflow.published != 0 {
		t.Fatalf("first status=%d published=%d body=%s", first.Code, workflow.published, first.Body.String())
	}
	replay := doWorkflowCallback(handler, "workflow-token", map[string]string{
		"tenant_id": "tenant-functional", "request_id": body.Request.ID, "actor_user_id": "admin-1", "decision": "approved",
	})
	if replay.Code != http.StatusOK || workflow.published != 0 {
		t.Fatalf("replay status=%d published=%d body=%s", replay.Code, workflow.published, replay.Body.String())
	}
	second := doWorkflowCallback(handler, "workflow-token", map[string]string{
		"tenant_id": "tenant-functional", "request_id": body.Request.ID, "actor_user_id": "bob", "decision": "approved",
	})
	if second.Code != http.StatusOK || workflow.published != 1 {
		t.Fatalf("second status=%d published=%d body=%s", second.Code, workflow.published, second.Body.String())
	}
}

func TestReleaseCenterWorkflowDecisionPreservesApprovalGuards(t *testing.T) {
	candidate := functionalCandidate()
	workflow := &functionalReleaseWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newFunctionalReleaseStore()
	review := releasecenter.ReviewReport{ID: "review-self", TenantID: "tenant-functional", DocumentID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, ReleaseRevision: candidate.ReleaseRevision, Status: "completed", Recommendation: "publish", RiskLevel: releasecenter.RiskLow}
	request := releasecenter.ReleaseRequest{ID: "request-self", TenantID: "tenant-functional", DocumentID: candidate.DocumentID, Candidate: candidate, ReviewID: review.ID, RequiredApprovals: 1, State: releasecenter.RequestApprovalPending, RequestedBy: "admin-self"}
	store.reviews[review.ID] = review
	store.requests[request.ID] = request

	users := newFakeUserStore()
	seedWorkflowUser(users, userstore.User{ID: "admin-self", Username: "self", Role: userstore.RoleAdmin, TenantID: "tenant-functional", Active: true})
	seedWorkflowUser(users, userstore.User{ID: "reader", Username: "reader", Role: userstore.RoleUser, TenantID: "tenant-functional", Active: true})
	seedWorkflowUser(users, userstore.User{ID: "other-admin", Username: "other", Role: userstore.RoleAdmin, TenantID: "other-tenant", Active: true})
	seedWorkflowUser(users, userstore.User{ID: "inactive", Username: "inactive", Role: userstore.RoleAdmin, TenantID: "tenant-functional", Active: false})

	tests := []struct {
		name   string
		token  string
		body   map[string]string
		status int
	}{
		{name: "unconfigured", token: "", body: map[string]string{"tenant_id": "tenant-functional", "request_id": request.ID, "actor_user_id": "admin-self", "decision": "approved"}, status: http.StatusServiceUnavailable},
		{name: "wrong token", token: "wrong", body: map[string]string{"tenant_id": "tenant-functional", "request_id": request.ID, "actor_user_id": "admin-self", "decision": "approved"}, status: http.StatusUnauthorized},
		{name: "missing actor", token: "workflow-token", body: map[string]string{"tenant_id": "tenant-functional", "request_id": request.ID, "decision": "approved"}, status: http.StatusBadRequest},
		{name: "unknown actor", token: "workflow-token", body: map[string]string{"tenant_id": "tenant-functional", "request_id": request.ID, "actor_user_id": "nobody", "decision": "approved"}, status: http.StatusForbidden},
		{name: "non-admin", token: "workflow-token", body: map[string]string{"tenant_id": "tenant-functional", "request_id": request.ID, "actor_user_id": "reader", "decision": "approved"}, status: http.StatusForbidden},
		{name: "cross-tenant", token: "workflow-token", body: map[string]string{"tenant_id": "tenant-functional", "request_id": request.ID, "actor_user_id": "other-admin", "decision": "approved"}, status: http.StatusForbidden},
		{name: "inactive", token: "workflow-token", body: map[string]string{"tenant_id": "tenant-functional", "request_id": request.ID, "actor_user_id": "inactive", "decision": "approved"}, status: http.StatusForbidden},
		{name: "self-approval", token: "workflow-token", body: map[string]string{"tenant_id": "tenant-functional", "request_id": request.ID, "actor_user_id": "admin-self", "decision": "approved"}, status: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			token := "workflow-token"
			callbackToken := "workflow-token"
			if test.name == "unconfigured" {
				callbackToken = ""
			}
			if test.name == "wrong token" {
				token = test.token
			}
			handler := handleReleaseCenterWorkflowDecision(store, workflow, users, nil, callbackToken)
			response := doWorkflowCallback(handler, token, test.body)
			if response.Code != test.status || workflow.published != 0 || len(store.decisions[request.ID]) != 0 {
				t.Fatalf("status=%d published=%d decisions=%d body=%s", response.Code, workflow.published, len(store.decisions[request.ID]), response.Body.String())
			}
		})
	}
}

func TestReleaseCenterWorkflowDecisionUnknownRequestIsNotFound(t *testing.T) {
	workflow := &functionalReleaseWorkflow{}
	store := newFunctionalReleaseStore()
	users := newFakeUserStore()
	seedWorkflowUser(users, userstore.User{ID: "admin-1", Username: "alice", Role: userstore.RoleAdmin, TenantID: "tenant-functional", Active: true})
	response := doWorkflowCallback(handleReleaseCenterWorkflowDecision(store, workflow, users, nil, "workflow-token"), "workflow-token", map[string]string{
		"tenant_id": "tenant-functional", "request_id": "missing", "actor_user_id": "alice", "decision": "approved",
	})
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func seedWorkflowUser(store *fakeUserStore, user userstore.User) {
	store.byID[user.ID] = user
	store.byName[strings.ToLower(user.Username)] = user
}

func doWorkflowCallback(handler http.Handler, token string, body map[string]string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/release-center/workflow/decision", &buf)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
