// Package publicationrelease owns the durable identity of the current document
// version and the one release that governance has approved for queries.
package publicationrelease

import (
	"errors"
	"time"
)

var (
	ErrInvalid  = errors.New("publicationrelease: invalid identity")
	ErrConflict = errors.New("publicationrelease: compare-and-set conflict")
)

type VersionIdentity struct {
	TenantID   string
	DocumentID string
	VersionID  string
}

type Candidate struct {
	VersionIdentity
	GenerationID     string
	ExpectedRevision int64
}

type Release struct {
	TenantID              string
	DocumentID            string
	CurrentVersionID      string
	PublishedVersionID    string
	PublishedGenerationID string
	Revision              int64
	ResolutionStatus      string
	LastError             string
	UpdatedAt             time.Time
}
