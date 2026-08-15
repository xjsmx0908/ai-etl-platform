package userstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v5"
)

const userCols = "id, username, password_hash, role, tenant_id, active, created_at, updated_at"

func userRow() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"id", "username", "password_hash", "role", "tenant_id", "active", "created_at", "updated_at"}).
		AddRow("u-1", "alice", "hash", "admin", "default", true, time.Now(), time.Now())
}

func TestGetByUsername_Found(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT " + userCols).
		WithArgs("alice").
		WillReturnRows(userRow())

	s := New(mock)
	u, found, err := s.GetByUsername(context.Background(), "alice")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if !found || u.Username != "alice" || u.Role != RoleAdmin || !u.Active {
		t.Fatalf("unexpected user: found=%v user=%+v", found, u)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestGetByUsername_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT " + userCols).
		WithArgs("nobody").
		WillReturnRows(pgxmock.NewRows([]string{"id", "username", "password_hash", "role", "tenant_id", "active", "created_at", "updated_at"}))

	s := New(mock)
	_, found, err := s.GetByUsername(context.Background(), "nobody")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if found {
		t.Fatal("expected not found")
	}
}

func TestCreate_BackfillsIDAndTimestamps(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	now := time.Now()
	mock.ExpectQuery("INSERT INTO users").
		WithArgs("bob", "hash", RoleUser, "acme", true).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
			AddRow("u-2", now, now))

	s := New(mock)
	u := &User{Username: "bob", PasswordHash: "hash", Role: RoleUser, TenantID: "acme", Active: true}
	if err := s.Create(context.Background(), u); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if u.ID != "u-2" || u.CreatedAt.IsZero() {
		t.Fatalf("expected backfilled id/timestamps, got %+v", u)
	}
}

func TestUpdate_PartialPatch(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	role := RoleReadonly
	mock.ExpectQuery("UPDATE users SET").
		WithArgs(RoleReadonly, "u-1").
		WillReturnRows(userRow())

	s := New(mock)
	got, err := s.Update(context.Background(), "u-1", UserPatch{Role: &role})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Username != "alice" {
		t.Fatalf("expected refreshed user, got %+v", got)
	}
}

func TestUpdate_EmptyPatchReturnsCurrent(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT " + userCols).
		WithArgs("u-1").
		WillReturnRows(userRow())

	s := New(mock)
	got, err := s.Update(context.Background(), "u-1", UserPatch{})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.ID != "u-1" {
		t.Fatalf("expected unchanged user, got %+v", got)
	}
}

func TestCountUsers(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT count").WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))

	s := New(mock)
	n, err := s.CountUsers(context.Background())
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if n != 3 {
		t.Fatalf("expected 3 users, got %d", n)
	}
}

func TestList_ReturnsUsersAndTotal(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT "+userCols).
		WithArgs("default", 20, 0).
		WillReturnRows(userRow().AddRow("u-2", "bob", "h", "user", "default", false, time.Now(), time.Now()))
	mock.ExpectQuery("SELECT count").WithArgs("default").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))

	s := New(mock)
	users, total, err := s.List(context.Background(), "default", 20, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 2 || len(users) != 2 {
		t.Fatalf("expected 2/2, got %d/%d", len(users), total)
	}
}

func TestCreateTenant_DuplicateReturnsError(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer mock.Close()
	mock.ExpectExec("INSERT INTO tenants").
		WithArgs("acme", "Acme Corp").
		WillReturnError(errors.New("duplicate key value violates unique constraint"))

	s := New(mock)
	err = s.CreateTenant(context.Background(), "acme", "Acme Corp")
	if err == nil {
		t.Fatal("expected duplicate tenant to error")
	}
}
