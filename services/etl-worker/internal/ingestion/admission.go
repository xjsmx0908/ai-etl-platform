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
	ErrJobNotFound       = errors.New("ingestion: job not found")
	ErrInvalidTransition = errors.New("ingestion: invalid job transition")
)

type ClaimResult string

const (
	ClaimAcquired ClaimResult = "acquired"
	ClaimBusy     ClaimResult = "busy"
	ClaimTerminal ClaimResult = "terminal"
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

// OperationsSnapshot is a bounded-cardinality view of durable ingestion state
// for metrics and alerting. It contains no tenant or document identifiers.
type OperationsSnapshot struct {
	PendingOutbox           int
	RetriedOutbox           int
	OldestOutboxAge         time.Duration
	Jobs                    map[string]int
	ExpiredProcessingLeases int
}

// Store atomically admits a document and its outbox event. Implementations must
// return the existing receipt when JobID has already been admitted.
type Store interface {
	Admit(context.Context, Submission) (Receipt, error)
	ClaimPending(context.Context, int, time.Duration) ([]OutboxEvent, error)
	MarkPublished(context.Context, string, time.Time) error
	Release(context.Context, string, time.Time) error
}

// JobStore is the worker-facing durable lifecycle surface. Claim grants one
// processing lease; terminal jobs make duplicate Kafka deliveries safe to ACK.
type JobStore interface {
	Claim(context.Context, model.Task, time.Duration) (ClaimResult, error)
	Complete(context.Context, model.Task, time.Time) error
	Fail(context.Context, model.Task, string, time.Time) error
}

// MemoryStore is a deterministic adapter used by unit tests and development
// mode. Production uses the PostgreSQL adapter in postgres.go.
type MemoryStore struct {
	mu         sync.Mutex
	jobs       map[string]Receipt
	byEvent    map[string]OutboxEvent
	claimed    map[string]bool
	signatures map[string]string
	states     map[string]string
	leases     map[string]time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		jobs: make(map[string]Receipt), byEvent: make(map[string]OutboxEvent),
		claimed: make(map[string]bool), signatures: make(map[string]string),
		states: make(map[string]string), leases: make(map[string]time.Time),
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
	s.states[sub.JobID] = "queued"
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
	for jobID, receipt := range s.jobs {
		if receipt.EventID == eventID && s.states[jobID] == "queued" {
			s.states[jobID] = "published"
		}
	}
	return nil
}

func (s *MemoryStore) Claim(_ context.Context, task model.Task, lease time.Duration) (ClaimResult, error) {
	if task.JobID == "" || task.EventID == "" || task.FilePath == "" {
		return "", ErrInvalidSubmission
	}
	if lease <= 0 {
		lease = 10 * time.Minute
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, ok := s.jobs[task.JobID]
	if !ok || receipt.EventID != task.EventID || receipt.TenantID != task.TenantID || receipt.DocID != task.DocID {
		return "", ErrJobNotFound
	}
	switch s.states[task.JobID] {
	case "completed", "failed":
		return ClaimTerminal, nil
	case "processing":
		if time.Now().UTC().Before(s.leases[task.JobID]) {
			return ClaimBusy, nil
		}
	}
	s.states[task.JobID] = "processing"
	s.leases[task.JobID] = time.Now().UTC().Add(lease)
	return ClaimAcquired, nil
}

func (s *MemoryStore) Complete(_ context.Context, task model.Task, _ time.Time) error {
	return s.markTerminal(task, "completed")
}

func (s *MemoryStore) Fail(_ context.Context, task model.Task, _ string, _ time.Time) error {
	return s.markTerminal(task, "failed")
}

func (s *MemoryStore) markTerminal(task model.Task, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, ok := s.jobs[task.JobID]
	if !ok || receipt.EventID != task.EventID || receipt.TenantID != task.TenantID || receipt.DocID != task.DocID {
		return ErrJobNotFound
	}
	if s.states[task.JobID] == state {
		return nil
	}
	if s.states[task.JobID] != "processing" {
		return ErrInvalidTransition
	}
	s.states[task.JobID] = state
	delete(s.leases, task.JobID)
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
var _ JobStore = (*MemoryStore)(nil)
