// Package session owns platform-session establishment, expiry, rotation, and
// revocation behind one provider-neutral interface.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"ai-etl-pipeline/internal/auth"
)

var (
	ErrInvalid     = errors.New("session: invalid input")
	ErrUnavailable = errors.New("session: store unavailable")
	errChanged     = errors.New("session: state changed")
)

type Decision string

const (
	DecisionAllow          Decision = "allow"
	DecisionDeny           Decision = "deny"
	DecisionReauthenticate Decision = "reauthenticate"
)

type RevokeScope string

const (
	RevokeCurrent RevokeScope = "current"
	RevokeSubject RevokeScope = "subject"
)

type Risk string

const (
	RiskStandard Risk = "standard"
	RiskHigh     Risk = "high"
)

type Policy struct {
	IdleTimeout       time.Duration
	AbsoluteLifetime  time.Duration
	HighRiskFreshness time.Duration
	RequiredAssurance string
	ActionRisks       map[string]Risk
	Revision          string
}

type AuthenticationEvidence struct {
	Assurance       string
	AuthenticatedAt time.Time
}

type EstablishCommand struct {
	Principal          auth.Principal
	Evidence           AuthenticationEvidence
	ReplacesCredential string
	CorrelationID      string
}

type RevokeCommand struct {
	Credential    string
	Scope         RevokeScope
	CorrelationID string
}

type Credential struct {
	Token     string
	ExpiresAt time.Time
}

type AuthenticateResult struct {
	Decision  Decision
	Principal auth.Principal
}

type record struct {
	id                       string
	credentialDigest         [32]byte
	tenantID                 string
	subjectID                string
	authenticationMethod     auth.AuthenticationMethod
	assurance                string
	authenticatedAt          time.Time
	createdAt                time.Time
	lastActivityAt           time.Time
	absoluteExpiresAt        time.Time
	revokedAt                *time.Time
	generation               int64
	policyRevision           string
	establishedCorrelationID string
	revokedCorrelationID     string
}

func (r record) activeAt(at time.Time, idleTimeout time.Duration, policyRevision string) bool {
	return r.revokedAt == nil && at.Before(r.absoluteExpiresAt) &&
		at.Before(r.lastActivityAt.Add(idleTimeout)) && r.policyRevision == policyRevision
}

type subjectRevocation struct {
	credentialDigest [32]byte
	revokedAt        time.Time
	idleCutoff       time.Time
	policyRevision   string
	correlationID    string
}

type repository interface {
	create(context.Context, record) error
	load(context.Context, [32]byte) (record, bool, error)
	touch(context.Context, string, int64, time.Time) error
	rotate(context.Context, [32]byte, string, int64, record) error
	revokeCurrent(context.Context, [32]byte, time.Time, string) error
	revokeSubject(context.Context, subjectRevocation) error
}

type Manager struct {
	store  repository
	policy Policy
	now    func() time.Time
	token  func() (string, error)
}

type Option func(*Manager)

func WithClock(clock func() time.Time) Option {
	return func(manager *Manager) {
		if clock != nil {
			manager.now = clock
		}
	}
}

func New(store repository, policy Policy, options ...Option) (*Manager, error) {
	if store == nil || policy.IdleTimeout <= 0 || policy.AbsoluteLifetime <= 0 ||
		policy.HighRiskFreshness <= 0 || strings.TrimSpace(policy.RequiredAssurance) == "" ||
		strings.TrimSpace(policy.Revision) == "" || policy.IdleTimeout > policy.AbsoluteLifetime ||
		len(policy.ActionRisks) == 0 {
		return nil, ErrInvalid
	}
	actionRisks := make(map[string]Risk, len(policy.ActionRisks))
	for action, risk := range policy.ActionRisks {
		if strings.TrimSpace(action) == "" || (risk != RiskStandard && risk != RiskHigh) {
			return nil, ErrInvalid
		}
		actionRisks[action] = risk
	}
	policy.ActionRisks = actionRisks
	manager := &Manager{store: store, policy: policy, now: time.Now, token: randomToken}
	for _, option := range options {
		if option != nil {
			option(manager)
		}
	}
	return manager, nil
}

func (m *Manager) Establish(ctx context.Context, command EstablishCommand) (Credential, error) {
	now := m.now().UTC()
	correlationID := strings.TrimSpace(command.CorrelationID)
	if strings.TrimSpace(command.Principal.TenantID) == "" || strings.TrimSpace(command.Principal.SubjectID) == "" ||
		(command.Principal.AuthenticationMethod != auth.AuthenticationMethodLocal &&
			command.Principal.AuthenticationMethod != auth.AuthenticationMethodFederated) ||
		command.Evidence.Assurance != m.policy.RequiredAssurance ||
		command.Evidence.AuthenticatedAt.IsZero() || command.Evidence.AuthenticatedAt.After(now) ||
		now.Sub(command.Evidence.AuthenticatedAt) >= m.policy.HighRiskFreshness ||
		correlationID == "" || correlationID != command.CorrelationID || len(correlationID) > 256 {
		return Credential{}, ErrInvalid
	}
	token, err := m.token()
	if err != nil {
		return Credential{}, fmt.Errorf("%w: generate credential", ErrUnavailable)
	}
	expiresAt := now.Add(m.policy.AbsoluteLifetime)
	record := record{
		id: uuid.NewString(), credentialDigest: sha256.Sum256([]byte(token)),
		tenantID: command.Principal.TenantID, subjectID: command.Principal.SubjectID,
		authenticationMethod: command.Principal.AuthenticationMethod,
		assurance:            command.Evidence.Assurance, authenticatedAt: command.Evidence.AuthenticatedAt.UTC(),
		createdAt: now, lastActivityAt: now, absoluteExpiresAt: expiresAt, generation: 1,
		policyRevision: m.policy.Revision, establishedCorrelationID: correlationID,
	}
	if command.ReplacesCredential != "" {
		oldDigest := sha256.Sum256([]byte(command.ReplacesCredential))
		current, found, loadErr := m.store.load(ctx, oldDigest)
		if loadErr != nil {
			return Credential{}, fmt.Errorf("%w: load replacement", ErrUnavailable)
		}
		if !found || !current.activeAt(now, m.policy.IdleTimeout, m.policy.Revision) ||
			current.tenantID != record.tenantID || current.subjectID != record.subjectID ||
			current.authenticationMethod != record.authenticationMethod {
			return Credential{}, ErrInvalid
		}
		record.id = current.id
		record.generation = current.generation + 1
		record.createdAt = current.createdAt
		record.absoluteExpiresAt = current.absoluteExpiresAt
		if err := m.store.rotate(ctx, oldDigest, current.id, current.generation, record); err != nil {
			if errors.Is(err, errChanged) {
				return Credential{}, ErrInvalid
			}
			return Credential{}, fmt.Errorf("%w: rotate credential", ErrUnavailable)
		}
		return Credential{Token: token, ExpiresAt: record.absoluteExpiresAt}, nil
	}
	if err := m.store.create(ctx, record); err != nil {
		if errors.Is(err, errChanged) {
			return Credential{}, ErrInvalid
		}
		return Credential{}, fmt.Errorf("%w: establish", ErrUnavailable)
	}
	return Credential{Token: token, ExpiresAt: expiresAt}, nil
}

func (m *Manager) Authenticate(ctx context.Context, token, action string) (AuthenticateResult, error) {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(action) == "" {
		return AuthenticateResult{Decision: DecisionDeny}, ErrInvalid
	}
	now := m.now().UTC()
	record, found, err := m.store.load(ctx, sha256.Sum256([]byte(token)))
	if err != nil {
		return AuthenticateResult{Decision: DecisionDeny}, fmt.Errorf("%w: authenticate", ErrUnavailable)
	}
	if !found || !record.activeAt(now, m.policy.IdleTimeout, m.policy.Revision) {
		return AuthenticateResult{Decision: DecisionDeny}, nil
	}
	risk, registered := m.policy.ActionRisks[action]
	if !registered {
		return AuthenticateResult{Decision: DecisionDeny}, nil
	}
	if risk == RiskHigh && now.Sub(record.authenticatedAt) >= m.policy.HighRiskFreshness {
		return AuthenticateResult{Decision: DecisionReauthenticate, Principal: principalFromRecord(record)}, nil
	}
	if err := m.store.touch(ctx, record.id, record.generation, now); err != nil {
		if errors.Is(err, errChanged) {
			return AuthenticateResult{Decision: DecisionDeny}, nil
		}
		return AuthenticateResult{Decision: DecisionDeny}, fmt.Errorf("%w: update activity", ErrUnavailable)
	}
	return AuthenticateResult{Decision: DecisionAllow, Principal: principalFromRecord(record)}, nil
}

func principalFromRecord(record record) auth.Principal {
	return auth.Principal{
		TenantID: record.tenantID, SubjectID: record.subjectID, AuthenticationMethod: record.authenticationMethod,
	}
}

func (m *Manager) Revoke(ctx context.Context, command RevokeCommand) error {
	correlationID := strings.TrimSpace(command.CorrelationID)
	if strings.TrimSpace(command.Credential) == "" ||
		(command.Scope != RevokeCurrent && command.Scope != RevokeSubject) ||
		correlationID == "" || correlationID != command.CorrelationID || len(correlationID) > 256 {
		return ErrInvalid
	}
	digest := sha256.Sum256([]byte(command.Credential))
	now := m.now().UTC()
	if command.Scope == RevokeCurrent {
		if err := m.store.revokeCurrent(ctx, digest, now, correlationID); err != nil {
			return fmt.Errorf("%w: revoke current", ErrUnavailable)
		}
		return nil
	}
	if err := m.store.revokeSubject(ctx, subjectRevocation{
		credentialDigest: digest,
		revokedAt:        now,
		idleCutoff:       now.Add(-m.policy.IdleTimeout),
		policyRevision:   m.policy.Revision,
		correlationID:    correlationID,
	}); err != nil {
		if errors.Is(err, errChanged) {
			return ErrInvalid
		}
		return fmt.Errorf("%w: revoke subject", ErrUnavailable)
	}
	return nil
}

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
