package releasecenter

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"ai-etl-pipeline/internal/publicationworkflow"
)

var (
	ErrInvalidApprovalPolicy = errors.New("release center: invalid approval policy")
	ErrApprovalForbidden     = errors.New("release center: approval group membership required")
)

type ApprovalGroup struct {
	ID       string   `json:"group_id"`
	TenantID string   `json:"tenant_id,omitempty"`
	Name     string   `json:"name"`
	Active   bool     `json:"active"`
	Members  []string `json:"members,omitempty"`
}

type ApprovalPolicy struct {
	ID                     string    `json:"policy_id"`
	TenantID               string    `json:"tenant_id,omitempty"`
	KnowledgeSpaceID       string    `json:"knowledge_space_id,omitempty"`
	Permission             string    `json:"permission,omitempty"`
	MinimumRisk            RiskLevel `json:"minimum_risk,omitempty"`
	RequiredApprovals      int       `json:"required_approvals"`
	ApproverGroupID        string    `json:"approver_group_id"`
	AllowRequesterApproval bool      `json:"allow_requester_approval"`
	Priority               int       `json:"priority"`
	Active                 bool      `json:"active"`
}

type ApprovalPolicyStore interface {
	ResolveApprovalPolicy(context.Context, string, string, string, RiskLevel) (ApprovalPolicy, bool, error)
	IsApprovalGroupMember(context.Context, string, string, string) (bool, error)
}

type ApprovalPolicyManager interface {
	ApprovalPolicyStore
	PutGroup(context.Context, ApprovalGroup) error
	SetGroupMember(context.Context, string, string, string, bool) error
	PutPolicy(context.Context, ApprovalPolicy) error
	ListGroups(context.Context, string) ([]ApprovalGroup, error)
	ListPolicies(context.Context, string) ([]ApprovalPolicy, error)
}

func validateApprovalPolicy(policy ApprovalPolicy) error {
	if strings.TrimSpace(policy.ID) == "" || strings.TrimSpace(policy.TenantID) == "" ||
		strings.TrimSpace(policy.ApproverGroupID) == "" || policy.RequiredApprovals < 1 || policy.RequiredApprovals > 2 {
		return ErrInvalidApprovalPolicy
	}
	if policy.MinimumRisk != "" && riskRank(policy.MinimumRisk) == 0 {
		return ErrInvalidApprovalPolicy
	}
	return nil
}

func validateApprovalGroup(group ApprovalGroup) error {
	if strings.TrimSpace(group.ID) == "" || strings.TrimSpace(group.TenantID) == "" || strings.TrimSpace(group.Name) == "" {
		return ErrInvalidApprovalPolicy
	}
	return nil
}

func policyMatches(policy ApprovalPolicy, knowledgeSpaceID, permission string, risk RiskLevel) bool {
	if !policy.Active || (policy.KnowledgeSpaceID != "" && policy.KnowledgeSpaceID != knowledgeSpaceID) ||
		(policy.Permission != "" && !strings.EqualFold(policy.Permission, permission)) {
		return false
	}
	minimum := policy.MinimumRisk
	if minimum == "" {
		minimum = RiskLow
	}
	return riskRank(risk) >= riskRank(minimum)
}

func policySpecificity(policy ApprovalPolicy) int {
	score := 0
	if policy.KnowledgeSpaceID != "" {
		score += 4
	}
	if policy.Permission != "" {
		score += 2
	}
	return score + riskRank(policy.MinimumRisk)
}

type MemoryApprovalPolicyStore struct {
	mu       sync.RWMutex
	groups   map[string]ApprovalGroup
	policies map[string]ApprovalPolicy
	members  map[string]map[string]bool
}

func NewMemoryApprovalPolicyStore() *MemoryApprovalPolicyStore {
	return &MemoryApprovalPolicyStore{groups: map[string]ApprovalGroup{}, policies: map[string]ApprovalPolicy{}, members: map[string]map[string]bool{}}
}

func (s *MemoryApprovalPolicyStore) PutGroup(_ context.Context, group ApprovalGroup) error {
	if s == nil || validateApprovalGroup(group) != nil {
		return ErrInvalidApprovalPolicy
	}
	group.ID, group.TenantID, group.Name = strings.TrimSpace(group.ID), strings.TrimSpace(group.TenantID), strings.TrimSpace(group.Name)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.groups[group.TenantID+"\x00"+group.ID] = group
	return nil
}

func (s *MemoryApprovalPolicyStore) SetGroupMember(_ context.Context, tenantID, groupID, userID string, active bool) error {
	if s == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(groupID) == "" || strings.TrimSpace(userID) == "" {
		return ErrInvalidApprovalPolicy
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.TrimSpace(tenantID) + "\x00" + strings.TrimSpace(groupID)
	if _, ok := s.groups[key]; !ok {
		return ErrInvalidApprovalPolicy
	}
	if s.members[key] == nil {
		s.members[key] = map[string]bool{}
	}
	s.members[key][strings.TrimSpace(userID)] = active
	return nil
}

func (s *MemoryApprovalPolicyStore) PutPolicy(_ context.Context, policy ApprovalPolicy) error {
	if s == nil || validateApprovalPolicy(policy) != nil {
		return ErrInvalidApprovalPolicy
	}
	policy.ID, policy.TenantID, policy.ApproverGroupID = strings.TrimSpace(policy.ID), strings.TrimSpace(policy.TenantID), strings.TrimSpace(policy.ApproverGroupID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policies[policy.TenantID+"\x00"+policy.ID] = policy
	return nil
}

func (s *MemoryApprovalPolicyStore) ResolveApprovalPolicy(_ context.Context, tenantID, knowledgeSpaceID, permission string, risk RiskLevel) (ApprovalPolicy, bool, error) {
	if s == nil {
		return ApprovalPolicy{}, false, ErrInvalidApprovalPolicy
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var matches []ApprovalPolicy
	for _, policy := range s.policies {
		if policy.TenantID == tenantID && policyMatches(policy, knowledgeSpaceID, permission, risk) {
			matches = append(matches, policy)
		}
	}
	if len(matches) == 0 {
		return ApprovalPolicy{}, false, nil
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if policySpecificity(matches[i]) != policySpecificity(matches[j]) {
			return policySpecificity(matches[i]) > policySpecificity(matches[j])
		}
		return matches[i].Priority > matches[j].Priority
	})
	return matches[0], true, nil
}

func (s *MemoryApprovalPolicyStore) IsApprovalGroupMember(_ context.Context, tenantID, groupID, userID string) (bool, error) {
	if s == nil {
		return false, ErrInvalidApprovalPolicy
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	group, ok := s.groups[strings.TrimSpace(tenantID)+"\x00"+strings.TrimSpace(groupID)]
	if !ok || !group.Active {
		return false, nil
	}
	return s.members[strings.TrimSpace(tenantID)+"\x00"+strings.TrimSpace(groupID)][strings.TrimSpace(userID)], nil
}

func (s *MemoryApprovalPolicyStore) ListGroups(_ context.Context, tenantID string) ([]ApprovalGroup, error) {
	if s == nil {
		return nil, ErrInvalidApprovalPolicy
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	groups := make([]ApprovalGroup, 0)
	for _, group := range s.groups {
		if group.TenantID == tenantID {
			groups = append(groups, group)
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	return groups, nil
}

func (s *MemoryApprovalPolicyStore) ListPolicies(_ context.Context, tenantID string) ([]ApprovalPolicy, error) {
	if s == nil {
		return nil, ErrInvalidApprovalPolicy
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	policies := make([]ApprovalPolicy, 0)
	for _, policy := range s.policies {
		if policy.TenantID == tenantID {
			policies = append(policies, policy)
		}
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].ID < policies[j].ID })
	return policies, nil
}

func AuthorizeApproval(ctx context.Context, store ApprovalPolicyStore, actor publicationworkflow.Actor, request ReleaseRequest) error {
	if strings.TrimSpace(request.ApproverGroupID) == "" {
		if !request.AllowRequesterApproval && request.RequestedBy == actor.UserID {
			return ErrApprovalForbidden
		}
		return nil
	}
	member, err := store.IsApprovalGroupMember(ctx, actor.TenantID, request.ApproverGroupID, actor.UserID)
	if err != nil {
		return fmt.Errorf("check approval group membership: %w", err)
	}
	if !member || (!request.AllowRequesterApproval && request.RequestedBy == actor.UserID) {
		return ErrApprovalForbidden
	}
	return nil
}
