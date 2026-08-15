package audit

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v5"
)

const entryCols = "tenant_id, actor_user_id, actor_role, action, resource_type, " +
	"resource_id, result, detail, created_at"

func TestRecord(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs("acme", "u-1", "admin", "login", "", "", "success", map[string]any{}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	s := New(mock)
	if err := s.Record(context.Background(), Entry{
		TenantID: "acme", ActorUserID: "u-1", ActorRole: "admin", Action: "login",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
}

func TestRecord_DefaultsResultAndDetail(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs("acme", "u-1", "user", "upload", "document", "doc-1", "success", map[string]any{}).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	s := New(mock)
	if err := s.Record(context.Background(), Entry{
		TenantID: "acme", ActorUserID: "u-1", ActorRole: "user", Action: "upload",
		ResourceType: "document", ResourceID: "doc-1",
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
}

func TestList_WithActionFilter(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	now := time.Now()
	mock.ExpectQuery("SELECT "+entryCols).
		WithArgs("acme", "login", 20, 0).
		WillReturnRows(pgxmock.NewRows([]string{"tenant_id", "actor_user_id", "actor_role", "action",
			"resource_type", "resource_id", "result", "detail", "created_at"}).
			AddRow("acme", "u-1", "admin", "login", "", "", "success", map[string]any{}, now))
	mock.ExpectQuery("SELECT count").WithArgs("acme", "login").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))

	s := New(mock)
	entries, total, err := s.List(context.Background(), ListQuery{
		TenantID: "acme", Action: "login", Limit: 20,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(entries) != 1 || entries[0].Action != "login" {
		t.Fatalf("expected 1 login entry, got %d/%d", len(entries), total)
	}
}
