package releasecenter

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresOverviewProjectsPublishedApprovalAndTenantScope(t *testing.T) {
	dsn := os.Getenv("GOVERNANCE_RELEASE_TEST_DSN")
	if dsn == "" {
		t.Skip("set GOVERNANCE_RELEASE_TEST_DSN to run PostgreSQL release-center integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("releasecenter_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE documents (
			tenant_id TEXT NOT NULL, doc_id TEXT NOT NULL, file_name TEXT NOT NULL,
			permission TEXT NOT NULL, status TEXT NOT NULL, doc_status TEXT NOT NULL,
			owner TEXT NOT NULL, effective_date DATE, knowledge_space_id TEXT NOT NULL,
			publication_status TEXT NOT NULL, deletion_status TEXT NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY (tenant_id,doc_id));
		CREATE TABLE document_releases (
			tenant_id TEXT NOT NULL, document_id TEXT NOT NULL, current_version_id TEXT,
			resolution_status TEXT NOT NULL, revision BIGINT NOT NULL, published_version_id TEXT,
			PRIMARY KEY (tenant_id,document_id));
		CREATE TABLE index_manifests (
			generation_id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, document_id TEXT NOT NULL,
			document_version_id TEXT NOT NULL, state TEXT NOT NULL, expected_chunk_count INT,
			expected_chunk_digest TEXT, qdrant_count INT, qdrant_digest TEXT,
			elasticsearch_count INT, elasticsearch_digest TEXT, last_reconcile_error TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now());
		CREATE TABLE release_center_reviews (
			review_id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, document_id TEXT NOT NULL,
			document_version_id TEXT NOT NULL, generation_id TEXT NOT NULL, release_revision BIGINT NOT NULL,
			status TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now());
		CREATE TABLE release_center_requests (
			request_id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, document_id TEXT NOT NULL,
			document_version_id TEXT, state TEXT NOT NULL, required_approvals INT NOT NULL, review_id TEXT NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
		CREATE TABLE release_center_decisions (
			decision_id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, request_id TEXT NOT NULL,
			decided_by TEXT NOT NULL, decision TEXT NOT NULL);
		INSERT INTO documents (tenant_id,doc_id,file_name,permission,status,doc_status,owner,effective_date,knowledge_space_id,publication_status,deletion_status)
		VALUES
			('acme','published-1','Published','internal','completed','active','owner',CURRENT_DATE,'production','published','active'),
			('acme','approval-1','Approval','confidential','completed','active','owner',CURRENT_DATE,'production','draft','active'),
			('acme','blocked-1','Blocked','internal','completed','active','owner',CURRENT_DATE,'production','draft','active'),
			('acme','stale-1','Stale','internal','completed','active','owner',CURRENT_DATE,'production','draft','active'),
			('acme','replacement-1','Replacement','internal','completed','active','owner',CURRENT_DATE,'production','draft','active'),
			('acme','cutover-1','Cutover','internal','completed','active','owner',CURRENT_DATE,'production','published','active'),
			('other','hidden-1','Hidden','internal','completed','active','owner',CURRENT_DATE,'production','draft','active');
		INSERT INTO document_releases VALUES
			('acme','published-1','job-published','resolved',1,'job-published'),
			('acme','approval-1','job-approval','resolved',1,NULL),
			('acme','blocked-1','job-blocked','resolved',1,NULL),
			('acme','stale-1','job-stale','resolved',2,NULL),
			('acme','replacement-1','job-new','resolved',2,NULL),
			('acme','cutover-1','job-new','resolved',2,'job-old'),
			('other','hidden-1','job-hidden','resolved',1,NULL);
		INSERT INTO index_manifests (generation_id,tenant_id,document_id,document_version_id,state,expected_chunk_count,expected_chunk_digest,qdrant_count,qdrant_digest,elasticsearch_count,elasticsearch_digest)
		VALUES
			('gen-published','acme','published-1','job-published','active',1,'digest',1,'digest',1,'digest'),
			('gen-approval','acme','approval-1','job-approval','active',1,'digest',1,'digest',1,'digest'),
			('gen-blocked','acme','blocked-1','job-blocked','active',1,'digest',1,'digest',1,'digest'),
			('gen-stale','acme','stale-1','job-stale','active',1,'digest',1,'digest',1,'digest'),
			('gen-new','acme','replacement-1','job-new','active',1,'digest',1,'digest',1,'digest'),
			('gen-cutover','acme','cutover-1','job-new','active',1,'digest',1,'digest',1,'digest'),
			('gen-hidden','other','hidden-1','job-hidden','active',1,'digest',1,'digest',1,'digest');
		INSERT INTO release_center_reviews (review_id,tenant_id,document_id,document_version_id,generation_id,release_revision,status) VALUES
			('review-approval','acme','approval-1','job-approval','gen-approval',1,'completed'),
			('review-blocked','acme','blocked-1','job-blocked','gen-blocked',1,'failed'),
			('review-stale','acme','stale-1','job-stale','gen-stale',2,'completed'),
			('review-old-failed','acme','replacement-1','job-old','gen-old',1,'failed');
		INSERT INTO release_center_requests (request_id,tenant_id,document_id,document_version_id,state,required_approvals,review_id) VALUES ('request-approval','acme','approval-1','job-approval','approval_pending',2,'review-approval');
		INSERT INTO release_center_requests (request_id,tenant_id,document_id,document_version_id,state,required_approvals,review_id) VALUES ('request-stale','acme','stale-1','job-stale','needs_info',1,'review-stale');
		INSERT INTO release_center_decisions (decision_id,tenant_id,request_id,decided_by,decision) VALUES ('decision-1','acme','request-approval','admin-a','approved');
	`); err != nil {
		t.Fatal(err)
	}
	items, err := NewPostgresStore(pool).ListOverview(ctx, "acme", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 6 {
		t.Fatalf("items=%d, want 6 tenant-scoped rows: %+v", len(items), items)
	}
	var published, approval, blocked, stale, replacement, cutover *OverviewItem
	for i := range items {
		switch items[i].DocumentID {
		case "published-1":
			published = &items[i]
		case "approval-1":
			approval = &items[i]
		case "blocked-1":
			blocked = &items[i]
		case "stale-1":
			stale = &items[i]
		case "replacement-1":
			replacement = &items[i]
		case "cutover-1":
			cutover = &items[i]
		}
	}
	if published == nil || published.State != "published" {
		t.Fatalf("published projection=%+v", published)
	}
	if approval == nil || approval.State != "approval_pending" || approval.RequiredApprovals != 2 || approval.ApprovedDecisions != 1 || approval.RequestID != "request-approval" {
		t.Fatalf("approval projection=%+v", approval)
	}
	if blocked == nil || blocked.State != "review_blocked" || len(blocked.Blockers) != 1 || blocked.Blockers[0] != "agent_review_unavailable" {
		t.Fatalf("blocked projection=%+v", blocked)
	}
	if stale == nil || stale.State != "needs_info" || len(stale.Blockers) != 1 || stale.Blockers[0] != "exact_candidate_unavailable" {
		t.Fatalf("stale projection=%+v", stale)
	}
	if replacement == nil || replacement.State != "checking" {
		t.Fatalf("replacement projection=%+v, old failed review must not bind to current version", replacement)
	}
	if cutover == nil || cutover.State != "checking" {
		t.Fatalf("published replacement projection=%+v, want checking not published", cutover)
	}
}
