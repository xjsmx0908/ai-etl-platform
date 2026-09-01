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
	"sort"
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

type Assurance string

const (
	AssuranceDemoMFA       Assurance = "demo-mfa"
	AssuranceLocalPassword Assurance = "local-password"
)

type Policy struct {
	IdleTimeout             time.Duration
	AbsoluteLifetime        time.Duration
	HighRiskFreshness       time.Duration
	HighRiskAssurance       Assurance
	EstablishmentAssurances map[auth.AuthenticationMethod]Assurance
	ActionRisks             map[string]Risk
	MaxActiveSessions       int
	Revision                string
}

type AuthenticationEvidence struct {
	Assurance       Assurance
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
	Reference     Reference
	Scope         RevokeScope
	CorrelationID string
}

type ListCommand struct {
	Credential string
}

type RevokeManagedCommand struct {
	Credential    string
	Handle        ManagementHandle
	CorrelationID string
}

type ManagementHandle string

func ParseManagementHandle(value string) (ManagementHandle, error) {
	const prefix = "sm1_"
	if !strings.HasPrefix(value, prefix) || uuid.Validate(strings.TrimPrefix(value, prefix)) != nil {
		return "", ErrInvalid
	}
	return ManagementHandle(value), nil
}

type View struct {
	Handle               ManagementHandle
	AuthenticationMethod auth.AuthenticationMethod
	CreatedAt            time.Time
	LastActivityAt       time.Time
	ExpiresAt            time.Time
	Current              bool
}

// Reference identifies an authenticated logical session without exposing its
// stable store identifier. Only Authenticate can create a non-zero reference.
type Reference struct {
	id               string
	credentialDigest [32]byte
}

type Credential struct {
	Token     string
	ExpiresAt time.Time
}

type AuthenticateResult struct {
	Decision  Decision
	Principal auth.Principal
	Reference Reference
}

type record struct {
	id                       string
	managementHandle         ManagementHandle
	creationOrder            int64
	credentialDigest         [32]byte
	tenantID                 string
	subjectID                string
	authenticationMethod     auth.AuthenticationMethod
	assurance                Assurance
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

type currentRevocation struct {
	credentialDigest [32]byte
	sessionID        string
	revokedAt        time.Time
	correlationID    string
}

type creation struct {
	value             record
	maxActiveSessions int
	at                time.Time
	idleCutoff        time.Time
	policyRevision    string
}

type managedRevocation struct {
	fence         currentSessionFence
	handle        ManagementHandle
	correlationID string
}

type subjectList struct {
	fence currentSessionFence
}

type currentSessionFence struct {
	credentialDigest [32]byte
	currentSessionID string
	tenantID         string
	subjectID        string
	at               time.Time
	idleCutoff       time.Time
	policyRevision   string
}

type repository interface {
	create(context.Context, creation) error
	load(context.Context, [32]byte) (record, bool, error)
	listSubject(context.Context, subjectList) ([]record, error)
	touch(context.Context, string, int64, time.Time) error
	rotate(context.Context, [32]byte, string, int64, record) error
	revokeCurrent(context.Context, currentRevocation) error
	revokeManaged(context.Context, managedRevocation) error
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
		policy.HighRiskFreshness <= 0 || strings.TrimSpace(string(policy.HighRiskAssurance)) == "" ||
		strings.TrimSpace(policy.Revision) == "" || policy.IdleTimeout > policy.AbsoluteLifetime ||
		len(policy.ActionRisks) == 0 || policy.MaxActiveSessions < 0 {
		return nil, ErrInvalid
	}
	if len(policy.EstablishmentAssurances) == 0 {
		policy.EstablishmentAssurances = map[auth.AuthenticationMethod]Assurance{
			auth.AuthenticationMethodFederated: policy.HighRiskAssurance,
		}
	}
	highRiskMethodConfigured := false
	for method, assurance := range policy.EstablishmentAssurances {
		if (method != auth.AuthenticationMethodLocal && method != auth.AuthenticationMethodFederated) ||
			strings.TrimSpace(string(assurance)) == "" || string(assurance) != strings.TrimSpace(string(assurance)) {
			return nil, ErrInvalid
		}
		if assurance == policy.HighRiskAssurance {
			highRiskMethodConfigured = true
		}
	}
	if !highRiskMethodConfigured {
		return nil, ErrInvalid
	}
	policy.EstablishmentAssurances = cloneAssurances(policy.EstablishmentAssurances)
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
		m.policy.EstablishmentAssurances[command.Principal.AuthenticationMethod] != command.Evidence.Assurance ||
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
		id: uuid.NewString(), managementHandle: ManagementHandle("sm1_" + uuid.NewString()),
		credentialDigest: sha256.Sum256([]byte(token)),
		tenantID:         command.Principal.TenantID, subjectID: command.Principal.SubjectID,
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
		record.managementHandle = current.managementHandle
		record.creationOrder = current.creationOrder
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
	if err := m.store.create(ctx, creation{
		value: record, maxActiveSessions: m.policy.MaxActiveSessions, at: now,
		idleCutoff: now.Add(-m.policy.IdleTimeout), policyRevision: m.policy.Revision,
	}); err != nil {
		if errors.Is(err, errChanged) {
			return Credential{}, ErrInvalid
		}
		return Credential{}, fmt.Errorf("%w: establish", ErrUnavailable)
	}
	return Credential{Token: token, ExpiresAt: expiresAt}, nil
}

func (m *Manager) List(ctx context.Context, command ListCommand) ([]View, error) {
	if strings.TrimSpace(command.Credential) == "" {
		return nil, ErrInvalid
	}
	now := m.now().UTC()
	digest := sha256.Sum256([]byte(command.Credential))
	current, found, err := m.store.load(ctx, digest)
	if err != nil {
		return nil, fmt.Errorf("%w: load current session", ErrUnavailable)
	}
	if !found || !current.activeAt(now, m.policy.IdleTimeout, m.policy.Revision) {
		return nil, ErrInvalid
	}
	records, err := m.store.listSubject(ctx, subjectList{fence: currentSessionFence{
		credentialDigest: digest, currentSessionID: current.id,
		tenantID: current.tenantID, subjectID: current.subjectID,
		at: now, idleCutoff: now.Add(-m.policy.IdleTimeout), policyRevision: m.policy.Revision,
	}})
	if err != nil {
		if errors.Is(err, errChanged) {
			return nil, ErrInvalid
		}
		return nil, fmt.Errorf("%w: list sessions", ErrUnavailable)
	}
	views := make([]View, 0, len(records))
	for _, candidate := range records {
		if candidate.tenantID != current.tenantID || candidate.subjectID != current.subjectID ||
			!candidate.activeAt(now, m.policy.IdleTimeout, m.policy.Revision) {
			continue
		}
		views = append(views, View{
			Handle: candidate.managementHandle, AuthenticationMethod: candidate.authenticationMethod,
			CreatedAt: candidate.createdAt, LastActivityAt: candidate.lastActivityAt,
			ExpiresAt: candidate.absoluteExpiresAt, Current: candidate.id == current.id,
		})
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].CreatedAt.Equal(views[j].CreatedAt) {
			return views[i].Handle < views[j].Handle
		}
		return views[i].CreatedAt.After(views[j].CreatedAt)
	})
	return views, nil
}

func (m *Manager) RevokeManaged(ctx context.Context, command RevokeManagedCommand) error {
	credential := strings.TrimSpace(command.Credential)
	handle := strings.TrimSpace(string(command.Handle))
	correlationID := strings.TrimSpace(command.CorrelationID)
	parsedHandle, handleErr := ParseManagementHandle(handle)
	if credential == "" || handle != string(command.Handle) || handleErr != nil ||
		correlationID == "" || correlationID != command.CorrelationID || len(correlationID) > 256 {
		return ErrInvalid
	}
	now := m.now().UTC()
	digest := sha256.Sum256([]byte(credential))
	current, found, err := m.store.load(ctx, digest)
	if err != nil {
		return fmt.Errorf("%w: load current session", ErrUnavailable)
	}
	if !found || !current.activeAt(now, m.policy.IdleTimeout, m.policy.Revision) {
		return ErrInvalid
	}
	err = m.store.revokeManaged(ctx, managedRevocation{
		fence: currentSessionFence{
			credentialDigest: digest, currentSessionID: current.id,
			tenantID: current.tenantID, subjectID: current.subjectID,
			at: now, idleCutoff: now.Add(-m.policy.IdleTimeout), policyRevision: m.policy.Revision,
		}, handle: parsedHandle, correlationID: correlationID,
	})
	if errors.Is(err, errChanged) {
		return ErrInvalid
	}
	if err != nil {
		return fmt.Errorf("%w: revoke managed session", ErrUnavailable)
	}
	return nil
}

func cloneAssurances(source map[auth.AuthenticationMethod]Assurance) map[auth.AuthenticationMethod]Assurance {
	result := make(map[auth.AuthenticationMethod]Assurance, len(source))
	for method, assurance := range source {
		result[method] = assurance
	}
	return result
}

func (m *Manager) Authenticate(ctx context.Context, token, action string) (AuthenticateResult, error) {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(action) == "" {
		return AuthenticateResult{Decision: DecisionDeny}, ErrInvalid
	}
	now := m.now().UTC()
	digest := sha256.Sum256([]byte(token))
	record, found, err := m.store.load(ctx, digest)
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
	if risk == RiskHigh && (record.assurance != m.policy.HighRiskAssurance ||
		now.Sub(record.authenticatedAt) >= m.policy.HighRiskFreshness) {
		return AuthenticateResult{
			Decision: DecisionReauthenticate, Principal: principalFromRecord(record),
			Reference: Reference{id: record.id, credentialDigest: digest},
		}, nil
	}
	if err := m.store.touch(ctx, record.id, record.generation, now); err != nil {
		if errors.Is(err, errChanged) {
			return AuthenticateResult{
				Decision: DecisionDeny, Reference: Reference{id: record.id, credentialDigest: digest},
			}, nil
		}
		return AuthenticateResult{Decision: DecisionDeny}, fmt.Errorf("%w: update activity", ErrUnavailable)
	}
	return AuthenticateResult{
		Decision: DecisionAllow, Principal: principalFromRecord(record),
		Reference: Reference{id: record.id, credentialDigest: digest},
	}, nil
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
		(command.Reference.id != "" && uuid.Validate(command.Reference.id) != nil) ||
		(command.Scope == RevokeSubject && command.Reference.id != "") ||
		correlationID == "" || correlationID != command.CorrelationID || len(correlationID) > 256 {
		return ErrInvalid
	}
	digest := sha256.Sum256([]byte(command.Credential))
	if command.Reference.id != "" && command.Reference.credentialDigest != digest {
		return ErrInvalid
	}
	now := m.now().UTC()
	if command.Scope == RevokeCurrent {
		if err := m.store.revokeCurrent(ctx, currentRevocation{
			credentialDigest: digest, sessionID: command.Reference.id,
			revokedAt: now, correlationID: correlationID,
		}); err != nil {
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
