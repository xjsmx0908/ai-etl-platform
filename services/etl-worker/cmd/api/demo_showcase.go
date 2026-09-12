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
	if exists {
		return tx.Commit(ctx)
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
