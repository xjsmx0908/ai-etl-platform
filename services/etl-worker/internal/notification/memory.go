package notification

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryStore is a process-local outbox for tests.
type MemoryStore struct {
	mu     sync.Mutex
	events map[string]Event
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{events: map[string]Event{}}
}

func (s *MemoryStore) Enqueue(_ context.Context, event Event) error {
	if s == nil {
		return fmt.Errorf("notification store is not configured")
	}
	if event.ID == "" || event.DedupeKey == "" || event.TenantID == "" {
		return fmt.Errorf("notification event is incomplete")
	}
	event.Payload = SanitizePayload(event.Payload)
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.events[event.DedupeKey]; exists {
		return nil
	}
	s.events[event.DedupeKey] = event
	return nil
}

func (s *MemoryStore) ClaimPending(_ context.Context, limit int, _ time.Duration) ([]Event, error) {
	if s == nil {
		return nil, fmt.Errorf("notification store is not configured")
	}
	if limit <= 0 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Event
	for key, event := range s.events {
		if event.PublishedAt != nil {
			continue
		}
		event.Attempts++
		s.events[key] = event
		out = append(out, event)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *MemoryStore) MarkPublished(_ context.Context, eventID string, publishedAt time.Time) error {
	if publishedAt.IsZero() {
		publishedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, event := range s.events {
		if event.ID != eventID || event.PublishedAt != nil {
			continue
		}
		stamp := publishedAt
		event.PublishedAt = &stamp
		event.LastError = ""
		s.events[key] = event
		return nil
	}
	return nil
}

func (s *MemoryStore) Release(_ context.Context, eventID string, nextAttempt time.Time, lastError string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, event := range s.events {
		if event.ID != eventID || event.PublishedAt != nil {
			continue
		}
		event.AvailableAt = nextAttempt
		event.LastError = lastError
		s.events[key] = event
		return nil
	}
	return nil
}

func (s *MemoryStore) Snapshot(context.Context) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var snapshot Snapshot
	var oldest time.Time
	now := time.Now().UTC()
	for _, event := range s.events {
		if event.PublishedAt != nil {
			continue
		}
		snapshot.Pending++
		if event.Attempts > 0 {
			snapshot.Retried++
		}
		if oldest.IsZero() || event.CreatedAt.Before(oldest) {
			oldest = event.CreatedAt
		}
	}
	if !oldest.IsZero() {
		snapshot.OldestAge = now.Sub(oldest)
	}
	return snapshot, nil
}

// Events returns a copy of all stored events for assertions.
func (s *MemoryStore) Events() []Event {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, 0, len(s.events))
	for _, event := range s.events {
		cloned := event
		cloned.Payload = SanitizePayload(event.Payload)
		out = append(out, cloned)
	}
	return out
}
