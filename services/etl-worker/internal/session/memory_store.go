package session

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

type memoryStore struct {
	mu                   sync.Mutex
	records              map[[32]byte]record
	subjectRevokedBefore map[subjectKey]time.Time
	nextCreationOrder    int64
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

func (s *memoryStore) create(_ context.Context, command creation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := command.value
	key := subjectKey{tenantID: value.tenantID, subjectID: value.subjectID}
	if revokedBefore, found := s.subjectRevokedBefore[key]; found && !value.authenticatedAt.After(revokedBefore) {
		return errChanged
	}
	if _, exists := s.records[value.credentialDigest]; exists {
		return errors.New("credential collision")
	}
	s.nextCreationOrder++
	value.creationOrder = s.nextCreationOrder
	if command.maxActiveSessions > 0 {
		active := make([]record, 0)
		for _, candidate := range s.records {
			if candidate.tenantID == value.tenantID && candidate.subjectID == value.subjectID &&
				candidate.revokedAt == nil && candidate.absoluteExpiresAt.After(command.at) &&
				candidate.lastActivityAt.After(command.idleCutoff) && candidate.policyRevision == command.policyRevision {
				active = append(active, candidate)
			}
		}
		sort.Slice(active, func(i, j int) bool { return active[i].creationOrder < active[j].creationOrder })
		for len(active) >= command.maxActiveSessions {
			oldest := active[0]
			for digest, candidate := range s.records {
				if candidate.id == oldest.id {
					candidate.revokedAt = &command.at
					candidate.revokedCorrelationID = value.establishedCorrelationID
					candidate.generation++
					s.records[digest] = candidate
					break
				}
			}
			active = active[1:]
		}
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

func (s *memoryStore) listSubject(_ context.Context, command subjectList) ([]record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fence := command.fence
	current, found := s.records[fence.credentialDigest]
	if !found || current.id != fence.currentSessionID || current.tenantID != fence.tenantID ||
		current.subjectID != fence.subjectID || current.revokedAt != nil ||
		!current.absoluteExpiresAt.After(fence.at) || !current.lastActivityAt.After(fence.idleCutoff) ||
		current.policyRevision != fence.policyRevision {
		return nil, errChanged
	}
	values := make([]record, 0)
	for _, value := range s.records {
		if value.tenantID == fence.tenantID && value.subjectID == fence.subjectID {
			values = append(values, value)
		}
	}
	return values, nil
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

func (s *memoryStore) revokeCurrent(_ context.Context, command currentRevocation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if command.sessionID != "" {
		for currentDigest, value := range s.records {
			if value.id == command.sessionID && value.revokedAt == nil {
				value.revokedAt = &command.revokedAt
				value.revokedCorrelationID = command.correlationID
				value.generation++
				s.records[currentDigest] = value
				return nil
			}
		}
		return nil
	}
	value, found := s.records[command.credentialDigest]
	if !found || value.revokedAt != nil {
		return nil
	}
	value.revokedAt = &command.revokedAt
	value.revokedCorrelationID = command.correlationID
	value.generation++
	s.records[command.credentialDigest] = value
	return nil
}

func (s *memoryStore) revokeManaged(_ context.Context, command managedRevocation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fence := command.fence
	current, found := s.records[fence.credentialDigest]
	if !found || current.id != fence.currentSessionID ||
		!current.activeAt(fence.at, fence.at.Sub(fence.idleCutoff), fence.policyRevision) ||
		current.tenantID != fence.tenantID || current.subjectID != fence.subjectID {
		return errChanged
	}
	for digest, candidate := range s.records {
		if candidate.managementHandle != command.handle || candidate.tenantID != fence.tenantID ||
			candidate.subjectID != fence.subjectID {
			continue
		}
		if candidate.id == current.id {
			return errChanged
		}
		if candidate.activeAt(fence.at, fence.at.Sub(fence.idleCutoff), fence.policyRevision) {
			candidate.revokedAt = &fence.at
			candidate.revokedCorrelationID = command.correlationID
			candidate.generation++
			s.records[digest] = candidate
		}
		return nil
	}
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
