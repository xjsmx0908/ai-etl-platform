package knowledgecatalog_test

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v5"

	"ai-etl-pipeline/internal/knowledgecatalog"
)

func TestPostgresCatalogResolvesExplicitMemberSpace(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT id, tenant_id, name, kind, is_default, active").
		WithArgs("acme", "hr").
		WillReturnRows(pgxmock.NewRows([]string{"id", "tenant_id", "name", "kind", "is_default", "active"}).
			AddRow("hr", "acme", "人事制度", "production", false, true))
	mock.ExpectQuery("SELECT tenant_id, space_id, user_id::text, role").
		WithArgs("acme", "hr", "00000000-0000-0000-0000-000000000001").
		WillReturnRows(pgxmock.NewRows([]string{"tenant_id", "space_id", "user_id", "role"}).
			AddRow("acme", "hr", "00000000-0000-0000-0000-000000000001", "reader"))

	catalog := knowledgecatalog.New(knowledgecatalog.NewPostgresStore(mock))
	space, err := catalog.Resolve(context.Background(), knowledgecatalog.Principal{
		TenantID: "acme", UserID: "00000000-0000-0000-0000-000000000001", Role: "readonly",
	}, "hr", knowledgecatalog.CapabilityQuery)
	if err != nil || space.ID != "hr" {
		t.Fatalf("Resolve = %+v, %v", space, err)
	}
}

func TestPostgresCatalogFiltersPublishedEvidence(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT doc_id, knowledge_space_id, publication_status, doc_status").
		WithArgs("acme", []string{"good", "draft"}).
		WillReturnRows(pgxmock.NewRows([]string{"doc_id", "knowledge_space_id", "publication_status", "doc_status"}).
			AddRow("good", "hr", "published", "active").
			AddRow("draft", "hr", "draft", "active"))

	decision, err := knowledgecatalog.New(knowledgecatalog.NewPostgresStore(mock)).FilterEvidence(
		context.Background(), "acme", "hr", []string{"good", "draft"},
	)
	if err != nil || !decision.AllowedDocIDs["good"] || decision.UnpublishedFiltered != 1 {
		t.Fatalf("FilterEvidence = %+v, %v", decision, err)
	}
}
