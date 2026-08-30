package externalidentity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"ai-etl-pipeline/internal/auth"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresManagerCommitsBindingAuditAndCurrentAuthority(t *testing.T) {
	pool, cleanup := externalIdentityPool(t)
	defer cleanup()
	seedExternalIdentityUsers(t, pool)
	manager := NewPostgresManager(pool)
	actor := auth.Principal{TenantID: "acme", SubjectID: "00000000-0000-0000-0000-000000000001", Role: "admin"}

	binding, err := manager.Bind(context.Background(), actor, BindRequest{
		InternalUserID: "00000000-0000-0000-0000-000000000002",
		Identity:       ExternalIdentity{Issuer: "HTTPS://LOGIN.Example.COM/Tenant-A", Subject: "Subject-A"},
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if binding.ID == "" || binding.Issuer != "https://login.example.com/Tenant-A" ||
		binding.Subject != "Subject-A" || binding.TenantID != "acme" {
		t.Fatalf("unexpected binding: %+v", binding)
	}
	items, err := manager.List(context.Background(), actor, binding.InternalUserID)
	if err != nil || len(items) != 1 || items[0].ID != binding.ID {
		t.Fatalf("List items=%+v err=%v", items, err)
	}
	var audits, leakedSubjects int
	if err := pool.QueryRow(context.Background(), `SELECT count(*),
		count(*) FILTER (WHERE detail::text LIKE '%Subject-A%')
		FROM audit_logs WHERE action='external_identity.binding.create'`).Scan(&audits, &leakedSubjects); err != nil {
		t.Fatal(err)
	}
	if audits != 1 || leakedSubjects != 0 {
		t.Fatalf("audits=%d leaked_subjects=%d", audits, leakedSubjects)
	}

	directory := NewPostgresDirectory(pool)
	principal, err := directory.Resolve(context.Background(), binding.ExternalIdentity)
	if err != nil || principal.Role != "user" || principal.TenantID != "acme" {
		t.Fatalf("initial Resolve principal=%+v err=%v", principal, err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE users SET role='readonly' WHERE id=$1`, binding.InternalUserID); err != nil {
		t.Fatal(err)
	}
	principal, err = directory.Resolve(context.Background(), binding.ExternalIdentity)
	if err != nil || principal.Role != "readonly" || len(principal.Capabilities) != 1 || principal.Capabilities[0] != auth.ScopeQuery {
		t.Fatalf("updated Resolve principal=%+v err=%v", principal, err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE users SET active=false WHERE id=$1`, binding.InternalUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := directory.Resolve(context.Background(), binding.ExternalIdentity); !errors.Is(err, ErrInactive) {
		t.Fatalf("inactive Resolve error=%v", err)
	}
}

func TestPostgresManagerFailsClosedForConflictsTenantsAndAudit(t *testing.T) {
	t.Run("global identity conflict", func(t *testing.T) {
		pool, cleanup := externalIdentityPool(t)
		defer cleanup()
		seedExternalIdentityUsers(t, pool)
		manager := NewPostgresManager(pool)
		actor := auth.Principal{TenantID: "acme", SubjectID: "00000000-0000-0000-0000-000000000001", Role: "admin"}
		request := BindRequest{InternalUserID: "00000000-0000-0000-0000-000000000002", Identity: ExternalIdentity{Issuer: "https://idp.example.com", Subject: "alice"}}
		if _, err := manager.Bind(context.Background(), actor, request); err != nil {
			t.Fatal(err)
		}
		request.InternalUserID = "00000000-0000-0000-0000-000000000003"
		if _, err := manager.Bind(context.Background(), actor, request); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate Bind error=%v", err)
		}
	})

	t.Run("tenant isolation", func(t *testing.T) {
		pool, cleanup := externalIdentityPool(t)
		defer cleanup()
		seedExternalIdentityUsers(t, pool)
		manager := NewPostgresManager(pool)
		acme := auth.Principal{TenantID: "acme", SubjectID: "00000000-0000-0000-0000-000000000001", Role: "admin"}
		other := auth.Principal{TenantID: "other", SubjectID: "00000000-0000-0000-0000-000000000004", Role: "admin"}
		if _, err := manager.Bind(context.Background(), acme, BindRequest{InternalUserID: other.SubjectID, Identity: ExternalIdentity{Issuer: "https://idp.example.com", Subject: "other"}}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-tenant Bind error=%v", err)
		}
		binding, err := manager.Bind(context.Background(), other, BindRequest{InternalUserID: other.SubjectID, Identity: ExternalIdentity{Issuer: "https://idp.example.com", Subject: "other"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.List(context.Background(), acme, other.SubjectID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-tenant List error=%v", err)
		}
		if err := manager.Delete(context.Background(), acme, other.SubjectID, binding.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-tenant Delete error=%v", err)
		}
		if err := manager.Delete(context.Background(), other, other.SubjectID, binding.ID); err != nil {
			t.Fatalf("owner Delete: %v", err)
		}
		var bindings, audits int
		if err := pool.QueryRow(context.Background(), `SELECT
			(SELECT count(*) FROM external_identity_bindings),
			(SELECT count(*) FROM audit_logs WHERE action='external_identity.binding.delete')`).Scan(&bindings, &audits); err != nil {
			t.Fatal(err)
		}
		if bindings != 0 || audits != 1 {
			t.Fatalf("bindings=%d delete_audits=%d", bindings, audits)
		}
	})

	t.Run("audit rollback and admin enforcement", func(t *testing.T) {
		pool, cleanup := externalIdentityPool(t)
		defer cleanup()
		seedExternalIdentityUsers(t, pool)
		manager := NewPostgresManager(pool)
		user := auth.Principal{TenantID: "acme", SubjectID: "00000000-0000-0000-0000-000000000002", Role: "user"}
		request := BindRequest{InternalUserID: user.SubjectID, Identity: ExternalIdentity{Issuer: "https://idp.example.com", Subject: "alice"}}
		if _, err := manager.Bind(context.Background(), user, request); !errors.Is(err, ErrForbidden) {
			t.Fatalf("non-admin Bind error=%v", err)
		}
		if _, err := pool.Exec(context.Background(), `DROP TABLE audit_logs`); err != nil {
			t.Fatal(err)
		}
		admin := auth.Principal{TenantID: "acme", SubjectID: "00000000-0000-0000-0000-000000000001", Role: "admin"}
		if _, err := manager.Bind(context.Background(), admin, request); err == nil {
			t.Fatal("Bind succeeded without durable audit")
		}
		var bindings int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM external_identity_bindings`).Scan(&bindings); err != nil {
			t.Fatal(err)
		}
		if bindings != 0 {
			t.Fatalf("binding committed despite audit failure: %d", bindings)
		}
	})

	t.Run("delete audit rollback", func(t *testing.T) {
		pool, cleanup := externalIdentityPool(t)
		defer cleanup()
		seedExternalIdentityUsers(t, pool)
		manager := NewPostgresManager(pool)
		admin := auth.Principal{TenantID: "acme", SubjectID: "00000000-0000-0000-0000-000000000001", Role: "admin"}
		binding, err := manager.Bind(context.Background(), admin, BindRequest{
			InternalUserID: "00000000-0000-0000-0000-000000000002",
			Identity:       ExternalIdentity{Issuer: "https://idp.example.com", Subject: "alice"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(context.Background(), `DROP TABLE audit_logs`); err != nil {
			t.Fatal(err)
		}
		if err := manager.Delete(context.Background(), admin, binding.InternalUserID, binding.ID); err == nil {
			t.Fatal("Delete succeeded without durable audit")
		}
		var bindings int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM external_identity_bindings WHERE id=$1`, binding.ID).Scan(&bindings); err != nil {
			t.Fatal(err)
		}
		if bindings != 1 {
			t.Fatalf("binding deletion committed despite audit failure: %d", bindings)
		}
	})
}

func seedExternalIdentityUsers(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO tenants(id) VALUES ('acme'),('other');
		INSERT INTO users(id,tenant_id,role,active) VALUES
		('00000000-0000-0000-0000-000000000001','acme','admin',true),
		('00000000-0000-0000-0000-000000000002','acme','user',true),
		('00000000-0000-0000-0000-000000000003','acme','user',true),
		('00000000-0000-0000-0000-000000000004','other','admin',true);`)
	if err != nil {
		t.Fatal(err)
	}
}

func externalIdentityPool(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	dsn := os.Getenv("EXTERNAL_IDENTITY_TEST_DSN")
	if dsn == "" {
		t.Skip("set EXTERNAL_IDENTITY_TEST_DSN to run PostgreSQL external identity tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("externalidentity_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
		CREATE TABLE tenants(id TEXT PRIMARY KEY);
		CREATE TABLE users(id UUID PRIMARY KEY,tenant_id TEXT NOT NULL REFERENCES tenants(id),role TEXT NOT NULL,active BOOLEAN NOT NULL);
		CREATE UNIQUE INDEX users_identity_tenant_key ON users(id,tenant_id);
		CREATE TABLE external_identity_bindings(
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),issuer TEXT NOT NULL,external_subject TEXT NOT NULL,
			internal_user_id UUID NOT NULL,tenant_id TEXT NOT NULL,created_by TEXT NOT NULL DEFAULT '',created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			UNIQUE(issuer,external_subject),FOREIGN KEY(internal_user_id,tenant_id) REFERENCES users(id,tenant_id) ON DELETE CASCADE);
		CREATE TABLE audit_logs(
			id BIGSERIAL PRIMARY KEY,tenant_id TEXT NOT NULL DEFAULT '',actor_user_id TEXT NOT NULL DEFAULT '',actor_role TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL,resource_type TEXT NOT NULL DEFAULT '',resource_id TEXT NOT NULL DEFAULT '',result TEXT NOT NULL DEFAULT 'success',
			detail JSONB NOT NULL DEFAULT '{}',created_at TIMESTAMPTZ NOT NULL DEFAULT now());`)
	if err != nil {
		t.Fatal(err)
	}
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	}
}
