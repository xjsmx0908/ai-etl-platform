package session

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSubjectRevokeRejectsCredentialRevokedAtAtomicStoreSeam(t *testing.T) {
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	store := &revocationRaceStore{record: record{
		id: "session-1", tenantID: "demo-tenant", subjectID: "user-42",
		authenticationMethod: "federated", assurance: "demo-mfa",
		authenticatedAt: now, createdAt: now, lastActivityAt: now,
		absoluteExpiresAt: now.Add(time.Hour), generation: 1, policyRevision: "personal-demo-v1",
	}}
	manager, err := New(store, Policy{
		IdleTimeout: 30 * time.Minute, AbsoluteLifetime: 8 * time.Hour,
		HighRiskFreshness: 10 * time.Minute, HighRiskAssurance: "demo-mfa",
		ActionRisks: map[string]Risk{"knowledge.query": RiskStandard}, Revision: "personal-demo-v1",
	}, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}

	err = manager.Revoke(context.Background(), RevokeCommand{
		Credential: "credential", Scope: RevokeSubject, CorrelationID: "revoke-all-1",
	})
	if !errors.Is(err, ErrInvalid) || store.subjectRevoked {
		t.Fatalf("error=%v subject_revoked=%t", err, store.subjectRevoked)
	}
}

type revocationRaceStore struct {
	record         record
	subjectRevoked bool
}

func (*revocationRaceStore) create(context.Context, record) error { return nil }
func (s *revocationRaceStore) load(context.Context, [32]byte) (record, bool, error) {
	return s.record, true, nil
}
func (*revocationRaceStore) touch(context.Context, string, int64, time.Time) error { return nil }
func (*revocationRaceStore) rotate(context.Context, [32]byte, string, int64, record) error {
	return nil
}
func (*revocationRaceStore) revokeCurrent(context.Context, [32]byte, string, time.Time, string) error {
	return nil
}
func (s *revocationRaceStore) revokeSubject(context.Context, subjectRevocation) error {
	return errChanged
}
