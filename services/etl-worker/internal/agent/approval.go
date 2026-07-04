package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalRejected ApprovalStatus = "rejected"
)

type ApprovalRequest struct {
	ID            string          `json:"id"`
	RunID         string          `json:"run_id"`
	TenantID      string          `json:"tenant_id"`
	StepIndex     int             `json:"step_index"`
	ToolName      string          `json:"tool_name"`
	ToolArguments json.RawMessage `json:"tool_arguments,omitempty"`
	Status        ApprovalStatus  `json:"status"`
	RequestedBy   string          `json:"requested_by"`
	RequestedAt   time.Time       `json:"requested_at"`
	DecidedBy     string          `json:"decided_by,omitempty"`
	DecidedAt     time.Time       `json:"decided_at,omitempty"`
	Reason        string          `json:"reason,omitempty"`
}

type ApprovalStore interface {
	CreateApproval(ctx context.Context, approval ApprovalRequest) error
	LoadApproval(ctx context.Context, tenantID, approvalID string) (ApprovalRequest, error)
	ListRunApprovals(ctx context.Context, tenantID, runID string) ([]ApprovalRequest, error)
	DecideApproval(ctx context.Context, tenantID, approvalID string, status ApprovalStatus, decidedBy, reason string, decidedAt time.Time) (ApprovalRequest, error)
}

type MemoryApprovalStore struct {
	mu          sync.RWMutex
	approvals   map[string]ApprovalRequest
	approvalsBy map[string][]string
}

func NewMemoryApprovalStore() *MemoryApprovalStore {
	return &MemoryApprovalStore{
		approvals:   make(map[string]ApprovalRequest),
		approvalsBy: make(map[string][]string),
	}
}

func (s *MemoryApprovalStore) CreateApproval(_ context.Context, approval ApprovalRequest) error {
	if approval.Status == "" {
		approval.Status = ApprovalPending
	}
	if err := validateApprovalForCreate(approval); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.approvals[approval.ID]; exists {
		return fmt.Errorf("approval %q already exists", approval.ID)
	}
	s.approvals[approval.ID] = cloneApproval(approval)
	indexKey := approvalIndexKey(approval.TenantID, approval.RunID)
	s.approvalsBy[indexKey] = append(s.approvalsBy[indexKey], approval.ID)
	return nil
}

func (s *MemoryApprovalStore) LoadApproval(_ context.Context, tenantID, approvalID string) (ApprovalRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	approval, ok := s.approvals[approvalID]
	if !ok || approval.TenantID != tenantID {
		return ApprovalRequest{}, fmt.Errorf("approval %q not found", approvalID)
	}
	return cloneApproval(approval), nil
}

func (s *MemoryApprovalStore) ListRunApprovals(_ context.Context, tenantID, runID string) ([]ApprovalRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := s.approvalsBy[approvalIndexKey(tenantID, runID)]
	out := make([]ApprovalRequest, 0, len(ids))
	for _, id := range ids {
		approval, ok := s.approvals[id]
		if ok && approval.TenantID == tenantID && approval.RunID == runID {
			out = append(out, cloneApproval(approval))
		}
	}
	sortApprovals(out)
	return out, nil
}

func (s *MemoryApprovalStore) DecideApproval(_ context.Context, tenantID, approvalID string, status ApprovalStatus, decidedBy, reason string, decidedAt time.Time) (ApprovalRequest, error) {
	if status != ApprovalApproved && status != ApprovalRejected {
		return ApprovalRequest{}, fmt.Errorf("unsupported approval decision %q", status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	approval, ok := s.approvals[approvalID]
	if !ok || approval.TenantID != tenantID {
		return ApprovalRequest{}, fmt.Errorf("approval %q not found", approvalID)
	}
	if approval.Status != ApprovalPending {
		return ApprovalRequest{}, fmt.Errorf("approval %q is already %s", approvalID, approval.Status)
	}
	approval.Status = status
	approval.DecidedBy = decidedBy
	approval.DecidedAt = decidedAt.UTC()
	approval.Reason = reason
	s.approvals[approvalID] = cloneApproval(approval)
	return cloneApproval(approval), nil
}

type RedisApprovalStore struct {
	client *redis.Client
	prefix string
	ttl    time.Duration
}

func NewRedisApprovalStore(addr, password string, db int, ttl time.Duration) (*RedisApprovalStore, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		PoolSize:     20,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis agent approval store connect failed: %w", err)
	}
	return &RedisApprovalStore{client: client, prefix: "agent:approval", ttl: ttl}, nil
}

func (s *RedisApprovalStore) CreateApproval(ctx context.Context, approval ApprovalRequest) error {
	if approval.Status == "" {
		approval.Status = ApprovalPending
	}
	if err := validateApprovalForCreate(approval); err != nil {
		return err
	}
	data, err := json.Marshal(approval)
	if err != nil {
		return fmt.Errorf("marshal approval: %w", err)
	}
	key := s.key(approval.ID)
	indexKey := s.runIndexKey(approval.TenantID, approval.RunID)
	ok, err := s.client.SetNX(ctx, key, data, s.ttl).Result()
	if err != nil {
		return fmt.Errorf("redis create approval: %w", err)
	}
	if !ok {
		return fmt.Errorf("approval %q already exists", approval.ID)
	}
	pipe := s.client.Pipeline()
	pipe.SAdd(ctx, indexKey, approval.ID)
	pipe.Expire(ctx, indexKey, s.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis index approval: %w", err)
	}
	return nil
}

func (s *RedisApprovalStore) LoadApproval(ctx context.Context, tenantID, approvalID string) (ApprovalRequest, error) {
	approval, err := s.load(ctx, approvalID)
	if err != nil {
		return ApprovalRequest{}, err
	}
	if approval.TenantID != tenantID {
		return ApprovalRequest{}, fmt.Errorf("approval %q not found", approvalID)
	}
	return approval, nil
}

func (s *RedisApprovalStore) ListRunApprovals(ctx context.Context, tenantID, runID string) ([]ApprovalRequest, error) {
	ids, err := s.client.SMembers(ctx, s.runIndexKey(tenantID, runID)).Result()
	if err != nil {
		return nil, fmt.Errorf("redis list approvals: %w", err)
	}
	out := make([]ApprovalRequest, 0, len(ids))
	for _, id := range ids {
		approval, err := s.load(ctx, id)
		if err != nil {
			continue
		}
		if approval.TenantID == tenantID && approval.RunID == runID {
			out = append(out, approval)
		}
	}
	sortApprovals(out)
	return out, nil
}

func (s *RedisApprovalStore) DecideApproval(ctx context.Context, tenantID, approvalID string, status ApprovalStatus, decidedBy, reason string, decidedAt time.Time) (ApprovalRequest, error) {
	if status != ApprovalApproved && status != ApprovalRejected {
		return ApprovalRequest{}, fmt.Errorf("unsupported approval decision %q", status)
	}
	var saved ApprovalRequest
	key := s.key(approvalID)
	err := s.client.Watch(ctx, func(tx *redis.Tx) error {
		data, err := tx.Get(ctx, key).Bytes()
		if err != nil {
			if err == redis.Nil {
				return fmt.Errorf("approval %q not found", approvalID)
			}
			return fmt.Errorf("redis load approval: %w", err)
		}
		var approval ApprovalRequest
		if err := json.Unmarshal(data, &approval); err != nil {
			return fmt.Errorf("decode approval: %w", err)
		}
		if approval.TenantID != tenantID {
			return fmt.Errorf("approval %q not found", approvalID)
		}
		if approval.Status != ApprovalPending {
			return fmt.Errorf("approval %q is already %s", approvalID, approval.Status)
		}
		approval.Status = status
		approval.DecidedBy = decidedBy
		approval.DecidedAt = decidedAt.UTC()
		approval.Reason = reason
		next, err := json.Marshal(approval)
		if err != nil {
			return fmt.Errorf("marshal approval: %w", err)
		}
		_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, key, next, s.ttl)
			return nil
		})
		if err != nil {
			return err
		}
		saved = cloneApproval(approval)
		return nil
	}, key)
	if err != nil {
		if err == redis.TxFailedErr {
			return ApprovalRequest{}, fmt.Errorf("approval %q version conflict", approvalID)
		}
		return ApprovalRequest{}, err
	}
	return saved, nil
}

func (s *RedisApprovalStore) Close() error {
	return s.client.Close()
}

func (s *RedisApprovalStore) load(ctx context.Context, approvalID string) (ApprovalRequest, error) {
	data, err := s.client.Get(ctx, s.key(approvalID)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return ApprovalRequest{}, fmt.Errorf("approval %q not found", approvalID)
		}
		return ApprovalRequest{}, fmt.Errorf("redis load approval: %w", err)
	}
	var approval ApprovalRequest
	if err := json.Unmarshal(data, &approval); err != nil {
		return ApprovalRequest{}, fmt.Errorf("decode approval: %w", err)
	}
	return cloneApproval(approval), nil
}

func (s *RedisApprovalStore) key(approvalID string) string {
	return s.prefix + ":" + approvalID
}

func (s *RedisApprovalStore) runIndexKey(tenantID, runID string) string {
	return s.prefix + ":run:" + approvalIndexKey(tenantID, runID)
}

func validateApprovalForCreate(approval ApprovalRequest) error {
	if approval.ID == "" {
		return fmt.Errorf("approval id is required")
	}
	if approval.RunID == "" {
		return fmt.Errorf("approval run id is required")
	}
	if approval.TenantID == "" {
		return fmt.Errorf("approval tenant id is required")
	}
	if approval.StepIndex <= 0 {
		return fmt.Errorf("approval step index is required")
	}
	if approval.ToolName == "" {
		return fmt.Errorf("approval tool name is required")
	}
	if approval.Status == "" {
		approval.Status = ApprovalPending
	}
	if approval.Status != ApprovalPending {
		return fmt.Errorf("new approval must be pending")
	}
	return nil
}

func NewApprovalID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("approval-%d", time.Now().UnixNano())
	}
	return "approval-" + hex.EncodeToString(b[:])
}

func ApprovalIDForStep(runID string, stepIndex int) string {
	return fmt.Sprintf("approval-%s-%d", runID, stepIndex)
}

func approvalIndexKey(tenantID, runID string) string {
	return tenantID + ":" + runID
}

func sortApprovals(approvals []ApprovalRequest) {
	sort.Slice(approvals, func(i, j int) bool {
		if approvals[i].StepIndex != approvals[j].StepIndex {
			return approvals[i].StepIndex < approvals[j].StepIndex
		}
		if !approvals[i].RequestedAt.Equal(approvals[j].RequestedAt) {
			return approvals[i].RequestedAt.Before(approvals[j].RequestedAt)
		}
		return approvals[i].ID < approvals[j].ID
	})
}

func cloneApproval(approval ApprovalRequest) ApprovalRequest {
	out := approval
	if approval.ToolArguments != nil {
		out.ToolArguments = append([]byte(nil), approval.ToolArguments...)
	}
	return out
}
