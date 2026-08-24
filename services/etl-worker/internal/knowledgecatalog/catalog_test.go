package knowledgecatalog_test

import (
	"context"
	"errors"
	"testing"

	"ai-etl-pipeline/internal/knowledgecatalog"
)

func TestResolveUsesAuthorizedDefaultProductionSpace(t *testing.T) {
	store := knowledgecatalog.NewMemoryStore(
		[]knowledgecatalog.Space{
			{ID: "prod-space", TenantID: "acme", Slug: "user-uploads", Name: "用户上传", Kind: knowledgecatalog.SpaceKindProduction, IsDefault: true, Active: true},
			{ID: "demo-space", TenantID: "acme", Slug: "enterprise-demo", Name: "演示知识库", Kind: knowledgecatalog.SpaceKindDemo, Active: true},
		},
		[]knowledgecatalog.Membership{
			{SpaceID: "prod-space", UserID: "alice", Role: knowledgecatalog.MemberReader},
		},
		nil,
	)
	catalog := knowledgecatalog.New(store)

	space, err := catalog.Resolve(context.Background(), knowledgecatalog.Principal{
		TenantID: "acme",
		UserID:   "alice",
		Role:     "readonly",
	}, "", knowledgecatalog.CapabilityQuery)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if space.ID != "prod-space" {
		t.Fatalf("expected prod-space, got %+v", space)
	}
}

func TestFilterEvidenceAllowsOnlyPublishedActiveDocumentsInResolvedSpace(t *testing.T) {
	store := knowledgecatalog.NewMemoryStore(nil, nil, []knowledgecatalog.DocumentPolicy{
		{DocID: "published", KnowledgeSpaceID: "prod", PublicationStatus: "published", DocumentStatus: "active"},
		{DocID: "draft", KnowledgeSpaceID: "prod", PublicationStatus: "draft", DocumentStatus: "active"},
		{DocID: "retired", KnowledgeSpaceID: "prod", PublicationStatus: "published", DocumentStatus: "archived"},
		{DocID: "other-space", KnowledgeSpaceID: "other", PublicationStatus: "published", DocumentStatus: "active"},
	})
	catalog := knowledgecatalog.New(store)

	decision, err := catalog.FilterEvidence(context.Background(), "acme", "prod", []string{"published", "draft", "retired", "other-space", "missing"})
	if err != nil {
		t.Fatalf("FilterEvidence: %v", err)
	}
	if !decision.AllowedDocIDs["published"] || len(decision.AllowedDocIDs) != 1 {
		t.Fatalf("unexpected allowed set: %+v", decision.AllowedDocIDs)
	}
	if decision.UnpublishedFiltered != 3 || decision.RetiredFiltered != 1 {
		t.Fatalf("unexpected exclusion counts: %+v", decision)
	}
}

func TestFilterEvidenceFailsClosedWithoutCatalog(t *testing.T) {
	_, err := knowledgecatalog.New(nil).FilterEvidence(context.Background(), "acme", "prod", []string{"doc-1"})
	if !errors.Is(err, knowledgecatalog.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestResolveRejectsDemoSpaceForReaderWithoutMembership(t *testing.T) {
	store := knowledgecatalog.NewMemoryStore(
		[]knowledgecatalog.Space{{ID: "demo", TenantID: "acme", Kind: knowledgecatalog.SpaceKindDemo, Active: true}},
		nil,
		nil,
	)
	catalog := knowledgecatalog.New(store)
	_, err := catalog.Resolve(context.Background(), knowledgecatalog.Principal{TenantID: "acme", UserID: "alice", Role: "user"}, "demo", knowledgecatalog.CapabilityQuery)
	if err == nil {
		t.Fatal("expected membership denial")
	}
}

func TestResolveRejectsUploadForReader(t *testing.T) {
	store := knowledgecatalog.NewMemoryStore(
		[]knowledgecatalog.Space{{ID: "prod", TenantID: "acme", Kind: knowledgecatalog.SpaceKindProduction, Active: true}},
		[]knowledgecatalog.Membership{{SpaceID: "prod", UserID: "alice", Role: knowledgecatalog.MemberReader}},
		nil,
	)
	catalog := knowledgecatalog.New(store)
	_, err := catalog.Resolve(context.Background(), knowledgecatalog.Principal{TenantID: "acme", UserID: "alice", Role: "user"}, "prod", knowledgecatalog.CapabilityUpload)
	if err == nil {
		t.Fatal("expected upload denial")
	}
}

func TestResolveFailsClosedWhenStoreUnavailable(t *testing.T) {
	catalog := knowledgecatalog.New(nil)
	_, err := catalog.Resolve(context.Background(), knowledgecatalog.Principal{TenantID: "acme", UserID: "alice", Role: "user"}, "prod", knowledgecatalog.CapabilityQuery)
	if err == nil {
		t.Fatal("expected catalog outage error")
	}
}
