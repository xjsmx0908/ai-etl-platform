package releasecenter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) PutGroup(ctx context.Context, group ApprovalGroup) error {
	if s == nil || s.q == nil {
		return fmt.Errorf("release center store is not configured")
	}
	if err := validateApprovalGroup(group); err != nil {
		return err
	}
	_, err := s.q.Exec(ctx, `INSERT INTO release_center_approval_groups (tenant_id,group_id,name,active)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (tenant_id,group_id) DO UPDATE SET name=EXCLUDED.name,active=EXCLUDED.active,updated_at=now()`,
		strings.TrimSpace(group.TenantID), strings.TrimSpace(group.ID), strings.TrimSpace(group.Name), group.Active)
	if err != nil {
		return fmt.Errorf("save approval group: %w", err)
	}
	return nil
}

func (s *PostgresStore) SetGroupMember(ctx context.Context, tenantID, groupID, userID string, active bool) error {
	if s == nil || s.q == nil {
		return fmt.Errorf("release center store is not configured")
	}
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(groupID) == "" || strings.TrimSpace(userID) == "" {
		return ErrInvalidApprovalPolicy
	}
	_, err := s.q.Exec(ctx, `INSERT INTO release_center_approval_group_members (tenant_id,group_id,user_id,active)
		VALUES ($1,$2,$3::uuid,$4)
		ON CONFLICT (tenant_id,group_id,user_id) DO UPDATE SET active=EXCLUDED.active`,
		strings.TrimSpace(tenantID), strings.TrimSpace(groupID), strings.TrimSpace(userID), active)
	if err != nil {
		return fmt.Errorf("save approval group member: %w", err)
	}
	return nil
}

func (s *PostgresStore) PutPolicy(ctx context.Context, policy ApprovalPolicy) error {
	if s == nil || s.q == nil {
		return fmt.Errorf("release center store is not configured")
	}
	if err := validateApprovalPolicy(policy); err != nil {
		return err
	}
	minimum := policy.MinimumRisk
	if minimum == "" {
		minimum = RiskLow
	}
	_, err := s.q.Exec(ctx, `INSERT INTO release_center_approval_policies
		(tenant_id,policy_id,knowledge_space_id,permission,minimum_risk,required_approvals,
		 approver_group_id,allow_requester_approval,priority,active)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (tenant_id,policy_id) DO UPDATE SET
		 knowledge_space_id=EXCLUDED.knowledge_space_id,permission=EXCLUDED.permission,
		 minimum_risk=EXCLUDED.minimum_risk,required_approvals=EXCLUDED.required_approvals,
		 approver_group_id=EXCLUDED.approver_group_id,
		 allow_requester_approval=EXCLUDED.allow_requester_approval,
		 priority=EXCLUDED.priority,active=EXCLUDED.active,updated_at=now()`,
		strings.TrimSpace(policy.TenantID), strings.TrimSpace(policy.ID), strings.TrimSpace(policy.KnowledgeSpaceID),
		strings.TrimSpace(policy.Permission), minimum, policy.RequiredApprovals, strings.TrimSpace(policy.ApproverGroupID),
		policy.AllowRequesterApproval, policy.Priority, policy.Active)
	if err != nil {
		return fmt.Errorf("save approval policy: %w", err)
	}
	return nil
}

func (s *PostgresStore) ResolveApprovalPolicy(ctx context.Context, tenantID, knowledgeSpaceID, permission string, risk RiskLevel) (ApprovalPolicy, bool, error) {
	if s == nil || s.q == nil {
		return ApprovalPolicy{}, false, fmt.Errorf("release center store is not configured")
	}
	if riskRank(risk) == 0 {
		return ApprovalPolicy{}, false, ErrInvalidApprovalPolicy
	}
	var policy ApprovalPolicy
	err := s.q.QueryRow(ctx, `SELECT policy_id,tenant_id,knowledge_space_id,permission,minimum_risk,
		required_approvals,approver_group_id,allow_requester_approval,priority,active
		FROM release_center_approval_policies
		WHERE tenant_id=$1 AND active
		  AND (knowledge_space_id='' OR knowledge_space_id=$2)
		  AND (permission='' OR lower(permission)=lower($3))
		  AND CASE lower($4) WHEN 'low' THEN 1 WHEN 'medium' THEN 2 WHEN 'high' THEN 3 WHEN 'critical' THEN 4 ELSE 0 END
		      >= CASE minimum_risk WHEN 'low' THEN 1 WHEN 'medium' THEN 2 WHEN 'high' THEN 3 WHEN 'critical' THEN 4 ELSE 99 END
		ORDER BY (knowledge_space_id=$2) DESC, (lower(permission)=lower($3)) DESC,
		         CASE minimum_risk WHEN 'low' THEN 1 WHEN 'medium' THEN 2 WHEN 'high' THEN 3 WHEN 'critical' THEN 4 ELSE 0 END DESC,
		         priority DESC, policy_id LIMIT 1`, tenantID, knowledgeSpaceID, permission, string(risk)).Scan(
		&policy.ID, &policy.TenantID, &policy.KnowledgeSpaceID, &policy.Permission, &policy.MinimumRisk,
		&policy.RequiredApprovals, &policy.ApproverGroupID, &policy.AllowRequesterApproval, &policy.Priority, &policy.Active)
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalPolicy{}, false, nil
	}
	if err != nil {
		return ApprovalPolicy{}, false, fmt.Errorf("resolve approval policy: %w", err)
	}
	return policy, true, nil
}

func (s *PostgresStore) IsApprovalGroupMember(ctx context.Context, tenantID, groupID, userID string) (bool, error) {
	if s == nil || s.q == nil {
		return false, fmt.Errorf("release center store is not configured")
	}
	var member bool
	err := s.q.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM release_center_approval_groups g
		JOIN release_center_approval_group_members m
		  ON m.tenant_id=g.tenant_id AND m.group_id=g.group_id
		WHERE g.tenant_id=$1 AND g.group_id=$2 AND g.active AND m.user_id=$3::uuid AND m.active)`,
		tenantID, groupID, userID).Scan(&member)
	if err != nil {
		return false, fmt.Errorf("check approval group member: %w", err)
	}
	return member, nil
}

func (s *PostgresStore) ListGroups(ctx context.Context, tenantID string) ([]ApprovalGroup, error) {
	if s == nil || s.q == nil {
		return nil, fmt.Errorf("release center store is not configured")
	}
	rows, err := s.q.Query(ctx, `SELECT group_id,tenant_id,name,active
		FROM release_center_approval_groups WHERE tenant_id=$1 ORDER BY group_id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list approval groups: %w", err)
	}
	defer rows.Close()
	groups := make([]ApprovalGroup, 0)
	for rows.Next() {
		var group ApprovalGroup
		if err := rows.Scan(&group.ID, &group.TenantID, &group.Name, &group.Active); err != nil {
			return nil, fmt.Errorf("scan approval group: %w", err)
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

func (s *PostgresStore) ListPolicies(ctx context.Context, tenantID string) ([]ApprovalPolicy, error) {
	if s == nil || s.q == nil {
		return nil, fmt.Errorf("release center store is not configured")
	}
	rows, err := s.q.Query(ctx, `SELECT policy_id,tenant_id,knowledge_space_id,permission,minimum_risk,
		required_approvals,approver_group_id,allow_requester_approval,priority,active
		FROM release_center_approval_policies WHERE tenant_id=$1 ORDER BY priority DESC, policy_id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list approval policies: %w", err)
	}
	defer rows.Close()
	policies := make([]ApprovalPolicy, 0)
	for rows.Next() {
		var policy ApprovalPolicy
		if err := rows.Scan(&policy.ID, &policy.TenantID, &policy.KnowledgeSpaceID, &policy.Permission, &policy.MinimumRisk,
			&policy.RequiredApprovals, &policy.ApproverGroupID, &policy.AllowRequesterApproval, &policy.Priority, &policy.Active); err != nil {
			return nil, fmt.Errorf("scan approval policy: %w", err)
		}
		policies = append(policies, policy)
	}
	return policies, rows.Err()
}
