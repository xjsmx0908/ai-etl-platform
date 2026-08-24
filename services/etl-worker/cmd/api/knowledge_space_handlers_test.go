package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/knowledgecatalog"
)

func knowledgeContext(tenant, user, role string) context.Context {
	ctx := context.WithValue(context.Background(), auth.CtxTenantID, tenant)
	ctx = context.WithValue(ctx, auth.CtxUserID, user)
	return context.WithValue(ctx, auth.CtxPermission, role)
}

func TestKnowledgeSpacesListsOnlyMemberSpaces(t *testing.T) {
	store := knowledgecatalog.NewMemoryStore(
		[]knowledgecatalog.Space{
			{ID: "hr", TenantID: "acme", Name: "人事", Kind: knowledgecatalog.SpaceKindProduction, Active: true},
			{ID: "finance", TenantID: "acme", Name: "财务", Kind: knowledgecatalog.SpaceKindProduction, Active: true},
		},
		[]knowledgecatalog.Membership{{SpaceID: "hr", UserID: "alice", Role: knowledgecatalog.MemberReader}}, nil,
	)
	rec := doRequest(handleKnowledgeSpaces(knowledgecatalog.New(store)), http.MethodGet, "/v1/knowledge-spaces", nil, knowledgeContext("acme", "alice", "user"))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "finance") {
		t.Fatalf("unexpected response %d: %s", rec.Code, rec.Body.String())
	}
}

func TestKnowledgeSpacesAdminCreatesSpace(t *testing.T) {
	store := knowledgecatalog.NewMemoryStore(nil, nil, nil)
	rec := doRequest(handleKnowledgeSpaces(knowledgecatalog.New(store)), http.MethodPost, "/v1/knowledge-spaces",
		map[string]string{"id": "legal", "name": "法务制度", "kind": "production"}, knowledgeContext("acme", "admin-id", "admin"))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"id":"legal"`) {
		t.Fatalf("unexpected response %d: %s", rec.Code, rec.Body.String())
	}
}
