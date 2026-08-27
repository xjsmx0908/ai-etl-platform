package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ai-etl-pipeline/internal/db"
	"ai-etl-pipeline/internal/docstore"

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
	if _, err := tx.Exec(ctx, "UPDATE ingestion_jobs SET status='published', published_at=$2, updated_at=now() WHERE event_id=$1", eventID, publishedAt); err != nil {
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

var _ Store = (*PostgresStore)(nil)
