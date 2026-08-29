package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ai-etl-pipeline/internal/db"
	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/publicationrelease"

	"github.com/jackc/pgx/v5"
)

// PostgresStore atomically writes the document projection, job, and outbox row.
type PostgresStore struct{ q db.Querier }

func NewPostgresStore(q db.Querier) *PostgresStore { return &PostgresStore{q: q} }

func (s *PostgresStore) Admit(ctx context.Context, sub Submission) (Receipt, error) {
	if sub.JobID == "" || sub.EventID == "" || sub.RequestSignature == "" || sub.Document.TenantID == "" || sub.Document.DocID == "" {
		return Receipt{}, ErrInvalidSubmission
	}
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return Receipt{}, fmt.Errorf("begin admission: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// Serialize replacements for the same tenant/document identity even when no
	// document row exists yet. PostgreSQL's transaction advisory lock is scoped
	// to this transaction and does not require a schema change.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", sub.Document.TenantID+"/"+sub.Document.DocID); err != nil {
		return Receipt{}, fmt.Errorf("lock admission identity: %w", err)
	}
	var existing Receipt
	var existingSignature string
	err = tx.QueryRow(ctx, `SELECT job_id,event_id,tenant_id,doc_id,request_signature FROM ingestion_jobs WHERE job_id=$1`, sub.JobID).
		Scan(&existing.JobID, &existing.EventID, &existing.TenantID, &existing.DocID, &existingSignature)
	if err == nil {
		if existingSignature != sub.RequestSignature {
			return Receipt{}, ErrAdmissionConflict
		}
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
			return Receipt{}, fmt.Errorf("finish repeated admission: %w", rollbackErr)
		}
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Receipt{}, fmt.Errorf("lookup ingestion job: %w", err)
	}
	if err := docstore.New(tx).Upsert(ctx, sub.Document); err != nil {
		return Receipt{}, err
	}
	taskJSON, err := json.Marshal(sub.Task)
	if err != nil {
		return Receipt{}, fmt.Errorf("marshal task: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_jobs (job_id,event_id,tenant_id,doc_id,request_signature,task)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb)`, sub.JobID, sub.EventID, sub.Document.TenantID, sub.Document.DocID, sub.RequestSignature, string(taskJSON)); err != nil {
		return Receipt{}, fmt.Errorf("insert ingestion job: %w", err)
	}
	if _, err := publicationrelease.NewPostgresStore(tx).RecordCurrent(ctx, publicationrelease.VersionIdentity{
		TenantID: sub.Document.TenantID, DocumentID: sub.Document.DocID, VersionID: sub.JobID,
	}); err != nil {
		return Receipt{}, fmt.Errorf("record current document version: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ingestion_outbox (event_id,job_id,tenant_id,doc_id,task)
		VALUES ($1,$2,$3,$4,$5::jsonb)`, sub.EventID, sub.JobID, sub.Document.TenantID, sub.Document.DocID, string(taskJSON)); err != nil {
		return Receipt{}, fmt.Errorf("insert ingestion outbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Receipt{}, fmt.Errorf("commit admission: %w", err)
	}
	return Receipt{JobID: sub.JobID, EventID: sub.EventID, DocID: sub.Document.DocID, TenantID: sub.Document.TenantID}, nil
}

func (s *PostgresStore) ClaimPending(ctx context.Context, limit int, lease time.Duration) ([]OutboxEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	rows, err := s.q.Query(ctx, `
		WITH candidates AS (
			SELECT event_id FROM ingestion_outbox
			WHERE published_at IS NULL AND available_at <= now()
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE ingestion_outbox AS o
		SET claimed_at=now(), available_at=now()+$2::interval, attempts=attempts+1
		FROM candidates AS c
		WHERE o.event_id=c.event_id
		RETURNING o.event_id,o.job_id,o.tenant_id,o.doc_id,o.task,o.created_at,o.attempts`, limit, lease.String())
	if err != nil {
		return nil, fmt.Errorf("claim ingestion outbox: %w", err)
	}
	defer rows.Close()
	var out []OutboxEvent
	for rows.Next() {
		var e OutboxEvent
		var raw []byte
		if err := rows.Scan(&e.EventID, &e.JobID, &e.TenantID, &e.DocID, &raw, &e.CreatedAt, &e.Attempts); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &e.Task); err != nil {
			return nil, fmt.Errorf("decode outbox task: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *PostgresStore) MarkPublished(ctx context.Context, eventID string, publishedAt time.Time) error {
	if publishedAt.IsZero() {
		publishedAt = time.Now().UTC()
	}
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, "UPDATE ingestion_outbox SET published_at=$2, claimed_at=NULL WHERE event_id=$1 AND published_at IS NULL", eventID, publishedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE ingestion_jobs SET
		status=CASE WHEN status='queued' THEN 'published' ELSE status END,
		published_at=COALESCE(published_at,$2), updated_at=now()
		WHERE event_id=$1`, eventID, publishedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) Release(ctx context.Context, eventID string, nextAttempt time.Time) error {
	if nextAttempt.IsZero() {
		nextAttempt = time.Now().UTC()
	}
	_, err := s.q.Exec(ctx, "UPDATE ingestion_outbox SET claimed_at=NULL, available_at=$2 WHERE event_id=$1 AND published_at IS NULL", eventID, nextAttempt)
	return err
}

func (s *PostgresStore) OperationsSnapshot(ctx context.Context) (OperationsSnapshot, error) {
	snapshot := OperationsSnapshot{Jobs: map[string]int{
		"queued": 0, "published": 0, "processing": 0, "completed": 0, "failed": 0,
	}}
	var oldestAge float64
	if err := s.q.QueryRow(ctx, `
		SELECT COUNT(*)::int,
		       COUNT(*) FILTER (WHERE attempts > 0)::int,
		       COALESCE(EXTRACT(EPOCH FROM (now()-MIN(created_at))),0)::double precision
		FROM ingestion_outbox WHERE published_at IS NULL`).Scan(
		&snapshot.PendingOutbox, &snapshot.RetriedOutbox, &oldestAge,
	); err != nil {
		return OperationsSnapshot{}, fmt.Errorf("snapshot ingestion outbox: %w", err)
	}
	snapshot.OldestOutboxAge = time.Duration(oldestAge * float64(time.Second))
	rows, err := s.q.Query(ctx, `SELECT status, COUNT(*)::int FROM ingestion_jobs GROUP BY status`)
	if err != nil {
		return OperationsSnapshot{}, fmt.Errorf("snapshot ingestion jobs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return OperationsSnapshot{}, err
		}
		if _, known := snapshot.Jobs[status]; known {
			snapshot.Jobs[status] = count
		}
	}
	if err := rows.Err(); err != nil {
		return OperationsSnapshot{}, err
	}
	if err := s.q.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM ingestion_jobs
		WHERE status='processing' AND lease_until <= now()`).Scan(&snapshot.ExpiredProcessingLeases); err != nil {
		return OperationsSnapshot{}, fmt.Errorf("snapshot expired ingestion leases: %w", err)
	}
	return snapshot, nil
}

func (s *PostgresStore) IsObjectReferenced(ctx context.Context, objectKey string) (bool, error) {
	if objectKey == "" {
		return false, ErrInvalidSubmission
	}
	var referenced bool
	if err := s.q.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM documents WHERE object_key=$1)
		    OR EXISTS(
		        SELECT 1 FROM ingestion_jobs
		        WHERE task->>'file_path'=$1
		    )`, objectKey).Scan(&referenced); err != nil {
		return false, fmt.Errorf("check document object reference: %w", err)
	}
	return referenced, nil
}

func (s *PostgresStore) Claim(ctx context.Context, task model.Task, lease time.Duration) (ClaimResult, error) {
	if task.JobID == "" || task.EventID == "" || task.TenantID == "" || task.DocID == "" || task.FilePath == "" {
		return "", ErrInvalidSubmission
	}
	if lease <= 0 {
		lease = 10 * time.Minute
	}
	var state string
	err := s.q.QueryRow(ctx, `
		UPDATE ingestion_jobs SET
			status='processing', processing_started_at=COALESCE(processing_started_at,now()),
			lease_until=now()+$7::interval, updated_at=now(), error=''
		WHERE job_id=$1 AND event_id=$2 AND tenant_id=$3 AND doc_id=$4
		  AND task->>'file_path'=$5 AND COALESCE(task->>'file_hash','')=$6
		  AND EXISTS (SELECT 1 FROM documents d WHERE d.tenant_id=$3 AND d.doc_id=$4 AND d.deletion_status='active')
		  AND (status IN ('queued','published') OR (status='processing' AND lease_until <= now()))
		RETURNING status`, task.JobID, task.EventID, task.TenantID, task.DocID, task.FilePath, task.FileHash, lease.String()).Scan(&state)
	if err == nil {
		return ClaimAcquired, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("claim ingestion job: %w", err)
	}
	err = s.q.QueryRow(ctx, `
		SELECT status FROM ingestion_jobs
		WHERE job_id=$1 AND event_id=$2 AND tenant_id=$3 AND doc_id=$4
		  AND task->>'file_path'=$5 AND COALESCE(task->>'file_hash','')=$6`,
		task.JobID, task.EventID, task.TenantID, task.DocID, task.FilePath, task.FileHash).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrJobNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read ingestion job state: %w", err)
	}
	switch state {
	case "completed", "failed":
		return ClaimTerminal, nil
	case "processing":
		return ClaimBusy, nil
	default:
		return "", fmt.Errorf("%w: cannot claim state %s", ErrInvalidTransition, state)
	}
}

func (s *PostgresStore) Complete(ctx context.Context, task model.Task, completedAt time.Time) error {
	return s.markJobTerminal(ctx, task, "completed", "", completedAt)
}

func (s *PostgresStore) Fail(ctx context.Context, task model.Task, message string, completedAt time.Time) error {
	return s.markJobTerminal(ctx, task, "failed", message, completedAt)
}

func (s *PostgresStore) markJobTerminal(ctx context.Context, task model.Task, state, message string, completedAt time.Time) error {
	if task.JobID == "" || task.EventID == "" || task.TenantID == "" || task.DocID == "" {
		return ErrInvalidSubmission
	}
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin ingestion terminal transition: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	tag, err := tx.Exec(ctx, `
		UPDATE ingestion_jobs SET status=$5, completed_at=$6, lease_until=NULL,
			error=$7, updated_at=now()
		WHERE job_id=$1 AND event_id=$2 AND tenant_id=$3 AND doc_id=$4
		  AND status IN ('processing',$5)`, task.JobID, task.EventID, task.TenantID, task.DocID,
		state, completedAt, message)
	if err != nil {
		return fmt.Errorf("mark ingestion job %s: %w", state, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidTransition
	}
	var knowledgeSpaceID, deletionStatus string
	err = tx.QueryRow(ctx, `
		UPDATE documents SET status=$3, stage=$3, error=$4,
			completed_at=$5, updated_at=now(),
			publication_status=CASE
				WHEN $3='completed' AND knowledge_space_id='user-uploads' AND deletion_status='active' THEN 'published'
				ELSE publication_status
			END
		WHERE tenant_id=$1 AND doc_id=$2 AND object_key=$6
		RETURNING knowledge_space_id,deletion_status`, task.TenantID, task.DocID, state, message, completedAt, task.FilePath).Scan(&knowledgeSpaceID, &deletionStatus)
	if err != nil {
		return fmt.Errorf("mark document %s: %w", state, err)
	}
	if state == "completed" && knowledgeSpaceID == "user-uploads" && deletionStatus == "active" {
		if _, err := publicationrelease.NewPostgresStore(tx).PublishAutomatic(ctx, publicationrelease.VersionIdentity{
			TenantID: task.TenantID, DocumentID: task.DocID, VersionID: task.JobID,
		}); err != nil {
			return fmt.Errorf("automatically publish completed document release: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit ingestion terminal transition: %w", err)
	}
	return nil
}

var _ Store = (*PostgresStore)(nil)
var _ JobStore = (*PostgresStore)(nil)
