package notification

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ai-etl-pipeline/internal/db"
)

// PostgresStore persists the governance notification outbox.
type PostgresStore struct{ q db.Querier }

func NewPostgresStore(q db.Querier) *PostgresStore { return &PostgresStore{q: q} }

var _ Store = (*PostgresStore)(nil)

func (s *PostgresStore) Enqueue(ctx context.Context, event Event) error {
	if s == nil || s.q == nil {
		return fmt.Errorf("notification store is not configured")
	}
	if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.DedupeKey) == "" ||
		strings.TrimSpace(event.TenantID) == "" || strings.TrimSpace(event.Type) == "" {
		return fmt.Errorf("notification event is incomplete")
	}
	event.Payload = SanitizePayload(event.Payload)
	body, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("encode notification payload: %w", err)
	}
	createdAt := event.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err = s.q.Exec(ctx, `INSERT INTO governance_notification_outbox
		(event_id,dedupe_key,tenant_id,source,source_id,event_type,payload,created_at,available_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8)
		ON CONFLICT (dedupe_key) DO NOTHING`,
		event.ID, event.DedupeKey, event.TenantID, event.Source, event.SourceID, event.Type, body, createdAt)
	if err != nil {
		return fmt.Errorf("enqueue notification: %w", err)
	}
	return nil
}

func (s *PostgresStore) ClaimPending(ctx context.Context, limit int, lease time.Duration) ([]Event, error) {
	if s == nil || s.q == nil {
		return nil, fmt.Errorf("notification store is not configured")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if lease <= 0 {
		lease = 30 * time.Second
	}
	rows, err := s.q.Query(ctx, `
		WITH candidates AS (
			SELECT event_id FROM governance_notification_outbox
			WHERE published_at IS NULL AND available_at <= now()
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE governance_notification_outbox AS o
		SET claimed_at=now(), available_at=now()+$2::interval, attempts=attempts+1
		FROM candidates AS c
		WHERE o.event_id=c.event_id
		RETURNING o.event_id,o.dedupe_key,o.tenant_id,o.source,o.source_id,o.event_type,o.payload,o.attempts,o.created_at,o.last_error`,
		limit, lease.String())
	if err != nil {
		return nil, fmt.Errorf("claim notification outbox: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var event Event
		var raw []byte
		if err := rows.Scan(&event.ID, &event.DedupeKey, &event.TenantID, &event.Source, &event.SourceID,
			&event.Type, &raw, &event.Attempts, &event.CreatedAt, &event.LastError); err != nil {
			return nil, fmt.Errorf("scan notification outbox: %w", err)
		}
		payload := map[string]string{}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &payload); err != nil {
				return nil, fmt.Errorf("decode notification payload: %w", err)
			}
		}
		event.Payload = SanitizePayload(payload)
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notification outbox: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) MarkPublished(ctx context.Context, eventID string, publishedAt time.Time) error {
	if s == nil || s.q == nil {
		return fmt.Errorf("notification store is not configured")
	}
	if publishedAt.IsZero() {
		publishedAt = time.Now().UTC()
	}
	_, err := s.q.Exec(ctx, `UPDATE governance_notification_outbox
		SET published_at=$2, claimed_at=NULL, last_error=''
		WHERE event_id=$1 AND published_at IS NULL`, eventID, publishedAt)
	if err != nil {
		return fmt.Errorf("mark notification published: %w", err)
	}
	return nil
}

func (s *PostgresStore) Release(ctx context.Context, eventID string, nextAttempt time.Time, lastError string) error {
	if s == nil || s.q == nil {
		return fmt.Errorf("notification store is not configured")
	}
	if nextAttempt.IsZero() {
		nextAttempt = time.Now().UTC()
	}
	if utf8Len(lastError) > 300 {
		lastError = string([]rune(lastError)[:300])
	}
	_, err := s.q.Exec(ctx, `UPDATE governance_notification_outbox
		SET claimed_at=NULL, available_at=$2, last_error=$3
		WHERE event_id=$1 AND published_at IS NULL`, eventID, nextAttempt, lastError)
	if err != nil {
		return fmt.Errorf("release notification: %w", err)
	}
	return nil
}

func (s *PostgresStore) Snapshot(ctx context.Context) (Snapshot, error) {
	if s == nil || s.q == nil {
		return Snapshot{}, fmt.Errorf("notification store is not configured")
	}
	var snapshot Snapshot
	var oldestAge float64
	if err := s.q.QueryRow(ctx, `
		SELECT COUNT(*)::int,
		       COUNT(*) FILTER (WHERE attempts > 0)::int,
		       COALESCE(EXTRACT(EPOCH FROM (now()-MIN(created_at))),0)::double precision
		FROM governance_notification_outbox WHERE published_at IS NULL`).Scan(
		&snapshot.Pending, &snapshot.Retried, &oldestAge); err != nil {
		return Snapshot{}, fmt.Errorf("snapshot notification outbox: %w", err)
	}
	snapshot.OldestAge = time.Duration(oldestAge * float64(time.Second))
	return snapshot, nil
}

func utf8Len(value string) int {
	return len([]rune(value))
}
