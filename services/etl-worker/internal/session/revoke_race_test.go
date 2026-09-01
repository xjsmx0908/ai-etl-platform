package session

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"ai-etl-pipeline/internal/auth"
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

func TestCurrentRevokeFollowsRotationBetweenAuthenticateLoadAndTouch(t *testing.T) {
	now := time.Date(2026, 9, 1, 5, 0, 0, 0, time.UTC)
	credential := "credential-before-rotation"
	digest := sha256.Sum256([]byte(credential))
	store := &rotationDuringAuthenticationStore{record: record{
		id: "11111111-1111-1111-1111-111111111111", credentialDigest: digest,
		tenantID: "demo-tenant", subjectID: "user-42", authenticationMethod: auth.AuthenticationMethodFederated,
		assurance: AssuranceDemoMFA, authenticatedAt: now, createdAt: now, lastActivityAt: now,
		absoluteExpiresAt: now.Add(time.Hour), generation: 1, policyRevision: "personal-demo-v1",
	}}
	manager, err := New(store, Policy{
		IdleTimeout: 30 * time.Minute, AbsoluteLifetime: 8 * time.Hour,
		HighRiskFreshness: 10 * time.Minute, HighRiskAssurance: AssuranceDemoMFA,
		ActionRisks: map[string]Risk{"knowledge.query": RiskStandard}, Revision: "personal-demo-v1",
	}, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}

	result, err := manager.Authenticate(context.Background(), credential, "knowledge.query")
	if err != nil || result.Decision != DecisionDeny || result.Reference.id == "" {
		t.Fatalf("interleaved authentication result=%+v err=%v", result, err)
	}
	if err := manager.Revoke(context.Background(), RevokeCommand{
		Credential: credential, Reference: result.Reference,
		Scope: RevokeCurrent, CorrelationID: "logout-after-interleaved-rotation",
	}); err != nil {
		t.Fatal(err)
	}
	if !store.rotatedCredentialRevoked {
		t.Fatal("replacement credential survived load-touch rotation race")
	}
}

type revocationRaceStore struct {
	record         record
	subjectRevoked bool
}

type rotationDuringAuthenticationStore struct {
	record                   record
	rotatedCredentialRevoked bool
}

func (*rotationDuringAuthenticationStore) create(context.Context, record) error { return nil }
func (s *rotationDuringAuthenticationStore) load(context.Context, [32]byte) (record, bool, error) {
	return s.record, true, nil
}
func (s *rotationDuringAuthenticationStore) touch(context.Context, string, int64, time.Time) error {
	s.record.generation++
	s.record.credentialDigest = sha256.Sum256([]byte("credential-after-rotation"))
	return errChanged
}
func (*rotationDuringAuthenticationStore) rotate(context.Context, [32]byte, string, int64, record) error {
	return nil
}
func (s *rotationDuringAuthenticationStore) revokeCurrent(_ context.Context, command currentRevocation) error {
	if command.sessionID == s.record.id {
		s.rotatedCredentialRevoked = true
	}
	return nil
}
func (*rotationDuringAuthenticationStore) revokeSubject(context.Context, subjectRevocation) error {
	return nil
}

func (*revocationRaceStore) create(context.Context, record) error { return nil }
func (s *revocationRaceStore) load(context.Context, [32]byte) (record, bool, error) {
	return s.record, true, nil
}
func (*revocationRaceStore) touch(context.Context, string, int64, time.Time) error { return nil }
func (*revocationRaceStore) rotate(context.Context, [32]byte, string, int64, record) error {
	return nil
}
func (*revocationRaceStore) revokeCurrent(context.Context, currentRevocation) error {
	return nil
}
func (s *revocationRaceStore) revokeSubject(context.Context, subjectRevocation) error {
	return errChanged
}
