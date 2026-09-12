package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	demoPublishedVersionID     = "demo-job-handbook"
	demoPendingVersionID       = "demo-job-onboarding"
	demoConfidentialVersionID  = "demo-job-payroll"
	demoPublishedGeneration    = "demo-gen-handbook"
	demoPendingGeneration      = "demo-gen-onboarding"
	demoConfidentialGeneration = "demo-gen-payroll"
)

func ensureDemoShowcase(ctx context.Context, cfg config.Config, q db.Querier) error {
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
	if exists {
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return nil
	}

	now := time.Now().UTC()
	expires := now.Add(168 * time.Hour)
	docs := []struct {
		docID, fileName, permission, publication, jobID, eventID, generation, digest string
		published                                                                    bool
	}{
		{demoPublishedDocID, "员工手册.md", "public", "published", demoPublishedVersionID, "demo-event-handbook", demoPublishedGeneration, "sha256:demo-handbook", true},
		{demoPendingDocID, "新员工入职指南.md", "internal", "draft", demoPendingVersionID, "demo-event-onboarding", demoPendingGeneration, "sha256:demo-onboarding", false},
		{demoConfidentialDocID, "薪酬核算说明.md", "confidential", "draft", demoConfidentialVersionID, "demo-event-payroll", demoConfidentialGeneration, "sha256:demo-payroll", false},
	}
	for _, doc := range docs {
		if _, err := tx.Exec(ctx, `INSERT INTO documents (
				tenant_id,doc_id,file_name,object_key,file_hash,file_size,content_type,permission,status,stage,
				chunks_done,chunks_total,uploaded_by,completed_at,doc_status,effective_date,owner,knowledge_space_id,publication_status,deletion_status
			) VALUES ($1,$2,$3,$4,$5,2048,'text/markdown',$6,'completed','completed',3,3,$7,$8,'active',CURRENT_DATE,$9,$10,$11,'active')
			ON CONFLICT (tenant_id, doc_id) DO NOTHING`,
			cfg.DemoTenantID, doc.docID, doc.fileName, "demo/"+doc.docID+".md", doc.digest, doc.permission,
			userID, now, adminID, demoSpaceID, doc.publication); err != nil {
			return fmt.Errorf("seed demo document %s: %w", doc.docID, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO ingestion_jobs (
				job_id,event_id,tenant_id,doc_id,request_signature,task,status,completed_at
			) VALUES ($1,$2,$3,$4,$5,$6::jsonb,'completed',$7)
			ON CONFLICT (job_id) DO NOTHING`,
			doc.jobID, doc.eventID, cfg.DemoTenantID, doc.docID, "demo-sig-"+doc.docID,
			fmt.Sprintf(`{"file_path":"demo/%s.md"}`, doc.docID), now); err != nil {
			return fmt.Errorf("seed demo ingestion job %s: %w", doc.jobID, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO index_manifests (
				generation_id,tenant_id,document_id,document_version_id,chunker_version,embedding_model,vector_dimension,
				schema_version,collection_version,index_version,expected_chunk_count,expected_chunk_digest,
				qdrant_count,qdrant_digest,elasticsearch_count,elasticsearch_digest,state,activated_at
			) VALUES ($1,$2,$3,$4,'v1','demo',1536,'v1','v1','v1',3,$5,3,$5,3,$5,'active',$6)
			ON CONFLICT (generation_id) DO NOTHING`,
			doc.generation, cfg.DemoTenantID, doc.docID, doc.jobID, doc.digest, now); err != nil {
			return fmt.Errorf("seed demo generation %s: %w", doc.generation, err)
		}
		if doc.published {
			if _, err := tx.Exec(ctx, `INSERT INTO document_releases (
					tenant_id,document_id,current_version_id,published_version_id,published_generation_id,revision,resolution_status
				) VALUES ($1,$2,$3,$3,$4,1,'resolved')
				ON CONFLICT (tenant_id, document_id) DO NOTHING`,
				cfg.DemoTenantID, doc.docID, doc.jobID, doc.generation); err != nil {
				return fmt.Errorf("seed demo release %s: %w", doc.docID, err)
			}
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO document_releases (
				tenant_id,document_id,current_version_id,revision,resolution_status
			) VALUES ($1,$2,$3,1,'resolved')
			ON CONFLICT (tenant_id, document_id) DO NOTHING`,
			cfg.DemoTenantID, doc.docID, doc.jobID); err != nil {
			return fmt.Errorf("seed demo release %s: %w", doc.docID, err)
		}
	}

	if err := seedDemoReview(tx, ctx, cfg.DemoTenantID, demoPendingDocID, demoPendingVersionID, demoPendingGeneration,
		"demo-review-onboarding", "completed", "publish", "low",
		"材料完整，适合作为入职知识发布。", "[]", "match", "usable", "制度", expires, now); err != nil {
		return err
	}
	if err := seedDemoRequest(tx, ctx, cfg.DemoTenantID, demoPendingDocID, demoPendingVersionID, demoPendingGeneration,
		"demo-request-onboarding", "demo-review-onboarding", 1, "approval_pending", userID, "sha256:demo-onboarding", now); err != nil {
		return err
	}
	if err := seedDemoReview(tx, ctx, cfg.DemoTenantID, demoConfidentialDocID, demoConfidentialVersionID, demoConfidentialGeneration,
		"demo-review-payroll", "completed", "manual_review", "high",
		"检测到薪酬敏感信息，建议转人工并保持双人审批。",
		`[{"code":"sensitive_data_detected","severity":"high","summary":"文档含薪酬与账号类敏感字段，发布前需双人确认。","evidence_ref":"chunk-1"}]`,
		"match", "usable", "制度", expires, now); err != nil {
		return err
	}
	if err := seedDemoRequest(tx, ctx, cfg.DemoTenantID, demoConfidentialDocID, demoConfidentialVersionID, demoConfidentialGeneration,
		"demo-request-payroll", "demo-review-payroll", 2, "approval_pending", userID, "sha256:demo-payroll", now); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}
	slog.Info("provisioned demo release-center showcase", "tenant", cfg.DemoTenantID, "space", demoSpaceID)
	return nil
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

func seedDemoRequest(tx pgx.Tx, ctx context.Context, tenantID, docID, versionID, generationID, requestID, reviewID string, required int, state, requestedBy, digest string, now time.Time) error {
	_, err := tx.Exec(ctx, `INSERT INTO release_center_requests (
			request_id,tenant_id,document_id,document_version_id,generation_id,expected_chunk_count,expected_chunk_digest,
			release_revision,review_id,required_approvals,state,requested_by,created_at,updated_at
		) VALUES ($1,$2,$3,$4,$5,3,$6,1,$7,$8,$9,$10,$11,$11)
		ON CONFLICT (request_id) DO NOTHING`,
		requestID, tenantID, docID, versionID, generationID, digest, reviewID, required, state, requestedBy, now)
	if err != nil {
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
	identity := indexmanifest.GenerationIdentity{
		VersionIdentity: indexmanifest.VersionIdentity{
			TenantID: cfg.DemoTenantID, DocumentID: demoPublishedDocID, DocumentVersionID: demoPublishedVersionID,
		},
		GenerationID: demoPublishedGeneration,
	}
	observed, err := qdrant.ObserveGeneration(ctx, identity)
	if err == nil && observed.Count >= len(demoHandbookChunks()) {
		return nil
	}
	encoder := sparse.NewEncoder(sparse.DefaultParams())
	for i, content := range demoHandbookChunks() {
		chunk := model.Chunk{
			ChunkID:    fmt.Sprintf("%s-%d", demoPublishedDocID, i),
			DocID:      demoPublishedDocID,
			TenantID:   cfg.DemoTenantID,
			Content:    content,
			Index:      i,
			Permission: "public",
			FileHash:   "sha256:demo-handbook",
			CreatedAt:  time.Now().UTC(),
			Metadata: map[string]string{
				"knowledge_base_id": demoSpaceID,
				"applicable_scope":  "production",
			},
			SparseVector: encoder.Encode(content),
		}
		if err := embed.Embed(ctx, &chunk); err != nil {
			return fmt.Errorf("embed demo handbook chunk %d: %w", i, err)
		}
		if err := qdrant.UpsertGeneration(ctx, identity, chunk); err != nil {
			return fmt.Errorf("index demo handbook in qdrant: %w", err)
		}
		if err := elastic.UpsertGeneration(ctx, identity, chunk); err != nil {
			return fmt.Errorf("index demo handbook in elasticsearch: %w", err)
		}
	}
	slog.Info("provisioned demo handbook retrieval index", "tenant", cfg.DemoTenantID, "document", demoPublishedDocID)
	return nil
}

func indexDemoShowcase(ctx context.Context, cfg config.Config, qdrant demoChunkIndexer) error {
	if !demoLoginEnabled(cfg) || qdrant == nil {
		return nil
	}
	esIndexer, err := es.NewHTTPIndexer(cfg.ESAddress, cfg.ESAPIKey, cfg.ESIndex)
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
	ctx, cancel := context.WithTimeout(ctx, timeout*time.Duration(len(demoHandbookChunks())+1))
	defer cancel()
	return ensureDemoShowcaseIndex(ctx, cfg, qdrant, esIndexer, emb)
}
