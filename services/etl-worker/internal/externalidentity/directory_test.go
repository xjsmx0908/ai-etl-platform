package externalidentity

import (
	"context"
	"errors"
	"testing"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/userstore"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v5"
)

func TestNormalizeExternalIdentityPreservesSubjectAndIssuerPath(t *testing.T) {
	got, err := Normalize(ExternalIdentity{
		Issuer:  "  HTTPS://LOGIN.Example.COM/Tenant-A/  ",
		Subject: "Case-Sensitive Subject",
	})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got.Issuer != "https://login.example.com/Tenant-A/" {
		t.Fatalf("issuer=%q", got.Issuer)
	}
	if got.Subject != "Case-Sensitive Subject" {
		t.Fatalf("subject identity changed: %q", got.Subject)
	}
}

func TestNormalizeExternalIdentityRejectsAmbiguousValues(t *testing.T) {
	for name, input := range map[string]ExternalIdentity{
		"relative issuer": {Issuer: "login.example.com", Subject: "alice"},
		"insecure issuer": {Issuer: "http://login.example.com", Subject: "alice"},
		"issuer query":    {Issuer: "https://login.example.com?tenant=a", Subject: "alice"},
		"issuer fragment": {Issuer: "https://login.example.com#issuer", Subject: "alice"},
		"empty subject":   {Issuer: "https://login.example.com", Subject: "   "},
		"padded subject":  {Issuer: "https://login.example.com", Subject: " alice "},
		"control subject": {Issuer: "https://login.example.com", Subject: "alice\nadmin"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Normalize(input); err == nil {
				t.Fatal("accepted invalid external identity")
			}
		})
	}
}

func TestPostgresDirectoryResolveUsesCurrentInternalAuthority(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT u.id,u.tenant_id,u.role,u.active").
		WithArgs("https://login.example.com/Tenant-A", "Subject-A").
		WillReturnRows(pgxmock.NewRows([]string{"id", "tenant_id", "role", "active"}).
			AddRow("user-1", "acme", userstore.RoleReadonly, true))

	principal, err := NewPostgresDirectory(mock).Resolve(context.Background(), ExternalIdentity{
		Issuer: "HTTPS://LOGIN.EXAMPLE.COM/Tenant-A", Subject: "Subject-A",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if principal.SubjectID != "user-1" || principal.TenantID != "acme" ||
		principal.Role != userstore.RoleReadonly ||
		principal.AuthenticationMethod != auth.AuthenticationMethodFederated ||
		len(principal.Capabilities) != 1 || principal.Capabilities[0] != auth.ScopeQuery {
		t.Fatalf("unexpected principal: %+v", principal)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresDirectoryResolveFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		rows     *pgxmock.Rows
		queryErr error
		want     error
	}{
		"unknown":  {queryErr: pgx.ErrNoRows, want: ErrNotFound},
		"inactive": {rows: pgxmock.NewRows([]string{"id", "tenant_id", "role", "active"}).AddRow("user-1", "acme", "admin", false), want: ErrInactive},
		"bad role": {rows: pgxmock.NewRows([]string{"id", "tenant_id", "role", "active"}).AddRow("user-1", "acme", "owner", true), want: ErrInvalidAuthority},
		"database": {queryErr: errors.New("database unavailable"), want: ErrUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			mock, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer mock.Close()
			expectation := mock.ExpectQuery("SELECT u.id,u.tenant_id,u.role,u.active").
				WithArgs("https://login.example.com", "alice")
			if tc.queryErr != nil {
				expectation.WillReturnError(tc.queryErr)
			} else {
				expectation.WillReturnRows(tc.rows)
			}
			_, err = NewPostgresDirectory(mock).Resolve(context.Background(), ExternalIdentity{
				Issuer: "https://login.example.com", Subject: "alice",
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Resolve error=%v, want %v", err, tc.want)
			}
		})
	}
}
