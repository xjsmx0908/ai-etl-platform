package session

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/db"
)

type postgresStore struct {
	q db.Querier
}

func NewPostgresStore(q db.Querier) *postgresStore {
	return &postgresStore{q: q}
}

func (s *postgresStore) create(ctx context.Context, command creation) error {
	if s == nil || s.q == nil {
		return ErrUnavailable
	}
	value := command.value
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockSubject(ctx, tx, value.tenantID, value.subjectID); err != nil {
		return err
	}
	if err := rejectStaleEvidence(ctx, tx, value.tenantID, value.subjectID, value.authenticatedAt); err != nil {
		return err
	}
	if command.maxActiveSessions > 0 {
		rows, err := tx.Query(ctx, `SELECT id FROM platform_sessions
			WHERE tenant_id=$1 AND internal_user_id=$2 AND revoked_at IS NULL
			AND absolute_expires_at>$3 AND last_activity_at>$4 AND policy_revision=$5
			ORDER BY created_at,id FOR UPDATE`, value.tenantID, value.subjectID,
			command.at, command.idleCutoff, command.policyRevision)
		if err != nil {
			return err
		}
		activeIDs := make([]string, 0)
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			activeIDs = append(activeIDs, id)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for len(activeIDs) >= command.maxActiveSessions {
			evictedID := activeIDs[0]
			activeIDs = activeIDs[1:]
			tag, err := tx.Exec(ctx, `UPDATE platform_sessions SET revoked_at=$2,
				revocation_reason='session_limit',revoked_correlation_id=$3,generation=generation+1
				WHERE id=$1 AND revoked_at IS NULL`, evictedID, command.at, value.establishedCorrelationID)
			if err != nil || tag.RowsAffected() != 1 {
				return ErrUnavailable
			}
			if _, err := tx.Exec(ctx, `INSERT INTO audit_logs (
				tenant_id,actor_user_id,action,result,detail,created_at
			) VALUES ($1,$2,'session_limit_eviction','success',jsonb_build_object(
				'reason','active_session_limit','correlation_id',$3::text
			),$4)`, value.tenantID, value.subjectID, value.establishedCorrelationID, command.at); err != nil {
				return ErrUnavailable
			}
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO platform_sessions (
		id,management_handle,credential_digest,internal_user_id,tenant_id,authentication_method,
		assurance_level,authenticated_at,created_at,last_activity_at,
		absolute_expires_at,generation,policy_revision,established_correlation_id
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		value.id, value.managementHandle, value.credentialDigest[:], value.subjectID, value.tenantID,
		string(value.authenticationMethod), string(value.assurance), value.authenticatedAt,
		value.createdAt, value.lastActivityAt, value.absoluteExpiresAt,
		value.generation, value.policyRevision, value.establishedCorrelationID,
	)
	if err != nil || tag.RowsAffected() != 1 {
		return ErrUnavailable
	}
	return tx.Commit(ctx)
}

func (s *postgresStore) load(ctx context.Context, digest [32]byte) (record, bool, error) {
	if s == nil || s.q == nil {
		return record{}, false, ErrUnavailable
	}
	var value record
	var method, assurance string
	err := s.q.QueryRow(ctx, `SELECT id,management_handle,tenant_id,internal_user_id,authentication_method,
		assurance_level,authenticated_at,created_at,last_activity_at,absolute_expires_at,
		revoked_at,generation,policy_revision,established_correlation_id
		FROM platform_sessions WHERE credential_digest=$1`, digest[:]).Scan(
		&value.id, &value.managementHandle, &value.tenantID, &value.subjectID, &method,
		&assurance, &value.authenticatedAt, &value.createdAt,
		&value.lastActivityAt, &value.absoluteExpiresAt, &value.revokedAt,
		&value.generation, &value.policyRevision, &value.establishedCorrelationID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return record{}, false, nil
	}
	if err != nil {
		return record{}, false, err
	}
	value.credentialDigest = digest
	value.authenticationMethod = auth.AuthenticationMethod(method)
	value.assurance = Assurance(assurance)
	return value, true, nil
}

func (s *postgresStore) listSubject(ctx context.Context, command subjectList) ([]record, error) {
	if s == nil || s.q == nil {
		return nil, ErrUnavailable
	}
	rows, err := s.q.Query(ctx, `SELECT id,management_handle,credential_digest,authentication_method,
		assurance_level,authenticated_at,created_at,last_activity_at,absolute_expires_at,
		revoked_at,generation,policy_revision,established_correlation_id
		FROM platform_sessions WHERE tenant_id=$1 AND internal_user_id=$2
		AND revoked_at IS NULL AND absolute_expires_at>$5 AND last_activity_at>$6
		AND policy_revision=$7
		AND EXISTS (SELECT 1 FROM platform_sessions current_session
			WHERE current_session.credential_digest=$3 AND current_session.id=$4
			AND current_session.tenant_id=$1 AND current_session.internal_user_id=$2
			AND current_session.revoked_at IS NULL AND current_session.absolute_expires_at>$5
			AND current_session.last_activity_at>$6 AND current_session.policy_revision=$7)`,
		command.tenantID, command.subjectID, command.credentialDigest[:], command.currentSessionID,
		command.at, command.idleCutoff, command.policyRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]record, 0)
	for rows.Next() {
		var value record
		var digest []byte
		var method, assurance string
		if err := rows.Scan(&value.id, &value.managementHandle, &digest, &method, &assurance,
			&value.authenticatedAt, &value.createdAt, &value.lastActivityAt, &value.absoluteExpiresAt,
			&value.revokedAt, &value.generation, &value.policyRevision, &value.establishedCorrelationID); err != nil {
			return nil, err
		}
		if len(digest) != sha256.Size {
			return nil, ErrUnavailable
		}
		copy(value.credentialDigest[:], digest)
		value.tenantID, value.subjectID = command.tenantID, command.subjectID
		value.authenticationMethod, value.assurance = auth.AuthenticationMethod(method), Assurance(assurance)
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, errChanged
	}
	return values, nil
}

func (s *postgresStore) touch(ctx context.Context, id string, generation int64, at time.Time) error {
	if s == nil || s.q == nil {
		return ErrUnavailable
	}
	tag, err := s.q.Exec(ctx, `UPDATE platform_sessions SET last_activity_at=$3
		WHERE id=$1 AND generation=$2 AND revoked_at IS NULL AND absolute_expires_at>$3`,
		id, generation, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errChanged
	}
	return nil
}

func (s *postgresStore) rotate(ctx context.Context, oldDigest [32]byte, id string, generation int64, replacement record) error {
	if s == nil || s.q == nil {
		return ErrUnavailable
	}
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := lockSubject(ctx, tx, replacement.tenantID, replacement.subjectID); err != nil {
		return err
	}
	if err := rejectStaleEvidence(ctx, tx, replacement.tenantID, replacement.subjectID, replacement.authenticatedAt); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE platform_sessions SET credential_digest=$4,
		assurance_level=$5,authenticated_at=$6,created_at=$7,last_activity_at=$8,
		absolute_expires_at=$9,generation=$10,policy_revision=$11,established_correlation_id=$12,
		revoked_at=NULL,revocation_reason='',revoked_correlation_id=''
		WHERE credential_digest=$1 AND id=$2 AND generation=$3 AND revoked_at IS NULL`,
		oldDigest[:], id, generation, replacement.credentialDigest[:], string(replacement.assurance),
		replacement.authenticatedAt, replacement.createdAt, replacement.lastActivityAt,
		replacement.absoluteExpiresAt, replacement.generation, replacement.policyRevision,
		replacement.establishedCorrelationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errChanged
	}
	return tx.Commit(ctx)
}

func (s *postgresStore) revokeCurrent(ctx context.Context, command currentRevocation) error {
	if s == nil || s.q == nil {
		return ErrUnavailable
	}
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var tenantID, subjectID string
	if command.sessionID != "" {
		err = tx.QueryRow(ctx, `UPDATE platform_sessions SET revoked_at=$2,
			revocation_reason='current_session',revoked_correlation_id=$3,generation=generation+1
			WHERE id=$1 AND revoked_at IS NULL
			RETURNING tenant_id,internal_user_id`, command.sessionID, command.revokedAt, command.correlationID).Scan(&tenantID, &subjectID)
	} else {
		err = tx.QueryRow(ctx, `UPDATE platform_sessions SET revoked_at=$2,
			revocation_reason='current_session',revoked_correlation_id=$3,generation=generation+1
			WHERE credential_digest=$1 AND revoked_at IS NULL
			RETURNING tenant_id,internal_user_id`, command.credentialDigest[:], command.revokedAt, command.correlationID).Scan(&tenantID, &subjectID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO audit_logs (
		tenant_id,actor_user_id,action,result,detail,created_at
	) VALUES ($1,$2,'session_logout','success',jsonb_build_object(
		'reason','local_session_revoked','correlation_id',$3::text
	),$4)`, tenantID, subjectID, command.correlationID, command.revokedAt)
	if err != nil || tag.RowsAffected() != 1 {
		return ErrUnavailable
	}
	return tx.Commit(ctx)
}

func (s *postgresStore) revokeManaged(ctx context.Context, command managedRevocation) error {
	if s == nil || s.q == nil {
		return ErrUnavailable
	}
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var currentID string
	err = tx.QueryRow(ctx, `SELECT id FROM platform_sessions
		WHERE credential_digest=$1 AND id=$2 AND tenant_id=$3 AND internal_user_id=$4
		AND revoked_at IS NULL AND absolute_expires_at>$5 AND last_activity_at>$6
		AND policy_revision=$7 FOR UPDATE`, command.credentialDigest[:], command.currentSessionID,
		command.tenantID, command.subjectID, command.revokedAt, command.idleCutoff, command.policyRevision).Scan(&currentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errChanged
	}
	if err != nil {
		return err
	}
	var targetID string
	err = tx.QueryRow(ctx, `SELECT id FROM platform_sessions
		WHERE management_handle=$1 AND tenant_id=$2 AND internal_user_id=$3 FOR UPDATE`,
		command.handle, command.tenantID, command.subjectID).Scan(&targetID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if targetID == currentID {
		return errChanged
	}
	tag, err := tx.Exec(ctx, `UPDATE platform_sessions SET revoked_at=$2,
		revocation_reason='managed_session',revoked_correlation_id=$3,generation=generation+1
		WHERE id=$1 AND revoked_at IS NULL`, targetID, command.revokedAt, command.correlationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}
	tag, err = tx.Exec(ctx, `INSERT INTO audit_logs (
		tenant_id,actor_user_id,action,result,detail,created_at
	) VALUES ($1,$2,'session_device_revoked','success',jsonb_build_object(
		'reason','user_managed_session','correlation_id',$3::text
	),$4)`, command.tenantID, command.subjectID, command.correlationID, command.revokedAt)
	if err != nil || tag.RowsAffected() != 1 {
		return ErrUnavailable
	}
	return tx.Commit(ctx)
}

func (s *postgresStore) revokeSubject(ctx context.Context, command subjectRevocation) error {
	if s == nil || s.q == nil {
		return ErrUnavailable
	}
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var tenantID, subjectID string
	err = tx.QueryRow(ctx, `SELECT tenant_id,internal_user_id FROM platform_sessions
		WHERE credential_digest=$1`, command.credentialDigest[:]).Scan(&tenantID, &subjectID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errChanged
	}
	if err != nil {
		return err
	}
	if err := lockSubject(ctx, tx, tenantID, subjectID); err != nil {
		return err
	}
	var active bool
	err = tx.QueryRow(ctx, `SELECT true FROM platform_sessions
		WHERE credential_digest=$1 AND tenant_id=$2 AND internal_user_id=$3
		AND revoked_at IS NULL AND absolute_expires_at>$4
		AND last_activity_at>$5 AND policy_revision=$6 FOR UPDATE`,
		command.credentialDigest[:], tenantID, subjectID, command.revokedAt,
		command.idleCutoff, command.policyRevision).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return errChanged
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform_session_subject_states (
		tenant_id,internal_user_id,revoked_before,revoked_correlation_id
		) VALUES ($1,$2,$3,$4)
		ON CONFLICT (tenant_id,internal_user_id) DO UPDATE SET
			revoked_before=GREATEST(platform_session_subject_states.revoked_before,EXCLUDED.revoked_before),
			revoked_correlation_id=CASE
				WHEN EXCLUDED.revoked_before >= platform_session_subject_states.revoked_before
				THEN EXCLUDED.revoked_correlation_id
				ELSE platform_session_subject_states.revoked_correlation_id
			END`,
		tenantID, subjectID, command.revokedAt, command.correlationID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE platform_sessions SET revoked_at=$3,
		revocation_reason='subject_sessions',revoked_correlation_id=$4,generation=generation+1
		WHERE tenant_id=$1 AND internal_user_id=$2 AND revoked_at IS NULL`, tenantID, subjectID, command.revokedAt, command.correlationID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func lockSubject(ctx context.Context, tx pgx.Tx, tenantID, subjectID string) error {
	var locked bool
	err := tx.QueryRow(ctx, `SELECT true FROM users
		WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, subjectID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return errChanged
	}
	return err
}

func rejectStaleEvidence(ctx context.Context, tx pgx.Tx, tenantID, subjectID string, authenticatedAt time.Time) error {
	var revokedBefore time.Time
	err := tx.QueryRow(ctx, `SELECT revoked_before FROM platform_session_subject_states
		WHERE tenant_id=$1 AND internal_user_id=$2`, tenantID, subjectID).Scan(&revokedBefore)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !authenticatedAt.After(revokedBefore) {
		return errChanged
	}
	return nil
}

var _ repository = (*postgresStore)(nil)
