// Package deletionworkflow owns restart-safe document deletion from acceptance
// through exact dependency cleanup and final authoritative removal.
package deletionworkflow

import (
	"errors"
	"time"
)

var (
	ErrInvalid  = errors.New("deletionworkflow: invalid request")
	ErrNotFound = errors.New("deletionworkflow: document not found")
	ErrConflict = errors.New("deletionworkflow: stale claim")
)

type State string

const (
	StatePending    State = "pending"
	StateProcessing State = "processing"
)

type AcceptRequest struct {
	TenantID    string
	DocumentID  string
	ActorUserID string
	ActorRole   string
}

type Job struct {
	JobID                  string
	TenantID               string
	DocumentID             string
	ObjectPrefix           string
	ObjectKeys             []string
	State                  State
	Attempts               int
	ClaimToken             string
	LeaseUntil             time.Time
	QdrantDeletedAt        time.Time
	ElasticsearchDeletedAt time.Time
	ObjectsDeletedAt       time.Time
	LastError              string
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type OperationsSnapshot struct {
	Pending       int
	Processing    int
	Failed        int
	ExpiredLeases int
	OldestAge     time.Duration
}
