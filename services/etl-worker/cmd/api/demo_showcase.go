package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/db"
	"ai-etl-pipeline/internal/embedder"
	"ai-etl-pipeline/internal/es"
	"ai-etl-pipeline/internal/indexmanifest"
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/sparse"
)

const (
	demoSpaceID                = "demo-kb"
	demoPublishedDocID         = "demo-doc-handbook"
	demoPendingDocID           = "demo-doc-onboarding"
	demoConfidentialDocID      = "demo-doc-payroll"
	demoTravelDocID            = "demo-doc-travel"
	demoSecurityDocID          = "demo-doc-security"
	demoLeaveDocID             = "demo-doc-leave"
	demoPublishedVersionID     = "demo-job-handbook"
	demoPendingVersionID       = "demo-job-onboarding"
	demoConfidentialVersionID  = "demo-job-payroll"
	demoTravelVersionID        = "demo-job-travel"
	demoSecurityVersionID      = "demo-job-security"
	demoLeaveVersionID         = "demo-job-leave"
	demoPublishedGeneration    = "demo-gen-handbook"
	demoPendingGeneration      = "demo-gen-onboarding"
	demoConfidentialGeneration = "demo-gen-payroll"
	demoTravelGeneration       = "demo-gen-travel"
	demoSecurityGeneration     = "demo-gen-security"
	demoLeaveGeneration        = "demo-gen-leave"
)

func ensureDemoShowcase(ctx context.Context, cfg config.Config, q db.Querier, objects demoSourceObjectStore) error {
	if !demoLoginEnabled(cfg) || q == nil {
		return nil
	}
	tx, err := q.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var exists bool
	err = tx.QueryRow(ctx, `SELECT true FROM documents WHERE tenant_id=$1 AND doc_id=$2`, cfg.DemoTenantID, demoPendingDocID).Scan(&exists)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check demo showcase: %w", err)
	}
	var adminID, userID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM users WHERE lower(username)=lower($1)`, cfg.DemoAdminUsername).Scan(&adminID); err != nil {
		return fmt.Errorf("load demo admin: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM users WHERE lower(username)=lower($1)`, cfg.DemoUserUsername).Scan(&userID); err != nil {
		return fmt.Errorf("load demo user: %w", err)
	}

	if _, err := tx.Exec(ctx, `INSERT INTO knowledge_spaces (tenant_id,id,name,kind,is_default,active,purpose)
		VALUES ($1,$2,'演示知识库','production',false,true,'面试演示用受管知识空间')
		ON CONFLICT (tenant_id, id) DO NOTHING`, cfg.DemoTenantID, demoSpaceID); err != nil {
		return fmt.Errorf("seed demo space: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO knowledge_space_members (tenant_id,space_id,user_id,role)
		VALUES ($1,$2,$3::uuid,'manager'), ($1,$2,$4::uuid,'contributor')
		ON CONFLICT (tenant_id, space_id, user_id) DO NOTHING`, cfg.DemoTenantID, demoSpaceID, adminID, userID); err != nil {
		return fmt.Errorf("seed demo space members: %w", err)
	}
	if err := ensureDemoDefaultSpace(tx, ctx, cfg.DemoTenantID); err != nil {
		return err
	}
	now := time.Now().UTC()
	expires := now.Add(168 * time.Hour)
	docs := demoShowcaseDocuments()
	// The catalog row below advertises a source object. Write that object first,
	// or the seed manufactures exactly the state it cannot describe: a document
	// that is `completed`, whose manifest is healthy, whose chunks answer
	// questions, and whose source object does not exist. Nothing else in the
	// platform notices that — a repair replays an ingestion event, and the replay
	// is the first thing that fails.
	if err := syncDemoShowcaseSourceObjects(ctx, objects, docs); err != nil {
		return err
	}
	byID := make(map[string]demoShowcaseDocument, len(docs))
	// Converge the seeded projections on every boot, not only on first
	// provisioning. A manifest written by an older seed can disagree with the
	// retrieval index it claims to describe, and the reconciler cannot repair that
	// difference on its own: a repair replays an ingestion outbox event, and the
	// seed has to create that event as well. Re-running these inserts is the only
	// place that converges an environment which was provisioned before.
	for _, doc := range docs {
		byID[doc.docID] = doc
		if err := ensureDemoShowcaseDocument(tx, ctx, cfg, doc, adminID, userID, now); err != nil {
			return err
		}
	}
	// The request carries the exact candidate an approver binds to, so it tracks
	// the manifest instead of being written once: ReconcileStaleRequests compares
	// the two on every collector tick and demotes the request to needs_info while
	// they disagree.
	if !exists {
		if err := seedDemoReview(tx, ctx, cfg.DemoTenantID, demoPendingDocID, demoPendingVersionID, demoPendingGeneration,
			"demo-review-onboarding", "completed", "publish", "low",
			"材料完整，适合作为入职知识发布。", "[]", "match", "usable", "制度", expires, now); err != nil {
			return err
		}
	}
	if err := seedDemoRequest(tx, ctx, cfg, byID[demoPendingDocID],
		"demo-request-onboarding", "demo-review-onboarding", 1, "approval_pending", userID, now); err != nil {
		return err
	}
	if !exists {
		if err := seedDemoReview(tx, ctx, cfg.DemoTenantID, demoConfidentialDocID, demoConfidentialVersionID, demoConfidentialGeneration,
			"demo-review-payroll", "completed", "manual_review", "high",
			"检测到薪酬敏感信息，建议转人工并保持双人审批。",
			`[{"code":"sensitive_data_detected","severity":"high","summary":"文档含薪酬与账号类敏感字段，发布前需双人确认。","evidence_ref":"chunk-1"}]`,
			"match", "usable", "制度", expires, now); err != nil {
			return err
		}
	}
	if err := seedDemoRequest(tx, ctx, cfg, byID[demoConfidentialDocID],
		"demo-request-payroll", "demo-review-payroll", 2, "approval_pending", userID, now); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if exists {
		return nil
	}
	slog.Info("provisioned demo release-center showcase", "tenant", cfg.DemoTenantID, "space", demoSpaceID)
	return nil
}

// demoSourceObjectStore is the slice of the object store the showcase seed
// needs. Kept narrow so the seed can be exercised without MinIO.
type demoSourceObjectStore interface {
	Upload(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error
	Exists(ctx context.Context, key string) (bool, error)
}

const demoSourceContentType = "text/markdown; charset=utf-8"

// demoShowcaseSourceKey is the object key the catalog advertises for a showcase
// document. It is derived in one place so the row and the object cannot drift.
func demoShowcaseSourceKey(doc demoShowcaseDocument) string {
	return "demo/" + doc.docID + ".md"
}

// demoShowcaseSourceBytes renders the archived source document from the same
// chunk models the retrieval seed writes. Deriving both from one source is the
// point: the object and the index have to describe the same document, or a
// re-ingest from the object would produce different chunks than the manifest
// already records.
func demoShowcaseSourceBytes(doc demoShowcaseDocument) []byte {
	var builder strings.Builder
	for i, content := range doc.chunks {
		if i > 0 {
			builder.WriteString("\n\n")
		}
		builder.WriteString(content)
	}
	builder.WriteString("\n")
	return []byte(builder.String())
}

// demoShowcaseSourceHash is the SHA-256 of the archived object, so the catalog
// size and hash can be checked against the store instead of asserted.
func demoShowcaseSourceHash(doc demoShowcaseDocument) string {
	sum := sha256.Sum256(demoShowcaseSourceBytes(doc))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// syncDemoShowcaseSourceObjects writes every showcase document's source object
// and reads it back, so a boot that cannot materialize the object fails loudly
// instead of leaving the catalog pointing at nothing. A missing object store is
// an error rather than a no-op: skipping the write is precisely how the catalog
// ended up advertising source objects that did not exist.
func syncDemoShowcaseSourceObjects(ctx context.Context, objects demoSourceObjectStore, docs []demoShowcaseDocument) error {
	if objects == nil {
		return errors.New("demo showcase seed requires an object store")
	}
	for _, doc := range docs {
		key := demoShowcaseSourceKey(doc)
		content := demoShowcaseSourceBytes(doc)
		if err := objects.Upload(ctx, key, bytes.NewReader(content), int64(len(content)), demoSourceContentType); err != nil {
			return fmt.Errorf("seed demo source object %s: %w", key, err)
		}
		exists, err := objects.Exists(ctx, key)
		if err != nil {
			return fmt.Errorf("verify demo source object %s: %w", key, err)
		}
		if !exists {
			return fmt.Errorf("demo source object %s is absent after upload", key)
		}
	}
	return nil
}

// ensureDemoShowcaseDocument converges one showcase document's durable rows.
//
// Every derived value here comes from demoShowcaseChunks, the same chunk models
// the retrieval seed writes. That is the point: index_manifests.expected_chunk_digest
// is the ground truth the reconciler compares both projections against, and the
// release center only offers a document for approval while its manifest agrees
// with its projections. A hand-written digest therefore does not merely look
// untidy, it makes the manifest permanently divergent.
//
// The ingestion outbox event belongs to the same contract. FinishReconciliation
// resolves the repair target by joining ingestion_jobs to ingestion_outbox, so a
// manifest seeded without an outbox row has no replay path at all: the divergence
// can never be scheduled, and the same failure is reported on every pass.
func ensureDemoShowcaseDocument(tx pgx.Tx, ctx context.Context, cfg config.Config, doc demoShowcaseDocument, adminID, userID string, now time.Time) error {
	digest, err := demoShowcaseExpectedDigest(cfg, doc)
	if err != nil {
		return fmt.Errorf("derive demo manifest digest %s: %w", doc.docID, err)
	}
	sourceKey := demoShowcaseSourceKey(doc)
	// The declared size is measured, not asserted: a row claiming 2048 bytes for
	// a 111-byte object is the same lie in a smaller place.
	sourceSize := int64(len(demoShowcaseSourceBytes(doc)))
	// $14 is the admin UUID the previous seed wrote into owner. A row still
	// holding that value is corrected in place; a row an administrator has since
	// edited is left alone. Without this branch the fix would only ever reach a
	// fresh database: the conflict branch updates nothing unless a column in its
	// WHERE moves, so an already-seeded deployment would keep rendering the UUID.
	if _, err := tx.Exec(ctx, `INSERT INTO documents (
			tenant_id,doc_id,file_name,object_key,file_hash,file_size,content_type,permission,status,stage,
			chunks_done,chunks_total,uploaded_by,completed_at,doc_status,effective_date,owner,knowledge_space_id,publication_status,deletion_status
		) VALUES ($1,$2,$3,$4,$5,$6,'text/markdown',$7,'completed','completed',$13,$13,$8,$9,'active',CURRENT_DATE,$10,$11,$12,'active')
		ON CONFLICT (tenant_id, doc_id) DO UPDATE SET
			object_key=EXCLUDED.object_key,
			file_size=EXCLUDED.file_size,
			owner=CASE WHEN documents.owner=$14 THEN EXCLUDED.owner ELSE documents.owner END,
			updated_at=now()
		WHERE documents.object_key IS DISTINCT FROM EXCLUDED.object_key
		   OR documents.file_size IS DISTINCT FROM EXCLUDED.file_size
		   OR (documents.owner=$14 AND documents.owner IS DISTINCT FROM EXCLUDED.owner)`,
		cfg.DemoTenantID, doc.docID, doc.fileName, sourceKey, doc.digest, sourceSize, doc.permission,
		userID, now, doc.owner, demoSpaceID, doc.publication, len(doc.chunks), adminID); err != nil {
		return fmt.Errorf("seed demo document %s: %w", doc.docID, err)
	}
	// A completed job is a published one, so the terminal fixture sets
	// published_at. The update is additive: an already-provisioned row keeps its
	// original timestamp.
	if _, err := tx.Exec(ctx, `INSERT INTO ingestion_jobs (
			job_id,event_id,tenant_id,doc_id,request_signature,task,status,completed_at,published_at
		) VALUES ($1,$2,$3,$4,$5,$6::jsonb,'completed',$7,$7)
		ON CONFLICT (job_id) DO UPDATE SET published_at=COALESCE(ingestion_jobs.published_at, EXCLUDED.published_at)`,
		doc.versionID, doc.eventID, cfg.DemoTenantID, doc.docID, "demo-sig-"+doc.docID,
		fmt.Sprintf(`{"file_path":"demo/%s.md"}`, doc.docID), now); err != nil {
		return fmt.Errorf("seed demo ingestion job %s: %w", doc.versionID, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ingestion_outbox (
			event_id,job_id,tenant_id,doc_id,task,published_at
		) VALUES ($1,$2,$3,$4,$5::jsonb,$6)
		ON CONFLICT (event_id) DO NOTHING`,
		doc.eventID, doc.versionID, cfg.DemoTenantID, doc.docID,
		fmt.Sprintf(`{"file_path":"demo/%s.md"}`, doc.docID), now); err != nil {
		return fmt.Errorf("seed demo ingestion outbox %s: %w", doc.eventID, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO index_manifests (
			generation_id,tenant_id,document_id,document_version_id,chunker_version,embedding_model,vector_dimension,
			schema_version,collection_version,index_version,expected_chunk_count,expected_chunk_digest,
			qdrant_count,qdrant_digest,elasticsearch_count,elasticsearch_digest,state,activated_at
		) VALUES ($1,$2,$3,$4,'v1','demo',1536,'v1','v1','v1',$5,$6,$5,$6,$5,$6,'active',$7)
		ON CONFLICT (generation_id) DO UPDATE SET
			expected_chunk_count=EXCLUDED.expected_chunk_count,
			expected_chunk_digest=EXCLUDED.expected_chunk_digest,
			qdrant_count=EXCLUDED.qdrant_count,qdrant_digest=EXCLUDED.qdrant_digest,
			elasticsearch_count=EXCLUDED.elasticsearch_count,elasticsearch_digest=EXCLUDED.elasticsearch_digest
		WHERE index_manifests.expected_chunk_count IS DISTINCT FROM EXCLUDED.expected_chunk_count
			OR index_manifests.expected_chunk_digest IS DISTINCT FROM EXCLUDED.expected_chunk_digest`,
		doc.generationID, cfg.DemoTenantID, doc.docID, doc.versionID, len(doc.chunks), digest, now); err != nil {
		return fmt.Errorf("seed demo generation %s: %w", doc.generationID, err)
	}
	if doc.published {
		if _, err := tx.Exec(ctx, `INSERT INTO document_releases (
				tenant_id,document_id,current_version_id,published_version_id,published_generation_id,revision,resolution_status
			) VALUES ($1,$2,$3,$3,$4,1,'resolved')
			ON CONFLICT (tenant_id, document_id) DO NOTHING`,
			cfg.DemoTenantID, doc.docID, doc.versionID, doc.generationID); err != nil {
			return fmt.Errorf("seed demo release %s: %w", doc.docID, err)
		}
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO document_releases (
			tenant_id,document_id,current_version_id,revision,resolution_status
		) VALUES ($1,$2,$3,1,'resolved')
		ON CONFLICT (tenant_id, document_id) DO NOTHING`,
		cfg.DemoTenantID, doc.docID, doc.versionID); err != nil {
		return fmt.Errorf("seed demo release %s: %w", doc.docID, err)
	}
	return nil
}

// demoShowcaseIdentity names the projection generation one showcase document
// owns. It is shared by the SQL seed and the index seed so the manifest, the
// Qdrant points and the Elasticsearch documents cannot disagree about identity.
func demoShowcaseIdentity(cfg config.Config, doc demoShowcaseDocument) indexmanifest.GenerationIdentity {
	return indexmanifest.GenerationIdentity{
		VersionIdentity: indexmanifest.VersionIdentity{
			TenantID: cfg.DemoTenantID, DocumentID: doc.docID, DocumentVersionID: doc.versionID,
		},
		GenerationID: doc.generationID,
	}
}

// demoShowcaseChunks is the single source of truth for the chunks of one
// showcase document. The SQL seed derives the manifest digest from it and the
// index seed writes exactly these models, so the expectation can never describe
// content the projections do not hold.
func demoShowcaseChunks(cfg config.Config, doc demoShowcaseDocument) []model.Chunk {
	chunks := make([]model.Chunk, 0, len(doc.chunks))
	for i, content := range doc.chunks {
		chunks = append(chunks, model.Chunk{
			ChunkID:    fmt.Sprintf("%s-%d", doc.docID, i),
			DocID:      doc.docID,
			TenantID:   cfg.DemoTenantID,
			Content:    content,
			Index:      i,
			Permission: doc.permission,
			FileHash:   doc.digest,
			Metadata: map[string]string{
				"knowledge_base_id": demoSpaceID,
				"applicable_scope":  "production",
			},
		})
	}
	return chunks
}

// demoShowcaseExpectedDigest is the value both index_manifests and the release
// center request must carry: the identity digest of the chunks the retrieval
// seed actually writes.
func demoShowcaseExpectedDigest(cfg config.Config, doc demoShowcaseDocument) (string, error) {
	return indexmanifest.ChunkIdentityDigest(demoShowcaseIdentity(cfg, doc), demoShowcaseChunks(cfg, doc))
}

func seedDemoReview(tx pgx.Tx, ctx context.Context, tenantID, docID, versionID, generationID, reviewID, status, recommendation, risk, summary, findings, spaceFit, usable, kind string, expires, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO release_center_reviews (
			review_id,tenant_id,document_id,document_version_id,generation_id,release_revision,status,recommendation,risk_level,
			summary,findings,space_fit,knowledge_usable,kind_label,model,prompt_version,created_at,expires_at
		) VALUES ($1,$2,$3,$4,$5,1,$6,$7,$8,$9,$10::jsonb,$11,$12,$13,'demo-reviewer','v1',$14,$15)
		ON CONFLICT (review_id) DO NOTHING`,
		reviewID, tenantID, docID, versionID, generationID, status, recommendation, risk, summary, findings, spaceFit, usable, kind, now, expires)
	if err != nil {
		return fmt.Errorf("seed demo review %s: %w", reviewID, err)
	}
	return nil
}

// seedDemoRequest converges the release request that carries one showcase
// document's exact candidate.
//
// The expected count and digest are derived from the document rather than passed
// in, because the approver re-derives the candidate from index_manifests and
// rejects the request when the two disagree (ApprovalService.Decide compares
// them for equality, and ReconcileStaleRequests demotes the request while they
// differ). A request seeded with a literal would therefore diverge from its
// manifest the moment either side changed. The update is deliberately narrow:
// review_id and state belong to the running release workflow, not to the seed.
func seedDemoRequest(tx pgx.Tx, ctx context.Context, cfg config.Config, doc demoShowcaseDocument, requestID, reviewID string, required int, state, requestedBy string, now time.Time) error {
	digest, err := demoShowcaseExpectedDigest(cfg, doc)
	if err != nil {
		return fmt.Errorf("derive demo request digest %s: %w", doc.docID, err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO release_center_requests (
			request_id,tenant_id,document_id,document_version_id,generation_id,expected_chunk_count,expected_chunk_digest,
			release_revision,review_id,required_approvals,state,requested_by,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,1,$8,$9,$10,$11,$12,$12)
		ON CONFLICT (request_id) DO UPDATE SET
			expected_chunk_count=EXCLUDED.expected_chunk_count,
			expected_chunk_digest=EXCLUDED.expected_chunk_digest
		WHERE release_center_requests.state NOT IN ('published','rejected')
		  AND (release_center_requests.expected_chunk_count IS DISTINCT FROM EXCLUDED.expected_chunk_count
		    OR release_center_requests.expected_chunk_digest IS DISTINCT FROM EXCLUDED.expected_chunk_digest)`,
		requestID, cfg.DemoTenantID, doc.docID, doc.versionID, doc.generationID,
		len(doc.chunks), digest, reviewID, required, state, requestedBy, now); err != nil {
		return fmt.Errorf("seed demo request %s: %w", requestID, err)
	}
	return nil
}

func demoHandbookChunks() []string {
	return []string{
		"知境企业知识库员工手册。问答工作台只回答已经发布的知识，草稿、退役和未发布文档不得作为回答证据。",
		"知识发布前必须经过确定性门禁和 Agent 预审。普通文档由一名管理员审批，机密或高风险文档需要两名管理员审批。Agent 只能给出建议，不能自行批准或发布。",
		"演示知识库与生产租户隔离。普通用户可以检索已发布的公开和内部知识，不能查看机密文档，也不能改发布状态。",
	}
}

// demoShowcaseDocument is the single source of truth for a seeded showcase
// document. The SQL seed and the retrieval-index seed both read this list, so a
// manifest can never claim chunks that were never written to the stores.
//
// That drift is not cosmetic: the release center Agent pre-review binds to the
// exact candidate by reading its stored chunks, so a manifest without chunks
// makes every automatic review fail with "exact candidate content is
// unavailable" and the document can never leave manual_exception.
type demoShowcaseDocument struct {
	docID        string
	fileName     string
	permission   string
	publication  string
	versionID    string
	eventID      string
	generationID string
	digest       string
	published    bool
	// owner is the accountable owner shown in the release center and on the
	// document detail page. It is a business role, not a user id: the column's
	// definition (migration 0004) is "business role or user", deliberately
	// distinct from uploaded_by which records who performed the upload. Seeding
	// the demo admin's UUID here rendered as a 36-character identifier wherever
	// an administrator was meant to read a name.
	owner  string
	chunks []string
}

func demoShowcaseDocuments() []demoShowcaseDocument {
	return []demoShowcaseDocument{
		{
			docID: demoPublishedDocID, fileName: "员工手册.md", permission: "public", publication: "published",
			versionID: demoPublishedVersionID, eventID: "demo-event-handbook", generationID: demoPublishedGeneration,
			digest: "sha256:demo-handbook", published: true, owner: "人力资源部", chunks: demoHandbookChunks(),
		},
		{
			docID: demoPendingDocID, fileName: "新员工入职指南.md", permission: "internal", publication: "draft",
			versionID: demoPendingVersionID, eventID: "demo-event-onboarding", generationID: demoPendingGeneration,
			digest: "sha256:demo-onboarding", published: false, owner: "人力资源部", chunks: demoOnboardingChunks(),
		},
		{
			docID: demoConfidentialDocID, fileName: "薪酬核算说明.md", permission: "confidential", publication: "draft",
			versionID: demoConfidentialVersionID, eventID: "demo-event-payroll", generationID: demoConfidentialGeneration,
			digest: "sha256:demo-payroll", published: false, owner: "财务部", chunks: demoPayrollChunks(),
		},
		// The three documents above are the release-center showcase: one
		// published, one pending approval, one held back for its risk level.
		// Those states are the demo, so they must not change.
		//
		// The three below exist because those states leave this tenant with
		// exactly one answerable document. A visitor who signs in through the
		// login page's ordinary-role entry lands here, so every business
		// question came back as a refusal ("the retrieved content does not
		// support an answer") and the product read as if it could not answer
		// anything. They are published, readable by the ordinary role, and
		// carry concrete figures so questions have something to hit.
		{
			docID: demoTravelDocID, fileName: "差旅与报销标准.md", permission: "internal", publication: "published",
			versionID: demoTravelVersionID, eventID: "demo-event-travel", generationID: demoTravelGeneration,
			digest: "sha256:demo-travel", published: true, owner: "财务部", chunks: demoTravelChunks(),
		},
		{
			docID: demoSecurityDocID, fileName: "信息安全管理规范.md", permission: "internal", publication: "published",
			versionID: demoSecurityVersionID, eventID: "demo-event-security", generationID: demoSecurityGeneration,
			digest: "sha256:demo-security", published: true, owner: "信息技术部", chunks: demoSecurityChunks(),
		},
		{
			docID: demoLeaveDocID, fileName: "员工假期管理规定.md", permission: "public", publication: "published",
			versionID: demoLeaveVersionID, eventID: "demo-event-leave", generationID: demoLeaveGeneration,
			digest: "sha256:demo-leave", published: true, owner: "人力资源部", chunks: demoLeaveChunks(),
		},
	}
}

func demoOnboardingChunks() []string {
	return []string{
		"新员工入职首日需到人力资源部完成身份核验，领取工牌、办公设备与内网账号，并现场签署保密协议。",
		"入职第一周需完成信息安全、合规与部门业务三项培训，培训记录由直属主管确认后归档到员工档案。",
		"试用期为三个月。期满前两周由直属主管发起转正评估，评估结果经部门负责人审批后生效。",
	}
}

func demoPayrollChunks() []string {
	return []string{
		"月度薪酬于每月十五日发放，遇法定节假日提前至最近一个工作日。",
		"薪酬结构由基本工资、绩效奖金与专项补贴组成，绩效奖金依据上一季度考核结果核定。",
		"员工个人银行账号、社保公积金缴纳基数与个税专项附加扣除信息属于敏感信息，仅限人力资源部与财务部授权人员查看，不得对外提供。",
	}
}

func demoTravelChunks() []string {
	return []string{
		"员工出差前须在系统提交出差申请，经直属主管审批后生效。跨省出差或预计费用超过 5000 元的，需部门负责人二次审批。",
		"住宿费按城市分级执行：一线城市每晚上限 600 元，省会城市 450 元，其他城市 350 元。市内交通凭票报销，长途交通优先选择高铁二等座。",
		"差旅结束后 15 个工作日内提交报销，需附出差申请单、发票原件与行程单。超期未提交的，须由部门负责人书面说明后方可受理。",
	}
}

func demoSecurityChunks() []string {
	return []string{
		"员工账号密码长度不得少于 12 位，须包含大小写字母、数字与符号，每 90 天更换一次。禁止在多个系统间复用同一密码，禁止将账号借予他人使用。",
		"公司数据分为公开、内部、机密三级。内部数据仅限公司员工访问；机密数据须经数据所有者授权，访问行为全程留痕。",
		"向外部提供内部或机密数据须经信息安全部门审批并签署保密协议。办公设备禁止安装未授权软件，移动存储设备接入内网前须完成加密登记。",
	}
}

func demoLeaveChunks() []string {
	return []string{
		"员工入职满一年后享受带薪年假：工龄 1 至 10 年每年 5 天，10 至 20 年每年 10 天，20 年以上每年 15 天。年假须提前 3 个工作日在系统提交申请。",
		"病假须提供二级以上医院证明，全年累计超过 15 天的，超出部分按基本工资的 80% 计发。事假为无薪假，全年累计不得超过 20 天。",
		"所有请假均须在系统提交并由直属主管审批。连续请假超过 3 个工作日的，须部门负责人审批；超过 10 个工作日的，须人力资源部备案。",
	}
}

func ensureDemoDefaultSpace(tx pgx.Tx, ctx context.Context, tenantID string) error {
	if _, err := tx.Exec(ctx, `UPDATE knowledge_spaces SET is_default=false, updated_at=now()
		WHERE tenant_id=$1 AND is_default AND id<>$2`, tenantID, demoSpaceID); err != nil {
		return fmt.Errorf("clear demo default space: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE knowledge_spaces SET is_default=true, updated_at=now()
		WHERE tenant_id=$1 AND id=$2`, tenantID, demoSpaceID); err != nil {
		return fmt.Errorf("set demo default space: %w", err)
	}
	return nil
}

type demoChunkIndexer interface {
	UpsertGeneration(context.Context, indexmanifest.GenerationIdentity, model.Chunk) error
	ObserveGeneration(context.Context, indexmanifest.GenerationIdentity) (indexmanifest.BackendObservation, error)
}

func ensureDemoShowcaseIndex(ctx context.Context, cfg config.Config, qdrant, elastic demoChunkIndexer, embed embedder.Embedder) error {
	if !demoLoginEnabled(cfg) || qdrant == nil || elastic == nil || embed == nil {
		return nil
	}
	encoder := sparse.NewEncoder(sparse.DefaultParams())
	for _, doc := range demoShowcaseDocuments() {
		identity := demoShowcaseIdentity(cfg, doc)
		if observed, err := qdrant.ObserveGeneration(ctx, identity); err == nil && observed.Count >= len(doc.chunks) {
			continue
		}
		for _, chunk := range demoShowcaseChunks(cfg, doc) {
			chunk.CreatedAt = time.Now().UTC()
			chunk.SparseVector = encoder.Encode(chunk.Content)
			if err := embed.Embed(ctx, &chunk); err != nil {
				return fmt.Errorf("embed demo chunk %s/%d: %w", doc.docID, chunk.Index, err)
			}
			if err := qdrant.UpsertGeneration(ctx, identity, chunk); err != nil {
				return fmt.Errorf("index demo document %s in qdrant: %w", doc.docID, err)
			}
			if err := elastic.UpsertGeneration(ctx, identity, chunk); err != nil {
				return fmt.Errorf("index demo document %s in elasticsearch: %w", doc.docID, err)
			}
		}
		slog.Info("provisioned demo retrieval index", "tenant", cfg.DemoTenantID, "document", doc.docID, "chunks", len(doc.chunks))
	}
	return nil
}

func indexDemoShowcase(ctx context.Context, cfg config.Config, qdrant demoChunkIndexer) error {
	if !demoLoginEnabled(cfg) || qdrant == nil {
		return nil
	}
	esIndexer, err := es.NewHTTPIndexer(cfg.ESAddress, cfg.ESAPIKey, cfg.ESIndex, es.WithReplicas(cfg.ESIndexReplicas))
	if err != nil {
		return err
	}
	defer esIndexer.Close()
	emb, err := embedder.NewHTTPEmbedder(cfg)
	if err != nil {
		return err
	}
	defer emb.Close()
	timeout := cfg.EmbedTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout*time.Duration(demoShowcaseChunkTotal()+1))
	defer cancel()
	return ensureDemoShowcaseIndex(ctx, cfg, qdrant, esIndexer, emb)
}

func demoShowcaseChunkTotal() int {
	total := 0
	for _, doc := range demoShowcaseDocuments() {
		total += len(doc.chunks)
	}
	return total
}
