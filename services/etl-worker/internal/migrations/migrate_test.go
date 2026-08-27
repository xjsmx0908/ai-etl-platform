package migrations

import (
	"context"
	"strings"
	"testing"

	"github.com/pashagolub/pgxmock/v5"
)

func TestKnowledgeSpacesMigrationCarriesEnterpriseInvariants(t *testing.T) {
	body, err := migrationFiles.ReadFile("0005_knowledge_spaces.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE TABLE knowledge_spaces",
		"CREATE TABLE knowledge_space_members",
		"ADD COLUMN knowledge_space_id",
		"ADD COLUMN publication_status",
		"FOREIGN KEY (tenant_id, knowledge_space_id)",
		"publication_status IN ('draft','published','retired')",
		"metadata->>'knowledge_base_id'",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration missing invariant %q", required)
		}
	}
}

func TestNewTenantMigrationCreatesDefaultKnowledgeSpace(t *testing.T) {
	body, err := migrationFiles.ReadFile("0006_tenant_default_knowledge_space.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(body)
	for _, required := range []string{
		"AFTER INSERT ON tenants",
		"INSERT INTO knowledge_spaces",
		"NEW.id",
		"'user-uploads'",
		"'production'",
		"ON CONFLICT (tenant_id, id) DO NOTHING",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration missing new-tenant invariant %q", required)
		}
	}
}

func TestIngestionOutboxMigrationCarriesDurabilityInvariants(t *testing.T) {
	body, err := migrationFiles.ReadFile("0008_ingestion_outbox.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE TABLE ingestion_jobs",
		"CREATE TABLE ingestion_outbox",
		"event_id     TEXT NOT NULL UNIQUE",
		"request_signature TEXT NOT NULL",
		"published_at  TIMESTAMPTZ",
		"WHERE published_at IS NULL",
		"REFERENCES documents(tenant_id, doc_id) ON DELETE CASCADE",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration missing invariant %q", required)
		}
	}
}

// TestApplyAll_Unapplied runs migrations against a mock connection that reports
// every migration as unapplied, and asserts each one is applied in a transaction
// and recorded in schema_migrations.
func TestApplyAll_Unapplied(t *testing.T) {
	mock, err := pgxmock.NewConn()
	if err != nil {
		t.Fatalf("new mock conn: %v", err)
	}
	defer mock.Close(context.Background())

	expectLockAndSchema(mock)
	files := sortedUpFiles()
	if len(files) == 0 {
		t.Fatal("expected at least one embedded migration")
	}
	for _, name := range files {
		// Not yet applied → run inside a transaction.
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs(name).
			WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
		mock.ExpectBegin()
		// The whole body executes as one statement; bodies vary (CREATE TABLE,
		// ALTER TABLE, ...) so match any non-empty statement.
		mock.ExpectExec(`.+`).
			WillReturnResult(pgxmock.NewResult("EXEC", 0))
		mock.ExpectExec("INSERT INTO schema_migrations").
			WithArgs(name).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		mock.ExpectCommit()
	}
	// Best-effort unlock after the work.
	mock.ExpectExec("SELECT pg_advisory_unlock").
		WithArgs(advisoryLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))

	if err := applyAll(context.Background(), mock); err != nil {
		t.Fatalf("applyAll: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestApplyAll_AlreadyApplied asserts that applied migrations are skipped: no
// transaction is opened and no INSERT into schema_migrations happens.
func TestApplyAll_AlreadyApplied(t *testing.T) {
	mock, err := pgxmock.NewConn()
	if err != nil {
		t.Fatalf("new mock conn: %v", err)
	}
	defer mock.Close(context.Background())

	expectLockAndSchema(mock)
	for _, name := range sortedUpFiles() {
		mock.ExpectQuery("SELECT EXISTS").
			WithArgs(name).
			WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
	}
	mock.ExpectExec("SELECT pg_advisory_unlock").
		WithArgs(advisoryLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))

	if err := applyAll(context.Background(), mock); err != nil {
		t.Fatalf("applyAll: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func expectLockAndSchema(mock pgxmock.PgxConnIface) {
	mock.ExpectExec("SELECT pg_advisory_lock").
		WithArgs(advisoryLockKey).
		WillReturnResult(pgxmock.NewResult("SELECT", 1))
	mock.ExpectExec("CREATE TABLE IF NOT EXISTS schema_migrations").
		WillReturnResult(pgxmock.NewResult("CREATE", 0))
}
