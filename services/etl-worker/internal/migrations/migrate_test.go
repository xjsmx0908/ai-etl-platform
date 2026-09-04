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

func TestPlatformSessionManagementMigrationUsesIndependentOpaqueHandles(t *testing.T) {
	body, err := migrationFiles.ReadFile("0022_platform_session_management.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"ADD COLUMN management_handle TEXT",
		"SET management_handle = 'sm1_' || gen_random_uuid()::text",
		"ALTER COLUMN management_handle SET NOT NULL",
		"platform_sessions_management_handle_format",
		"platform_sessions_management_handle_unique",
		"CREATE SEQUENCE platform_sessions_creation_order_seq",
		"row_number() OVER (ORDER BY created_at, id)",
		"ALTER COLUMN creation_order SET NOT NULL",
		"platform_sessions_creation_order_unique",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("session management migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"credential_digest::text", "internal_user_id::text", "tenant_id ||"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("session management handle derives from sensitive identity: %q", forbidden)
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

func TestIngestionJobLifecycleMigrationCarriesConsumerInvariants(t *testing.T) {
	body, err := migrationFiles.ReadFile("0009_ingestion_job_lifecycle.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(body)
	for _, required := range []string{
		"'processing'", "processing_started_at", "lease_until", "completed_at",
		"WHERE status = 'processing'",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration missing invariant %q", required)
		}
	}
}

func TestObjectReferenceLookupMigrationSupportsBoundedCleanup(t *testing.T) {
	body, err := migrationFiles.ReadFile("0010_document_object_key_index.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE INDEX", "documents", "object_key", "WHERE object_key <> ''",
		"ingestion_jobs", "task->>'file_path'",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration missing object-reference invariant %q", required)
		}
	}
	if strings.Contains(sql, "WHERE status IN") {
		t.Error("admitted object reference index must include terminal jobs")
	}
}

func TestIndexManifestMigrationCarriesGenerationInvariants(t *testing.T) {
	body, err := migrationFiles.ReadFile("0011_index_manifests.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE TABLE index_manifests", "generation_id TEXT PRIMARY KEY",
		"expected_chunk_digest TEXT NOT NULL", "qdrant_digest TEXT",
		"elasticsearch_digest TEXT", "'building','ready','failed','active','retired'",
		"ingestion_jobs_version_identity_key", "REFERENCES ingestion_jobs",
		"chunker_version <> ''", "embedding_model <> ''", "vector_dimension > 0",
		"qdrant_observed_at", "elasticsearch_observed_at", "attempts INT NOT NULL DEFAULT 1",
		"CREATE UNIQUE INDEX index_manifests_one_active", "WHERE state = 'active'",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration missing generation invariant %q", required)
		}
	}
}

func TestSealableIndexManifestMigrationAllowsPreWriteManifest(t *testing.T) {
	body, err := migrationFiles.ReadFile("0012_sealable_index_manifests.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"expected_chunk_count DROP NOT NULL",
		"expected_chunk_digest DROP NOT NULL",
		"index_manifests_expected_identity_pair",
		"expected_active_generation_id",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestIndexManifestVisibilityLookupMigrationSupportsBatchReads(t *testing.T) {
	body, err := migrationFiles.ReadFile("0013_index_manifest_visibility_lookup.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE INDEX index_manifests_visibility_lookup_idx",
		"ON index_manifests (tenant_id, document_id)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestIndexManifestReconciliationMigrationSupportsLeasedRepair(t *testing.T) {
	body, err := migrationFiles.ReadFile("0014_index_manifest_reconciliation.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"last_reconciled_at", "reconcile_lease_until", "reconcile_claim_token",
		"repair_attempts", "last_reconcile_error",
		"CREATE INDEX index_manifests_reconciliation_claim_idx", "WHERE state = 'active'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestIndexGenerationRetentionMigrationStartsRollbackWindowAtRetirement(t *testing.T) {
	body, err := migrationFiles.ReadFile("0015_index_generation_retention.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"retired_at", "retention_lease_until", "retention_claim_token",
		"qdrant_deleted_at", "elasticsearch_deleted_at", "retention_attempts",
		"retention_last_error", "UPDATE index_manifests SET retired_at=now() WHERE state='retired'",
		"CREATE INDEX index_manifests_retention_claim_idx", "WHERE state='retired'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestDocumentReleaseMigrationCarriesVersionBoundPublicationInvariants(t *testing.T) {
	body, err := migrationFiles.ReadFile("0016_document_releases.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE TABLE document_releases", "current_version_id", "published_version_id",
		"published_generation_id", "revision BIGINT", "resolution_status",
		"document_releases_published_identity_pair", "document_releases_resolved_current",
		"REFERENCES ingestion_jobs", "REFERENCES index_manifests",
		"ambiguous current document version", "ambiguous published generation",
		"j.task->>'file_path'=d.object_key", "WHERE published_version_id IS NOT NULL",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestRecoverableDeletionMigrationCarriesDurableProgressAndClaimFields(t *testing.T) {
	body, err := migrationFiles.ReadFile("0018_recoverable_document_deletion.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"deletion_status", "CREATE TABLE document_deletion_jobs", "claim_token", "lease_until",
		"object_keys TEXT[]", "qdrant_deleted_at", "elasticsearch_deleted_at", "objects_deleted_at",
		"UNIQUE (tenant_id, document_id)", "document_deletion_jobs_claim_idx",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestExactCandidatePublicationMigrationCarriesIdempotencyKey(t *testing.T) {
	body, err := migrationFiles.ReadFile("0017_exact_candidate_publication.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"last_publication_idempotency_key", "last_publication_request_hash",
		"document_releases_publication_idempotency_key",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestExternalIdentityBindingsMigrationCarriesAuthorityInvariants(t *testing.T) {
	body, err := migrationFiles.ReadFile("0019_external_identity_bindings.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE TABLE external_identity_bindings", "issuer TEXT NOT NULL",
		"external_subject TEXT NOT NULL", "internal_user_id UUID NOT NULL",
		"UNIQUE (issuer, external_subject)", "UNIQUE (id, tenant_id)",
		"users_identity_tenant_key", "FOREIGN KEY (internal_user_id, tenant_id)",
		"ON DELETE CASCADE", "external_identity_bindings_tenant_user_idx",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("external identity migration missing %q", required)
		}
	}
}

func TestIdentityLifecycleMigrationCarriesProvisioningInvariants(t *testing.T) {
	body, err := migrationFiles.ReadFile("0020_identity_lifecycle.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"origin TEXT NOT NULL", "identity_provisioning_connectors", "default_role",
		"identity_lifecycle_resources", "UNIQUE (connector_id, provider_resource_id)",
		"identity_lifecycle_idempotency", "PRIMARY KEY (connector_id, idempotency_key)",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("identity lifecycle migration missing %q", required)
		}
	}
}

func TestPlatformSessionsMigrationCarriesSessionSecurityInvariants(t *testing.T) {
	body, err := migrationFiles.ReadFile("0021_platform_sessions.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE TABLE platform_session_subject_states", "revoked_before TIMESTAMPTZ NOT NULL",
		"CREATE TABLE platform_sessions", "credential_digest BYTEA NOT NULL UNIQUE",
		"octet_length(credential_digest) = 32", "internal_user_id UUID NOT NULL",
		"FOREIGN KEY (internal_user_id, tenant_id)", "authentication_method",
		"assurance_level", "authenticated_at", "last_activity_at",
		"absolute_expires_at", "revoked_at", "revocation_reason",
		"established_correlation_id TEXT NOT NULL", "established_correlation_id <> ''",
		"revoked_correlation_id",
		"generation BIGINT", "policy_revision", "platform_sessions_subject_active_idx",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("platform session migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"password", "access_token", "refresh_token", "external_subject", "capabilities", " role "} {
		if strings.Contains(strings.ToLower(sql), forbidden) {
			t.Fatalf("platform session migration contains forbidden permission or provider data %q", forbidden)
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

func TestMigration0024ReleaseCenter(t *testing.T) {
	body, err := migrationFiles.ReadFile("0024_release_center.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	source := string(body)
	for _, expected := range []string{
		"CREATE TABLE release_center_reviews",
		"CREATE TABLE release_center_requests",
		"CREATE TABLE release_center_decisions",
		"document_version_id",
		"generation_id",
		"release_revision",
		"required_approvals",
		"findings JSONB",
		"REFERENCES release_center_reviews (tenant_id, review_id)",
	} {
		if !strings.Contains(source, expected) {
			t.Errorf("migration missing %q", expected)
		}
	}
}
