// Package knowledgecatalog owns knowledge-space authorization and document
// publication policy. Query and upload callers use this module instead of
// interpreting free-form document metadata independently.
package knowledgecatalog

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

type SpaceKind string

const (
	SpaceKindProduction SpaceKind = "production"
	SpaceKindDemo       SpaceKind = "demo"
)

type MemberRole string

const (
	MemberReader      MemberRole = "reader"
	MemberContributor MemberRole = "contributor"
	MemberManager     MemberRole = "manager"
)

type Capability string

const (
	CapabilityQuery  Capability = "query"
	CapabilityUpload Capability = "upload"
)

var (
	ErrForbidden   = errors.New("knowledge catalog: forbidden")
	ErrNotFound    = errors.New("knowledge catalog: space not found")
	ErrUnavailable = errors.New("knowledge catalog: unavailable")
	ErrConflict    = errors.New("knowledge catalog: conflict")
)

type Principal struct {
	TenantID string
	UserID   string
	Role     string
}

type Space struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id,omitempty"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	Kind      SpaceKind `json:"kind"`
	Purpose   string    `json:"purpose,omitempty"`
	IsDefault bool      `json:"is_default"`
	Active    bool      `json:"active"`
}

type Membership struct {
	TenantID string     `json:"tenant_id,omitempty"`
	SpaceID  string     `json:"space_id"`
	UserID   string     `json:"user_id"`
	Role     MemberRole `json:"role"`
}

type DocumentPolicy struct {
	DocID             string
	KnowledgeSpaceID  string
	PublicationStatus string
	DocumentStatus    string
}

type EvidenceDecision struct {
	AllowedDocIDs       map[string]bool
	UnpublishedFiltered int
	RetiredFiltered     int
}

// Store is the persistence seam. PostgreSQL and the deterministic in-memory
// adapter both satisfy it; callers interact only with Catalog.
type Store interface {
	Space(ctx context.Context, tenantID, spaceID string) (Space, bool, error)
	DefaultSpace(ctx context.Context, tenantID, userID string, admin bool) (Space, bool, error)
	Membership(ctx context.Context, tenantID, spaceID, userID string) (Membership, bool, error)
	ListSpaces(ctx context.Context, tenantID, userID string, admin bool) ([]Space, error)
	DocumentPolicies(ctx context.Context, tenantID string, docIDs []string) (map[string]DocumentPolicy, error)
	CreateSpace(ctx context.Context, space Space, creatorUserID string) (Space, error)
	UpdateSpacePurpose(ctx context.Context, tenantID, spaceID, purpose string) (Space, error)
}

var validSpaceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{1,63}$`)

// Create creates a non-default space and assigns its tenant-admin creator as a
// manager. Default changes are intentionally excluded from P0.
func (c *Catalog) Create(ctx context.Context, principal Principal, space Space) (Space, error) {
	if c == nil || c.store == nil {
		return Space{}, ErrUnavailable
	}
	if principal.Role != "admin" {
		return Space{}, ErrForbidden
	}
	space.ID = strings.TrimSpace(space.ID)
	space.Name = strings.TrimSpace(space.Name)
	if !validSpaceID.MatchString(space.ID) || space.Name == "" || len(space.Name) > 120 {
		return Space{}, ErrNotFound
	}
	if space.Kind != SpaceKindProduction && space.Kind != SpaceKindDemo {
		return Space{}, ErrNotFound
	}
	purpose, err := normalizePurpose(space.Purpose)
	if err != nil {
		return Space{}, err
	}
	space.Purpose = purpose
	space.TenantID = principal.TenantID
	space.Slug = space.ID
	space.Active = true
	space.IsDefault = false
	created, err := c.store.CreateSpace(ctx, space, principal.UserID)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			return Space{}, err
		}
		return Space{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return created, nil
}

// Catalog is the single interface used by query and upload flows.
type Catalog struct {
	store Store
}

func New(store Store) *Catalog {
	return &Catalog{store: store}
}

// Space returns a tenant-local knowledge space without capability checks.
// Review tools use this to read the owner-written purpose.
func (c *Catalog) Space(ctx context.Context, tenantID, spaceID string) (Space, bool, error) {
	if c == nil || c.store == nil {
		return Space{}, false, ErrUnavailable
	}
	return c.store.Space(ctx, tenantID, spaceID)
}

// UpdatePurpose lets a tenant admin rewrite the owner-facing purpose used by
// pre-review fitness checks. An empty purpose disables space matching.
func (c *Catalog) UpdatePurpose(ctx context.Context, principal Principal, spaceID, purpose string) (Space, error) {
	if c == nil || c.store == nil {
		return Space{}, ErrUnavailable
	}
	if principal.Role != "admin" {
		return Space{}, ErrForbidden
	}
	normalized, err := normalizePurpose(purpose)
	if err != nil {
		return Space{}, err
	}
	spaceID = strings.TrimSpace(spaceID)
	if !validSpaceID.MatchString(spaceID) {
		return Space{}, ErrNotFound
	}
	space, err := c.store.UpdateSpacePurpose(ctx, principal.TenantID, spaceID, normalized)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
			return Space{}, err
		}
		return Space{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return space, nil
}

func normalizePurpose(purpose string) (string, error) {
	purpose = strings.TrimSpace(purpose)
	if utf8.RuneCountInString(purpose) > 500 {
		return "", ErrNotFound
	}
	return purpose, nil
}

// Resolve chooses an explicit space or the caller's authorized default before
// retrieval starts. Search results never influence this decision.
func (c *Catalog) Resolve(ctx context.Context, principal Principal, requestedSpaceID string, capability Capability) (Space, error) {
	if c == nil || c.store == nil {
		return Space{}, ErrUnavailable
	}
	if strings.TrimSpace(principal.TenantID) == "" || strings.TrimSpace(principal.UserID) == "" {
		return Space{}, ErrForbidden
	}

	admin := principal.Role == "admin"
	var (
		space Space
		found bool
		err   error
	)
	if requested := strings.TrimSpace(requestedSpaceID); requested != "" {
		space, found, err = c.store.Space(ctx, principal.TenantID, requested)
	} else {
		space, found, err = c.store.DefaultSpace(ctx, principal.TenantID, principal.UserID, admin)
	}
	if err != nil {
		return Space{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !found || !space.Active {
		return Space{}, ErrNotFound
	}
	if space.TenantID != principal.TenantID {
		return Space{}, ErrForbidden
	}
	if admin {
		return space, nil
	}
	membership, ok, err := c.store.Membership(ctx, principal.TenantID, space.ID, principal.UserID)
	if err != nil {
		return Space{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !ok || !allows(membership.Role, capability) {
		return Space{}, ErrForbidden
	}
	return space, nil
}

// List returns active spaces visible to the principal.
func (c *Catalog) List(ctx context.Context, principal Principal) ([]Space, error) {
	if c == nil || c.store == nil {
		return nil, ErrUnavailable
	}
	spaces, err := c.store.ListSpaces(ctx, principal.TenantID, principal.UserID, principal.Role == "admin")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return spaces, nil
}

// FilterEvidence validates search candidates against authoritative registry
// state. Missing rows are denied: reconciliation cannot silently turn unknown
// chunks into published enterprise evidence.
func (c *Catalog) FilterEvidence(ctx context.Context, tenantID, spaceID string, docIDs []string) (EvidenceDecision, error) {
	if c == nil || c.store == nil {
		return EvidenceDecision{}, ErrUnavailable
	}
	policies, err := c.store.DocumentPolicies(ctx, tenantID, docIDs)
	if err != nil {
		return EvidenceDecision{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	decision := EvidenceDecision{AllowedDocIDs: make(map[string]bool)}
	for _, docID := range docIDs {
		policy, ok := policies[docID]
		if !ok || policy.KnowledgeSpaceID != spaceID || policy.PublicationStatus != "published" {
			decision.UnpublishedFiltered++
			continue
		}
		if policy.DocumentStatus == "superseded" || policy.DocumentStatus == "archived" {
			decision.RetiredFiltered++
			continue
		}
		decision.AllowedDocIDs[docID] = true
	}
	return decision, nil
}

func allows(role MemberRole, capability Capability) bool {
	switch capability {
	case CapabilityQuery:
		return role == MemberReader || role == MemberContributor || role == MemberManager
	case CapabilityUpload:
		return role == MemberContributor || role == MemberManager
	default:
		return false
	}
}
