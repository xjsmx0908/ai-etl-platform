package session

import (
	"context"
	"errors"
	"sync"
	"time"
)

type memoryStore struct {
	mu                   sync.Mutex
	records              map[[32]byte]record
	subjectRevokedBefore map[subjectKey]time.Time
}

type subjectKey struct {
	tenantID  string
	subjectID string
}

func NewMemoryStore() *memoryStore {
	return &memoryStore{
		records:              make(map[[32]byte]record),
		subjectRevokedBefore: make(map[subjectKey]time.Time),
	}
}

func (s *memoryStore) create(_ context.Context, value record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := subjectKey{tenantID: value.tenantID, subjectID: value.subjectID}
	if revokedBefore, found := s.subjectRevokedBefore[key]; found && !value.authenticatedAt.After(revokedBefore) {
		return errChanged
	}
	if _, exists := s.records[value.credentialDigest]; exists {
		return errors.New("credential collision")
	}
	s.records[value.credentialDigest] = value
	return nil
}

func (s *memoryStore) load(_ context.Context, digest [32]byte) (record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, found := s.records[digest]
	return value, found, nil
}

func (s *memoryStore) touch(_ context.Context, id string, generation int64, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for digest, value := range s.records {
		if value.id == id && value.generation == generation {
			value.lastActivityAt = at
			s.records[digest] = value
			return nil
		}
	}
	return errChanged
}

func (s *memoryStore) rotate(_ context.Context, oldDigest [32]byte, id string, generation int64, replacement record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, found := s.records[oldDigest]
	if !found || current.id != id || current.generation != generation || current.revokedAt != nil {
		return errChanged
	}
	if _, collision := s.records[replacement.credentialDigest]; collision {
		return errors.New("credential collision")
	}
	key := subjectKey{tenantID: replacement.tenantID, subjectID: replacement.subjectID}
	if revokedBefore, exists := s.subjectRevokedBefore[key]; exists && !replacement.authenticatedAt.After(revokedBefore) {
		return errChanged
	}
	delete(s.records, oldDigest)
	s.records[replacement.credentialDigest] = replacement
	return nil
}

func (s *memoryStore) revokeCurrent(_ context.Context, digest [32]byte, sessionID string, at time.Time, correlationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionID != "" {
		for currentDigest, value := range s.records {
			if value.id == sessionID && value.revokedAt == nil {
				value.revokedAt = &at
				value.revokedCorrelationID = correlationID
				value.generation++
				s.records[currentDigest] = value
				return nil
			}
		}
		return nil
	}
	value, found := s.records[digest]
	if !found || value.revokedAt != nil {
		return nil
	}
	value.revokedAt = &at
	value.revokedCorrelationID = correlationID
	value.generation++
	s.records[digest] = value
	return nil
}

func (s *memoryStore) revokeSubject(_ context.Context, command subjectRevocation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	credential, found := s.records[command.credentialDigest]
	idleTimeout := command.revokedAt.Sub(command.idleCutoff)
	if !found || !credential.activeAt(command.revokedAt, idleTimeout, command.policyRevision) {
		return errChanged
	}
	s.subjectRevokedBefore[subjectKey{tenantID: credential.tenantID, subjectID: credential.subjectID}] = command.revokedAt
	for digest, value := range s.records {
		if value.tenantID == credential.tenantID && value.subjectID == credential.subjectID && value.revokedAt == nil {
			value.revokedAt = &command.revokedAt
			value.revokedCorrelationID = command.correlationID
			value.generation++
			s.records[digest] = value
		}
	}
	return nil
}

var _ repository = (*memoryStore)(nil)
