package identitylifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"ai-etl-pipeline/internal/db"
	"ai-etl-pipeline/internal/externalidentity"
	"ai-etl-pipeline/internal/migrations"
)

func TestProvisionerCreateReplayDeactivateTombstoneAndReactivate(t *testing.T) {
	pool, cleanup := lifecyclePool(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES('acme','Acme')`); err != nil {
		t.Fatal(err)
	}
	policy := ConnectorPolicy{
		ID: "workforce", TenantID: "acme", Issuer: "https://idp.example.com/realms/acme",
		SubjectAttribute: "externalId", DefaultRole: "readonly",
	}
	if err := EnsureConnector(ctx, pool, policy); err != nil {
		t.Fatalf("ensure connector: %v", err)
	}
	provisioner := NewPostgresProvisioner(pool)
	create := LifecycleCommand{
		Operation: OperationCreate, ConnectorID: "workforce", ProviderResourceID: "provider-user-42",
		Identity: externalidentity.ExternalIdentity{Issuer: policy.Issuer, Subject: "oidc-subject-42"},
		Username: "alice@example.com", DisplayName: stringPointer("Alice"), Email: stringPointer("alice@example.com"),
		Active: boolPointer(true), SourceVersion: "v1", IdempotencyKey: "create-42",
	}
	created, err := provisioner.Apply(ctx, create)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == "" || created.InternalUserID == "" || created.TenantID != "acme" ||
		created.Role != "readonly" || !created.Active || created.Deleted {
		t.Fatalf("unexpected created result: %+v", created)
	}
	replayed, err := provisioner.Apply(ctx, create)
	if err != nil || replayed != created {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	changed := create
	changed.Username = "mallory@example.com"
	if _, err := provisioner.Apply(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay error=%v", err)
	}

	deactivate := LifecycleCommand{
		Operation: OperationUpdate, ConnectorID: "workforce", ProviderResourceID: "provider-user-42",
		Active: boolPointer(false), SourceVersion: "v2", IdempotencyKey: "deactivate-42",
	}
	deactivated, err := provisioner.Apply(ctx, deactivate)
	if err != nil || deactivated.Active {
		t.Fatalf("deactivate=%+v err=%v", deactivated, err)
	}
	var tokenVersion int
	if err := pool.QueryRow(ctx, `SELECT token_version FROM users WHERE id=$1`, created.InternalUserID).Scan(&tokenVersion); err != nil {
		t.Fatal(err)
	}
	if tokenVersion != 1 {
		t.Fatalf("token_version=%d want 1", tokenVersion)
	}
	deactivate.IdempotencyKey = "deactivate-42-again"
	if _, err := provisioner.Apply(ctx, deactivate); err != nil {
		t.Fatalf("repeat deactivate: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT token_version FROM users WHERE id=$1`, created.InternalUserID).Scan(&tokenVersion); err != nil {
		t.Fatal(err)
	}
	if tokenVersion != 1 {
		t.Fatalf("repeat deactivate token_version=%d want 1", tokenVersion)
	}
	if _, err := externalidentity.NewPostgresDirectory(pool).Resolve(ctx, create.Identity); !errors.Is(err, externalidentity.ErrInactive) {
		t.Fatalf("deactivated identity resolution error=%v", err)
	}

	deleteCommand := LifecycleCommand{
		Operation: OperationDelete, ConnectorID: "workforce", ProviderResourceID: "provider-user-42",
		IdempotencyKey: "delete-42",
	}
	deleted, err := provisioner.Apply(ctx, deleteCommand)
	if err != nil || !deleted.Deleted || deleted.Active {
		t.Fatalf("delete=%+v err=%v", deleted, err)
	}
	var bindings int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM external_identity_bindings
		WHERE issuer=$1 AND external_subject=$2`, create.Identity.Issuer, create.Identity.Subject).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if bindings != 1 {
		t.Fatalf("tombstone binding count=%d", bindings)
	}
	if replayedDelete, replayErr := provisioner.Apply(ctx, deleteCommand); replayErr != nil || replayedDelete != deleted {
		t.Fatalf("delete replay=%+v err=%v", replayedDelete, replayErr)
	}

	reactivated, err := provisioner.Apply(ctx, LifecycleCommand{
		Operation: OperationUpdate, ConnectorID: "workforce", ProviderResourceID: "provider-user-42",
		Identity: create.Identity, Active: boolPointer(true), SourceVersion: "v3", IdempotencyKey: "reactivate-42",
	})
	if err != nil || !reactivated.Active || reactivated.Deleted || reactivated.InternalUserID != created.InternalUserID {
		t.Fatalf("reactivate=%+v err=%v", reactivated, err)
	}
	cleared, err := provisioner.Apply(ctx, LifecycleCommand{
		Operation: OperationUpdate, ConnectorID: "workforce", ProviderResourceID: "provider-user-42",
		DisplayName: stringPointer(""), Email: stringPointer(""), IdempotencyKey: "clear-profile-42",
	})
	if err != nil || cleared.DisplayName != "" || cleared.Email != "" {
		t.Fatalf("clear profile=%+v err=%v", cleared, err)
	}
	var auditCount, identifierLeaks int
	if err := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (
		WHERE detail::text LIKE '%oidc-subject-42%' OR detail::text LIKE '%provider-user-42%') FROM audit_logs
		WHERE action LIKE 'identity.lifecycle.%'`).Scan(&auditCount, &identifierLeaks); err != nil {
		t.Fatal(err)
	}
	if auditCount != 6 || identifierLeaks != 0 {
		t.Fatalf("audit_count=%d identifier_leaks=%d", auditCount, identifierLeaks)
	}
}

func TestProvisionerRejectsUnboundedCommandFields(t *testing.T) {
	pool, cleanup := lifecyclePool(t)
	defer cleanup()
	seedLifecycleConnector(t, pool)
	provisioner := NewPostgresProvisioner(pool)
	command := lifecycleCreate("resource-a", "subject-a", "alice@example.com", "create-a")
	command.IdempotencyKey = strings.Repeat("k", 257)
	if _, err := provisioner.Apply(context.Background(), command); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized idempotency key error=%v", err)
	}
}

func TestProvisionerFailsClosedAndRollsBackLifecycleConflicts(t *testing.T) {
	t.Run("connector policy is immutable", func(t *testing.T) {
		pool, cleanup := lifecyclePool(t)
		defer cleanup()
		ctx := context.Background()
		if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES('acme','Acme'),('other','Other')`); err != nil {
			t.Fatal(err)
		}
		policy := ConnectorPolicy{ID: "workforce", TenantID: "acme", Issuer: "https://idp.example.com", SubjectAttribute: "externalId", DefaultRole: "readonly"}
		if err := EnsureConnector(ctx, pool, policy); err != nil {
			t.Fatal(err)
		}
		policy.TenantID = "other"
		if err := EnsureConnector(ctx, pool, policy); !errors.Is(err, ErrConflict) {
			t.Fatalf("connector rebind error=%v", err)
		}
		var tenant string
		if err := pool.QueryRow(ctx, `SELECT tenant_id FROM identity_provisioning_connectors WHERE id='workforce'`).Scan(&tenant); err != nil {
			t.Fatal(err)
		}
		if tenant != "acme" {
			t.Fatalf("connector tenant changed to %q", tenant)
		}
	})

	t.Run("identity conflict rolls back user", func(t *testing.T) {
		pool, cleanup := lifecyclePool(t)
		defer cleanup()
		ctx := context.Background()
		seedLifecycleConnector(t, pool)
		provisioner := NewPostgresProvisioner(pool)
		first := lifecycleCreate("resource-a", "subject-a", "alice@example.com", "create-a")
		if _, err := provisioner.Apply(ctx, first); err != nil {
			t.Fatal(err)
		}
		second := lifecycleCreate("resource-b", "subject-a", "bob@example.com", "create-b")
		if _, err := provisioner.Apply(ctx, second); !errors.Is(err, ErrConflict) {
			t.Fatalf("subject conflict error=%v", err)
		}
		var users, resources int
		if err := pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM users),(SELECT count(*) FROM identity_lifecycle_resources)`).Scan(&users, &resources); err != nil {
			t.Fatal(err)
		}
		if users != 1 || resources != 1 {
			t.Fatalf("partial conflict state users=%d resources=%d", users, resources)
		}
	})

	t.Run("audit failure rolls back all state", func(t *testing.T) {
		pool, cleanup := lifecyclePool(t)
		defer cleanup()
		ctx := context.Background()
		seedLifecycleConnector(t, pool)
		if _, err := pool.Exec(ctx, `DROP TABLE audit_logs`); err != nil {
			t.Fatal(err)
		}
		if _, err := NewPostgresProvisioner(pool).Apply(ctx, lifecycleCreate("resource-a", "subject-a", "alice@example.com", "create-a")); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("audit failure error=%v", err)
		}
		var users, bindings, resources, replays int
		if err := pool.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM users),(SELECT count(*) FROM external_identity_bindings),
			(SELECT count(*) FROM identity_lifecycle_resources),(SELECT count(*) FROM identity_lifecycle_idempotency)`).
			Scan(&users, &bindings, &resources, &replays); err != nil {
			t.Fatal(err)
		}
		if users+bindings+resources+replays != 0 {
			t.Fatalf("partial audit-failure state users=%d bindings=%d resources=%d replays=%d", users, bindings, resources, replays)
		}
	})
}

func TestProvisionerConcurrentReplayCreatesOneIdentity(t *testing.T) {
	pool, cleanup := lifecyclePool(t)
	defer cleanup()
	seedLifecycleConnector(t, pool)
	command := lifecycleCreate("resource-a", "subject-a", "alice@example.com", "create-a")
	provisioner := NewPostgresProvisioner(pool)
	results := make([]LifecycleResult, 2)
	errorsSeen := make([]error, 2)
	var wait sync.WaitGroup
	for i := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			results[index], errorsSeen[index] = provisioner.Apply(context.Background(), command)
		}(i)
	}
	wait.Wait()
	if errorsSeen[0] != nil || errorsSeen[1] != nil || results[0] != results[1] {
		t.Fatalf("results=%+v errors=%v", results, errorsSeen)
	}
	var users, bindings, resources, audits, replays int
	if err := pool.QueryRow(context.Background(), `SELECT
		(SELECT count(*) FROM users),(SELECT count(*) FROM external_identity_bindings),
		(SELECT count(*) FROM identity_lifecycle_resources),
		(SELECT count(*) FROM audit_logs WHERE action='identity.lifecycle.create'),
		(SELECT count(*) FROM identity_lifecycle_idempotency)`).
		Scan(&users, &bindings, &resources, &audits, &replays); err != nil {
		t.Fatal(err)
	}
	if users != 1 || bindings != 1 || resources != 1 || audits != 1 || replays != 1 {
		t.Fatalf("users=%d bindings=%d resources=%d audits=%d replays=%d", users, bindings, resources, audits, replays)
	}
}

func seedLifecycleConnector(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES('acme','Acme')`); err != nil {
		t.Fatal(err)
	}
	if err := EnsureConnector(ctx, pool, ConnectorPolicy{
		ID: "workforce", TenantID: "acme", Issuer: "https://idp.example.com/realms/acme",
		SubjectAttribute: "externalId", DefaultRole: "readonly",
	}); err != nil {
		t.Fatal(err)
	}
}

func lifecycleCreate(resourceID, subject, username, idempotencyKey string) LifecycleCommand {
	return LifecycleCommand{
		Operation: OperationCreate, ConnectorID: "workforce", ProviderResourceID: resourceID,
		Identity: externalidentity.ExternalIdentity{Issuer: "https://idp.example.com/realms/acme", Subject: subject},
		Username: username, Active: boolPointer(true), IdempotencyKey: idempotencyKey,
	}
}

func boolPointer(value bool) *bool { return &value }

func stringPointer(value string) *string { return &value }

func lifecyclePool(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	dsn := os.Getenv("IDENTITY_LIFECYCLE_TEST_DSN")
	if dsn == "" {
		t.Skip("set IDENTITY_LIFECYCLE_TEST_DSN to run PostgreSQL lifecycle tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("identitylifecycle_%d", time.Now().UnixNano())
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
	if err := migrations.Up(ctx, &db.Pool{Pool: pool}); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	}
}
