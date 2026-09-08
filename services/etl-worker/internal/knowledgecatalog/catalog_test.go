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

func TestCreatePersistsOwnerPurpose(t *testing.T) {
	store := knowledgecatalog.NewMemoryStore(nil, nil, nil)
	catalog := knowledgecatalog.New(store)
	space, err := catalog.Create(context.Background(), knowledgecatalog.Principal{TenantID: "acme", UserID: "admin-1", Role: "admin"}, knowledgecatalog.Space{
		ID: "hr", Name: "人事制度", Kind: knowledgecatalog.SpaceKindProduction, Purpose: "  只放已生效的人事制度  ",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if space.Purpose != "只放已生效的人事制度" {
		t.Fatalf("purpose=%q", space.Purpose)
	}
	loaded, found, err := catalog.Space(context.Background(), "acme", "hr")
	if err != nil || !found || loaded.Purpose != "只放已生效的人事制度" {
		t.Fatalf("Space=%+v found=%v err=%v", loaded, found, err)
	}
}

func TestUpdatePurposeRejectsNonAdmin(t *testing.T) {
	store := knowledgecatalog.NewMemoryStore([]knowledgecatalog.Space{{ID: "hr", TenantID: "acme", Name: "人事", Kind: knowledgecatalog.SpaceKindProduction, Active: true}}, nil, nil)
	_, err := knowledgecatalog.New(store).UpdatePurpose(context.Background(), knowledgecatalog.Principal{TenantID: "acme", UserID: "alice", Role: "user"}, "hr", "只放制度")
	if !errors.Is(err, knowledgecatalog.ErrForbidden) {
		t.Fatalf("expected forbidden, got %v", err)
	}
}

func TestUpdatePurposeRewritesMatchingStandard(t *testing.T) {
	store := knowledgecatalog.NewMemoryStore([]knowledgecatalog.Space{{ID: "hr", TenantID: "acme", Name: "人事", Kind: knowledgecatalog.SpaceKindProduction, Active: true}}, nil, nil)
	space, err := knowledgecatalog.New(store).UpdatePurpose(context.Background(), knowledgecatalog.Principal{TenantID: "acme", UserID: "admin-1", Role: "admin"}, "hr", "只放已生效的人事制度")
	if err != nil || space.Purpose != "只放已生效的人事制度" {
		t.Fatalf("UpdatePurpose=%+v err=%v", space, err)
	}
}
