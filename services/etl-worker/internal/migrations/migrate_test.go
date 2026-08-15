package migrations

import (
	"context"
	"testing"

	"github.com/pashagolub/pgxmock/v5"
)

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
		// The whole body executes as one statement; match a distinctive substring.
		mock.ExpectExec(`CREATE TABLE`).
			WillReturnResult(pgxmock.NewResult("CREATE", 0))
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
