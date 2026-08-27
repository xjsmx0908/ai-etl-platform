// Package ingestion owns durable admission of uploaded documents before queue
// publication. The Store interface is deliberately small so the HTTP gateway
// and the outbox relay can evolve independently of the persistence adapter.
package ingestion

import (
	"context"
	"errors"
	"sync"
	"time"

	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/model"
)

var (
	ErrInvalidSubmission = errors.New("ingestion: invalid submission")
	ErrAdmissionConflict = errors.New("ingestion: admission key reused with different request")
)

// Submission contains the already validated object metadata and the exact task
// that must eventually be published. JobID and EventID are generated once by
// the admission caller and remain stable across retries.
type Submission struct {
	JobID            string
	EventID          string
	RequestSignature string
	Document         docstore.Document
	Task             model.Task
}

// Receipt is returned after durable admission has committed.
type Receipt struct {
	JobID    string
	EventID  string
	DocID    string
	TenantID string
}

// OutboxEvent is the relay-facing representation of a pending task.
type OutboxEvent struct {
	JobID     string
	EventID   string
	TenantID  string
	DocID     string
	Task      model.Task
	CreatedAt time.Time
	Attempts  int
}

// Store atomically admits a document and its outbox event. Implementations must
// return the existing receipt when JobID has already been admitted.
type Store interface {
	Admit(context.Context, Submission) (Receipt, error)
	ClaimPending(context.Context, int, time.Duration) ([]OutboxEvent, error)
	MarkPublished(context.Context, string, time.Time) error
	Release(context.Context, string, time.Time) error
}

// MemoryStore is a deterministic adapter used by unit tests and development
// mode. Production uses the PostgreSQL adapter in postgres.go.
type MemoryStore struct {
	mu         sync.Mutex
	jobs       map[string]Receipt
	byEvent    map[string]OutboxEvent
	claimed    map[string]bool
	signatures map[string]string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		jobs: make(map[string]Receipt), byEvent: make(map[string]OutboxEvent),
		claimed: make(map[string]bool), signatures: make(map[string]string),
	}
}

func (s *MemoryStore) Admit(_ context.Context, sub Submission) (Receipt, error) {
	if sub.JobID == "" || sub.EventID == "" || sub.RequestSignature == "" || sub.Document.TenantID == "" || sub.Document.DocID == "" || sub.Task.DocID == "" {
		return Receipt{}, ErrInvalidSubmission
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if receipt, ok := s.jobs[sub.JobID]; ok {
		if s.signatures[sub.JobID] != sub.RequestSignature {
			return Receipt{}, ErrAdmissionConflict
		}
		return receipt, nil
	}
	receipt := Receipt{JobID: sub.JobID, EventID: sub.EventID, DocID: sub.Document.DocID, TenantID: sub.Document.TenantID}
	s.jobs[sub.JobID] = receipt
	s.signatures[sub.JobID] = sub.RequestSignature
	s.byEvent[sub.EventID] = OutboxEvent{
		JobID: sub.JobID, EventID: sub.EventID, TenantID: sub.Document.TenantID,
		DocID: sub.Document.DocID, Task: sub.Task, CreatedAt: time.Now().UTC(),
	}
	return receipt, nil
}

func (s *MemoryStore) ClaimPending(_ context.Context, limit int, _ time.Duration) ([]OutboxEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	out := make([]OutboxEvent, 0, limit)
	for _, event := range s.byEvent {
		if s.claimed[event.EventID] {
			continue
		}
		s.claimed[event.EventID] = true
		event.Attempts++
		s.byEvent[event.EventID] = event
		out = append(out, event)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (s *MemoryStore) MarkPublished(_ context.Context, eventID string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byEvent, eventID)
	delete(s.claimed, eventID)
	return nil
}

func (s *MemoryStore) Release(_ context.Context, eventID string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.claimed, eventID)
	return nil
}

func (s *MemoryStore) PendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byEvent)
}

var _ Store = (*MemoryStore)(nil)
