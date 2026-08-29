package deletionworkflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ai-etl-pipeline/internal/db"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type PostgresStore struct{ q db.Querier }

func NewPostgresStore(q db.Querier) *PostgresStore { return &PostgresStore{q: q} }

// Accept atomically revokes query authority, records durable cleanup work, and
// appends its immutable audit correlation. Replays return the existing job.
func (s *PostgresStore) Accept(ctx context.Context, request AcceptRequest) (Job, error) {
	if s == nil || s.q == nil || strings.TrimSpace(request.TenantID) == "" || strings.TrimSpace(request.DocumentID) == "" {
		return Job{}, ErrInvalid
	}
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return Job{}, fmt.Errorf("begin deletion acceptance: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var releaseRevision int64
	if err := tx.QueryRow(ctx, `SELECT revision FROM document_releases
		WHERE tenant_id=$1 AND document_id=$2 FOR UPDATE`, request.TenantID, request.DocumentID).Scan(&releaseRevision); errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	} else if err != nil {
		return Job{}, fmt.Errorf("lock deleting release: %w", err)
	}
	var deletionStatus string
	if err := tx.QueryRow(ctx, `SELECT deletion_status FROM documents WHERE tenant_id=$1 AND doc_id=$2 FOR UPDATE`, request.TenantID, request.DocumentID).Scan(&deletionStatus); errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	} else if err != nil {
		return Job{}, fmt.Errorf("lock deletion document: %w", err)
	}
	if deletionStatus == "pending" {
		job, err := scanJob(tx.QueryRow(ctx, jobSelect+` WHERE tenant_id=$1 AND document_id=$2`, request.TenantID, request.DocumentID))
		if err != nil {
			return Job{}, fmt.Errorf("load accepted deletion: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return Job{}, fmt.Errorf("commit deletion replay: %w", err)
		}
		return job, nil
	}
	jobID := "delete-" + uuid.NewString()
	job, err := scanJob(tx.QueryRow(ctx, `INSERT INTO document_deletion_jobs
		(job_id,tenant_id,document_id,object_prefix,object_keys)
		SELECT $1,$2,$3,$4,ARRAY(SELECT DISTINCT key FROM (
			SELECT object_key AS key FROM documents WHERE tenant_id=$2 AND doc_id=$3
			UNION SELECT task->>'file_path' AS key FROM ingestion_jobs WHERE tenant_id=$2 AND doc_id=$3
		) object_refs WHERE key<>'')
		RETURNING job_id,tenant_id,document_id,object_prefix,object_keys,state,attempts,claim_token,
		lease_until,qdrant_deleted_at,elasticsearch_deleted_at,objects_deleted_at,last_error,created_at,updated_at`,
		jobID, request.TenantID, request.DocumentID, request.TenantID+"/"+request.DocumentID))
	if err != nil {
		return Job{}, fmt.Errorf("create deletion job: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE documents SET deletion_status='pending',publication_status='retired',updated_at=now()
		WHERE tenant_id=$1 AND doc_id=$2`, request.TenantID, request.DocumentID); err != nil {
		return Job{}, fmt.Errorf("revoke deleting document: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE document_releases SET published_version_id=NULL,published_generation_id=NULL,
		revision=revision+1,updated_at=now() WHERE tenant_id=$1 AND document_id=$2`, request.TenantID, request.DocumentID); err != nil {
		return Job{}, fmt.Errorf("revoke deleting release: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ingestion_jobs SET status='failed',error='document deletion accepted',
		completed_at=now(),lease_until=NULL,updated_at=now()
		WHERE tenant_id=$1 AND doc_id=$2 AND status IN ('queued','published')`, request.TenantID, request.DocumentID); err != nil {
		return Job{}, fmt.Errorf("cancel pending ingestion for deletion: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_logs
		(tenant_id,actor_user_id,actor_role,action,resource_type,resource_id,result,detail)
		VALUES ($1,$2,$3,'delete_accepted','document',$4,'success',jsonb_build_object('deletion_job_id',$5::text))`,
		request.TenantID, request.ActorUserID, request.ActorRole, request.DocumentID, jobID); err != nil {
		return Job{}, fmt.Errorf("audit deletion acceptance: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Job{}, fmt.Errorf("commit deletion acceptance: %w", err)
	}
	return job, nil
}

func (s *PostgresStore) Claim(ctx context.Context, claim ClaimRequest) ([]Job, error) {
	if s == nil || s.q == nil || strings.TrimSpace(claim.Token) == "" {
		return nil, ErrInvalid
	}
	if claim.Limit <= 0 {
		claim.Limit = 20
	}
	if claim.Lease <= 0 {
		claim.Lease = 30 * time.Minute
	}
	rows, err := s.q.Query(ctx, `WITH candidates AS (
		SELECT d.job_id FROM document_deletion_jobs d
		WHERE d.available_at<=now() AND (d.state='pending' OR d.lease_until<=now())
		  AND NOT EXISTS (
			SELECT 1 FROM ingestion_jobs i
			WHERE i.tenant_id=d.tenant_id
			  AND i.doc_id=d.document_id
			  AND i.status='processing' AND i.lease_until>now()
		  )
		ORDER BY d.available_at,d.created_at FOR UPDATE OF d SKIP LOCKED LIMIT $1
	)
	UPDATE document_deletion_jobs j SET state='processing',attempts=attempts+1,
		lease_until=now()+$2::interval,claim_token=$3,updated_at=now()
	FROM candidates c WHERE j.job_id=c.job_id
	RETURNING j.job_id,j.tenant_id,j.document_id,j.object_prefix,j.object_keys,j.state,j.attempts,j.claim_token,
		j.lease_until,j.qdrant_deleted_at,j.elasticsearch_deleted_at,j.objects_deleted_at,j.last_error,j.created_at,j.updated_at`,
		claim.Limit, claim.Lease.String(), claim.Token)
	if err != nil {
		return nil, fmt.Errorf("claim deletion jobs: %w", err)
	}
	defer rows.Close()
	jobs := make([]Job, 0, claim.Limit)
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan deletion claim: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate deletion claims: %w", err)
	}
	return jobs, nil
}

func (s *PostgresStore) Finish(ctx context.Context, result FinishResult) error {
	job := result.Job
	if s == nil || s.q == nil || job.JobID == "" || job.TenantID == "" || job.DocumentID == "" || job.ClaimToken == "" {
		return ErrInvalid
	}
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin deletion completion: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var currentToken string
	var qdrantAt, elasticsearchAt, objectsAt *time.Time
	err = tx.QueryRow(ctx, `SELECT claim_token,qdrant_deleted_at,elasticsearch_deleted_at,objects_deleted_at
		FROM document_deletion_jobs WHERE job_id=$1 FOR UPDATE`, job.JobID).
		Scan(&currentToken, &qdrantAt, &elasticsearchAt, &objectsAt)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && currentToken != job.ClaimToken {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("lock deletion completion: %w", err)
	}
	complete := (qdrantAt != nil || result.QdrantDeleted) &&
		(elasticsearchAt != nil || result.ElasticsearchDeleted) &&
		(objectsAt != nil || result.ObjectsDeleted) && result.Reason == ""
	if !complete {
		if result.RetryAfter <= 0 {
			result.RetryAfter = time.Minute
		}
		tag, err := tx.Exec(ctx, `UPDATE document_deletion_jobs SET
			qdrant_deleted_at=CASE WHEN $3 THEN COALESCE(qdrant_deleted_at,now()) ELSE qdrant_deleted_at END,
			elasticsearch_deleted_at=CASE WHEN $4 THEN COALESCE(elasticsearch_deleted_at,now()) ELSE elasticsearch_deleted_at END,
			objects_deleted_at=CASE WHEN $5 THEN COALESCE(objects_deleted_at,now()) ELSE objects_deleted_at END,
			state='pending',available_at=now()+$6::interval,lease_until=NULL,claim_token='',last_error=$7,updated_at=now()
			WHERE job_id=$1 AND claim_token=$2`, job.JobID, job.ClaimToken, result.QdrantDeleted,
			result.ElasticsearchDeleted, result.ObjectsDeleted, result.RetryAfter.String(), result.Reason)
		if err != nil {
			return fmt.Errorf("persist deletion progress: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_logs
		(tenant_id,actor_user_id,actor_role,action,resource_type,resource_id,result,detail)
		VALUES ($1,'system','worker','delete_completed','document',$2,'success',jsonb_build_object('deletion_job_id',$3::text))`,
		job.TenantID, job.DocumentID, job.JobID); err != nil {
		return fmt.Errorf("audit deletion completion: %w", err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM documents WHERE tenant_id=$1 AND doc_id=$2
		AND EXISTS (SELECT 1 FROM document_deletion_jobs WHERE job_id=$3 AND claim_token=$4)`,
		job.TenantID, job.DocumentID, job.JobID, job.ClaimToken)
	if err != nil {
		return fmt.Errorf("finalize deleting document: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	if _, err := tx.Exec(ctx, `DELETE FROM document_deletion_jobs WHERE job_id=$1 AND claim_token=$2`, job.JobID, job.ClaimToken); err != nil {
		return fmt.Errorf("finalize deletion job: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit deletion completion: %w", err)
	}
	return nil
}

func (s *PostgresStore) OperationsSnapshot(ctx context.Context) (OperationsSnapshot, error) {
	var snapshot OperationsSnapshot
	var oldestSeconds float64
	err := s.q.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE state='pending'),
		count(*) FILTER (WHERE state='processing'),
		count(*) FILTER (WHERE last_error<>''),
		count(*) FILTER (WHERE state='processing' AND lease_until<=now()),
		COALESCE(EXTRACT(EPOCH FROM now()-min(created_at)),0)
		FROM document_deletion_jobs`).Scan(&snapshot.Pending, &snapshot.Processing,
		&snapshot.Failed, &snapshot.ExpiredLeases, &oldestSeconds)
	if err != nil {
		return OperationsSnapshot{}, fmt.Errorf("load deletion operations snapshot: %w", err)
	}
	snapshot.OldestAge = time.Duration(oldestSeconds * float64(time.Second))
	return snapshot, nil
}

const jobSelect = `SELECT job_id,tenant_id,document_id,object_prefix,object_keys,state,attempts,claim_token,
	lease_until,qdrant_deleted_at,elasticsearch_deleted_at,objects_deleted_at,last_error,created_at,updated_at
	FROM document_deletion_jobs`

func scanJob(row pgx.Row) (Job, error) {
	var job Job
	var leaseUntil, qdrantAt, elasticsearchAt, objectsAt *time.Time
	err := row.Scan(&job.JobID, &job.TenantID, &job.DocumentID, &job.ObjectPrefix, &job.ObjectKeys, &job.State,
		&job.Attempts, &job.ClaimToken, &leaseUntil, &qdrantAt, &elasticsearchAt, &objectsAt,
		&job.LastError, &job.CreatedAt, &job.UpdatedAt)
	if leaseUntil != nil {
		job.LeaseUntil = *leaseUntil
	}
	if qdrantAt != nil {
		job.QdrantDeletedAt = *qdrantAt
	}
	if elasticsearchAt != nil {
		job.ElasticsearchDeletedAt = *elasticsearchAt
	}
	if objectsAt != nil {
		job.ObjectsDeletedAt = *objectsAt
	}
	return job, err
}
