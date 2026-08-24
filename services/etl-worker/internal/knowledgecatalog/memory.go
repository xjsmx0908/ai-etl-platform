package knowledgecatalog

import (
	"context"
	"sort"
)

// MemoryStore is a deterministic adapter used by policy tests and isolated
// evaluation. It follows the same tenant and membership rules as PostgreSQL.
type MemoryStore struct {
	spaces      map[string]Space
	memberships map[string]Membership
	documents   map[string]DocumentPolicy
}

func NewMemoryStore(spaces []Space, memberships []Membership, documents []DocumentPolicy) *MemoryStore {
	store := &MemoryStore{
		spaces:      make(map[string]Space, len(spaces)),
		memberships: make(map[string]Membership, len(memberships)),
		documents:   make(map[string]DocumentPolicy, len(documents)),
	}
	for _, space := range spaces {
		store.spaces[space.ID] = space
	}
	for _, membership := range memberships {
		store.memberships[membership.SpaceID+"\x00"+membership.UserID] = membership
	}
	for _, document := range documents {
		store.documents[document.DocID] = document
	}
	return store
}

func (s *MemoryStore) Space(_ context.Context, tenantID, spaceID string) (Space, bool, error) {
	space, ok := s.spaces[spaceID]
	return space, ok && space.TenantID == tenantID, nil
}

func (s *MemoryStore) DefaultSpace(_ context.Context, tenantID, userID string, admin bool) (Space, bool, error) {
	for _, space := range s.spaces {
		if space.TenantID != tenantID || !space.IsDefault || !space.Active || space.Kind != SpaceKindProduction {
			continue
		}
		if admin {
			return space, true, nil
		}
		if _, ok := s.memberships[space.ID+"\x00"+userID]; ok {
			return space, true, nil
		}
	}
	return Space{}, false, nil
}

func (s *MemoryStore) Membership(_ context.Context, _ string, spaceID, userID string) (Membership, bool, error) {
	membership, ok := s.memberships[spaceID+"\x00"+userID]
	return membership, ok, nil
}

func (s *MemoryStore) ListSpaces(_ context.Context, tenantID, userID string, admin bool) ([]Space, error) {
	spaces := make([]Space, 0)
	for _, space := range s.spaces {
		if space.TenantID != tenantID || !space.Active {
			continue
		}
		if !admin {
			if _, ok := s.memberships[space.ID+"\x00"+userID]; !ok {
				continue
			}
		}
		spaces = append(spaces, space)
	}
	sort.Slice(spaces, func(i, j int) bool {
		if spaces[i].IsDefault != spaces[j].IsDefault {
			return spaces[i].IsDefault
		}
		return spaces[i].Name < spaces[j].Name
	})
	return spaces, nil
}

func (s *MemoryStore) DocumentPolicies(_ context.Context, _ string, docIDs []string) (map[string]DocumentPolicy, error) {
	policies := make(map[string]DocumentPolicy, len(docIDs))
	for _, docID := range docIDs {
		if policy, ok := s.documents[docID]; ok {
			policies[docID] = policy
		}
	}
	return policies, nil
}

func (s *MemoryStore) CreateSpace(_ context.Context, space Space, creatorUserID string) (Space, error) {
	if _, exists := s.spaces[space.ID]; exists {
		return Space{}, ErrConflict
	}
	s.spaces[space.ID] = space
	s.memberships[space.ID+"\x00"+creatorUserID] = Membership{TenantID: space.TenantID, SpaceID: space.ID, UserID: creatorUserID, Role: MemberManager}
	return space, nil
}

var _ Store = (*MemoryStore)(nil)
