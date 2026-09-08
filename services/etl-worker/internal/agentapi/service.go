// Package agentapi exposes the stateful Agent Orchestrator through HTTP.
package agentapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ai-etl-pipeline/internal/agent"
	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/publicationworkflow"
	"ai-etl-pipeline/internal/query"
	"ai-etl-pipeline/internal/releasecenter"
	"ai-etl-pipeline/internal/store"
)

const (
	ragQueryToolName      = "rag_query"
	etlTaskStatusToolName = "etl_task_status"
)

// QueryService is the Query module surface consumed by the Agent RAG tool.
type QueryService interface {
	Ask(ctx context.Context, req query.Request, access query.AccessContext) (query.Response, error)
}

// Observer receives low-cardinality Agent lifecycle events for metrics and audit adapters.
type Observer interface {
	RecordAgentRunStarted(autoExecute bool)
	RecordAgentRunFinished(state agent.RunState, errorType string, duration time.Duration)
	RecordAgentToolStep(toolName string, state agent.RunState, duration time.Duration)
	RecordAgentApprovalDecision(decision agent.ApprovalStatus, toolName string)
}

// Service owns the HTTP adapter for Agent runs.
type Service struct {
	orchestrator       *agent.Orchestrator
	reviewOrchestrator *agent.Orchestrator
	reviewWorkflow     PublicationWorkflow
	reviewModel        string
	store              agent.Store
	approvalStore      agent.ApprovalStore
	observer           Observer
	closers            []io.Closer
}

// Dependencies are optional durable adapters used by bounded production workflows.
type Dependencies struct {
	ApprovalStore       agent.ApprovalStore
	PublicationWorkflow PublicationWorkflow
	ReviewDocuments     docstore.Store
	ReviewChunks        interface {
		ListChunksByDoc(context.Context, string, string, []string) ([]store.StoredChunk, error)
	}
	ReviewSpaces reviewSpaceReader
}

type createRunRequest struct {
	Task        string `json:"task"`
	AutoExecute *bool  `json:"auto_execute,omitempty"`
	Workflow    string `json:"workflow,omitempty"`
	DocumentID  string `json:"document_id,omitempty"`
}

type approveRunRequest struct {
	ApprovalID string `json:"approval_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type rejectRunRequest struct {
	ApprovalID string `json:"approval_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type cancelRunRequest struct {
	Reason string `json:"reason,omitempty"`
}

// NewService wires the Agent API with the default store, lock manager, planner, and tools.
func NewService(cfg config.Config, qs QueryService, taskStatusStore model.TaskStatusStore) (*Service, error) {
	return NewServiceWithObserver(cfg, qs, taskStatusStore, nil)
}

// NewServiceWithObserver wires the Agent API and emits lifecycle events to observer when provided.
func NewServiceWithObserver(cfg config.Config, qs QueryService, taskStatusStore model.TaskStatusStore, observer Observer) (*Service, error) {
	return NewServiceWithDependencies(cfg, qs, taskStatusStore, observer, Dependencies{})
}

// NewServiceWithDependencies wires optional durable approval and publication adapters.
func NewServiceWithDependencies(cfg config.Config, qs QueryService, taskStatusStore model.TaskStatusStore, observer Observer, dependencies Dependencies) (*Service, error) {
	if qs == nil {
		return nil, fmt.Errorf("query service is required")
	}
	if taskStatusStore == nil {
		return nil, fmt.Errorf("task status store is required")
	}

	var store agent.Store
	approvalStore := dependencies.ApprovalStore
	var lockManager agent.LockManager
	var closers []io.Closer
	if cfg.IsDev() {
		store = agent.NewMemoryStore()
		if approvalStore == nil {
			approvalStore = agent.NewMemoryApprovalStore()
		}
		lockManager = agent.NewMemoryLockManager()
	} else {
		redisStore, err := agent.NewRedisStore(cfg.RedisStateAddr, cfg.RedisStatePassword, cfg.RedisStateDB, cfg.AgentRunTTL)
		if err != nil {
			return nil, err
		}
		store = redisStore
		closers = append(closers, redisStore)

		if approvalStore == nil {
			redisApprovalStore, err := agent.NewRedisApprovalStore(cfg.RedisStateAddr, cfg.RedisStatePassword, cfg.RedisStateDB, cfg.AgentRunTTL)
			if err != nil {
				_ = redisStore.Close()
				return nil, err
			}
			approvalStore = redisApprovalStore
			closers = append(closers, redisApprovalStore)
		}

		redisLockManager, err := agent.NewRedisLockManager(cfg.RedisStateAddr, cfg.RedisStatePassword, cfg.RedisStateDB, cfg.AgentRunTTL)
		if err != nil {
			closeAll(closers)
			return nil, err
		}
		lockManager = redisLockManager
		closers = append(closers, redisLockManager)
	}

	registry := agent.NewRegistry()
	if err := registerRAGQueryTool(registry, qs); err != nil {
		return nil, err
	}
	if err := registerTaskStatusTool(registry, taskStatusStore); err != nil {
		return nil, err
	}
	if dependencies.PublicationWorkflow != nil {
		if err := registerPublicationWorkflowTools(registry, dependencies.PublicationWorkflow); err != nil {
			closeAll(closers)
			return nil, err
		}
	}
	planner, err := newPlanner(cfg, registry)
	if err != nil {
		closeAll(closers)
		return nil, err
	}
	if dependencies.PublicationWorkflow != nil {
		planner = routingPlanner{fallback: planner, governance: GovernancePlanner{}}
	}
	orchestrator, err := agent.NewOrchestrator(store, lockManager, registry, planner, agent.Options{
		NodeID:          cfg.AgentNodeID,
		MaxSteps:        cfg.AgentMaxSteps,
		LockTTL:         cfg.AgentLockTTL,
		RunTimeout:      cfg.AgentRunTimeout,
		ApprovalTimeout: cfg.AgentApprovalTimeout,
		Authorizer:      agent.StaticAuthorizer{},
	})
	if err != nil {
		closeAll(closers)
		return nil, err
	}
	service := &Service{orchestrator: orchestrator, store: store, approvalStore: approvalStore, observer: observerOrNoop(observer), closers: closers}
	if dependencies.PublicationWorkflow != nil && dependencies.ReviewDocuments != nil && dependencies.ReviewChunks != nil {
		reviewRegistry := agent.NewRegistry()
		if err := registerReviewTools(reviewRegistry, dependencies.PublicationWorkflow, dependencies.ReviewDocuments, dependencies.ReviewChunks, store, dependencies.ReviewSpaces); err != nil {
			closeAll(closers)
			return nil, err
		}
		reviewPlanner, err := newReviewPlanner(cfg, reviewRegistry)
		if err != nil {
			closeAll(closers)
			return nil, err
		}
		reviewOrchestrator, err := agent.NewOrchestrator(store, lockManager, reviewRegistry, reviewPlanner, agent.Options{
			NodeID: cfg.AgentNodeID + "-review", MaxSteps: cfg.AgentMaxSteps, LockTTL: cfg.AgentLockTTL,
			MaxTokenBudget: cfg.AgentReviewMaxTokenBudget,
			RunTimeout:     cfg.AgentRunTimeout, ApprovalTimeout: cfg.AgentApprovalTimeout, Authorizer: agent.StaticAuthorizer{},
		})
		if err != nil {
			closeAll(closers)
			return nil, err
		}
		service.reviewOrchestrator = reviewOrchestrator
		service.reviewWorkflow = dependencies.PublicationWorkflow
		if cfg.AgentPlannerType != config.AgentPlannerRule {
			service.reviewModel = configuredReviewModel()
		}
	}
	return service, nil
}

// ReviewPublicationReport runs the autonomous, read-only review Agent and
// returns its validated business report.
func (s *Service) ReviewPublicationReport(ctx context.Context, actor agent.Actor, documentID string) (releasecenter.AgentReview, error) {
	if s == nil || s.reviewOrchestrator == nil {
		return releasecenter.AgentReview{}, fmt.Errorf("review agent is not configured")
	}
	if s.reviewWorkflow == nil {
		return releasecenter.AgentReview{}, fmt.Errorf("review publication workflow is not configured")
	}
	documentID = strings.TrimSpace(documentID)
	assessment, err := s.reviewWorkflow.Assess(ctx, publicationworkflow.Actor{TenantID: actor.TenantID, UserID: actor.UserID, Role: actor.Role}, documentID)
	if err != nil {
		return releasecenter.AgentReview{}, err
	}
	if !assessment.Ready || assessment.Candidate == nil {
		return releasecenter.AgentReview{}, publicationworkflow.ErrNotReady
	}
	candidate := *assessment.Candidate
	run, err := s.reviewOrchestrator.StartOrResume(ctx, actor, reviewRunID(actor.TenantID, candidate), documentReviewTaskPrefix+documentID, map[string]interface{}{
		"review_candidate": structMap(candidate),
	})
	if err != nil {
		return releasecenter.AgentReview{}, err
	}
	return s.resumePublicationReview(ctx, actor, run.ID)
}

// ResumePublicationReport continues a previously persisted Review Agent run.
// It is used after a worker restart or lock handoff and never creates a new
// run, preserving the existing observations and idempotency keys.
func (s *Service) ResumePublicationReport(ctx context.Context, actor agent.Actor, runID string) (releasecenter.AgentReview, error) {
	if s == nil || s.reviewOrchestrator == nil {
		return releasecenter.AgentReview{}, fmt.Errorf("review agent is not configured")
	}
	return s.resumePublicationReview(ctx, actor, strings.TrimSpace(runID))
}

func (s *Service) resumePublicationReview(ctx context.Context, actor agent.Actor, runID string) (releasecenter.AgentReview, error) {
	if strings.TrimSpace(runID) == "" {
		return releasecenter.AgentReview{}, fmt.Errorf("review run id is required")
	}
	run, err := s.reviewOrchestrator.RunToCompletion(ctx, runID, actor)
	if err != nil {
		return releasecenter.AgentReview{RunID: run.ID, Status: "failed", Recommendation: "manual_review", RiskLevel: releasecenter.RiskHigh, Summary: err.Error()}, nil
	}
	if run.State != agent.StateCompleted {
		return releasecenter.AgentReview{RunID: run.ID, Status: "failed", Recommendation: "manual_review", RiskLevel: releasecenter.RiskHigh, Summary: run.Error}, nil
	}
	var report releasecenter.AgentReview
	if err := json.Unmarshal([]byte(run.Final), &report); err != nil {
		return releasecenter.AgentReview{RunID: run.ID, Status: "failed", Recommendation: "manual_review", RiskLevel: releasecenter.RiskHigh, Summary: "invalid review report: " + err.Error()}, nil
	}
	report, candidate, err := validateAutonomousReview(run, report)
	if err != nil {
		return releasecenter.AgentReview{RunID: run.ID, Status: "failed", Recommendation: "manual_review", RiskLevel: releasecenter.RiskHigh, Summary: err.Error()}, nil
	}
	report.RunID = run.ID
	report.Candidate = candidate
	report.Model = s.reviewModel
	report.PromptVersion = reviewPromptVersion
	return report, nil
}

func newServiceWithComponents(orchestrator *agent.Orchestrator, store agent.Store) *Service {
	return newServiceWithComponentsAndApprovalStore(orchestrator, store, agent.NewMemoryApprovalStore())
}

func newServiceWithComponentsAndApprovalStore(orchestrator *agent.Orchestrator, store agent.Store, approvalStore agent.ApprovalStore) *Service {
	return newServiceWithComponentsAndObserver(orchestrator, store, approvalStore, nil)
}

func newServiceWithComponentsAndObserver(orchestrator *agent.Orchestrator, store agent.Store, approvalStore agent.ApprovalStore, observer Observer) *Service {
	if approvalStore == nil {
		approvalStore = agent.NewMemoryApprovalStore()
	}
	return &Service{orchestrator: orchestrator, store: store, approvalStore: approvalStore, observer: observerOrNoop(observer)}
}

func newPlanner(cfg config.Config, registry *agent.Registry) (agent.Planner, error) {
	switch cfg.ResolvedAgentPlannerType() {
	case config.AgentPlannerRule:
		return RulePlanner{}, nil
	case config.AgentPlannerLLM:
		return NewLLMPlanner(LLMPlannerOptions{
			Endpoint:  cfg.AgentPlannerEndpoint,
			APIKey:    cfg.AgentPlannerAPIKey,
			Model:     cfg.AgentPlannerModel,
			MaxTokens: cfg.AgentPlannerMaxTokens,
			Timeout:   cfg.AgentPlannerTimeout,
			Tools:     registry.Definitions(),
		})
	default:
		return nil, fmt.Errorf("unsupported agent planner type %q", cfg.ResolvedAgentPlannerType())
	}
}

func newReviewPlanner(cfg config.Config, registry *agent.Registry) (agent.Planner, error) {
	if cfg.AgentPlannerType == config.AgentPlannerRule {
		return ReviewRulePlanner{}, nil
	}
	inner, err := NewLLMPlanner(LLMPlannerOptions{
		Endpoint:  config.EnvStr("LLM_ENDPOINT", "https://api.openai.com/v1/chat/completions"),
		APIKey:    config.EnvSecret("LLM_API_KEY", ""),
		Model:     config.EnvStr("LLM_MODEL", "deepseek-v4-flash"),
		MaxTokens: config.EnvInt("AGENT_PLANNER_MAX_TOKENS", 1200),
		Timeout:   config.EnvDuration("AGENT_PLANNER_TIMEOUT", 45*time.Second), Tools: registry.Definitions(),
		SystemPrompt: reviewPlannerSystemPrompt,
	})
	if err != nil {
		return nil, err
	}
	return constrainedReviewPlanner{inner: inner}, nil
}

func configuredReviewModel() string {
	return config.EnvStr("LLM_MODEL", "deepseek-v4-flash")
}

// Close releases resources owned by the service.
func (s *Service) Close() error {
	return closeAll(s.closers)
}

func closeAll(closers []io.Closer) error {
	var firstErr error
	for _, closer := range closers {
		if closer == nil {
			continue
		}
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// HandleRuns handles POST /v1/agent/runs.
func (s *Service) HandleRuns(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/agent/runs" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	actor, err := actorFromRequest(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req createRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Workflow) != "" {
		if req.Workflow != documentPublicationWorkflow || strings.TrimSpace(req.DocumentID) == "" {
			http.Error(w, "invalid workflow request", http.StatusBadRequest)
			return
		}
		req.Task = governanceTaskPrefix + strings.TrimSpace(req.DocumentID)
	}

	run, err := s.orchestrator.Start(r.Context(), actor, req.Task)
	if err != nil {
		writeAgentError(w, err)
		return
	}

	autoExecute := true
	if req.AutoExecute != nil {
		autoExecute = *req.AutoExecute
	}
	s.observer.RecordAgentRunStarted(autoExecute)
	previous := run
	if autoExecute {
		run, err = s.orchestrator.RunToCompletion(r.Context(), run.ID, actor)
		if err != nil {
			writeAgentError(w, err)
			return
		}
		if err := s.ensurePendingApproval(r.Context(), run); err != nil {
			writeAgentError(w, err)
			return
		}
		s.observeRunChange(previous, run)
	}

	writeJSON(w, http.StatusCreated, run)
}

// HandleRun handles GET /v1/agent/runs/{id} and POST sub-actions.
func (s *Service) HandleRun(w http.ResponseWriter, r *http.Request) {
	runID, action, ok := parseRunPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	actor, err := actorFromRequest(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	switch action {
	case "":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		run, err := s.loadTenantRun(r.Context(), runID, actor)
		if err != nil {
			writeAgentError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, run)
	case "resume":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		current, err := s.loadTenantRun(r.Context(), runID, actor)
		if err != nil {
			writeAgentError(w, err)
			return
		}
		if agentTerminalState(current.State) {
			http.Error(w, "terminal run cannot be resumed", http.StatusConflict)
			return
		}
		actor, err = s.actorWithApprovedRunTools(r.Context(), current, actor)
		if err != nil {
			writeAgentError(w, err)
			return
		}
		run, err := s.orchestrator.RunToCompletion(r.Context(), runID, actor)
		if err != nil {
			writeAgentError(w, err)
			return
		}
		if err := s.ensurePendingApproval(r.Context(), run); err != nil {
			writeAgentError(w, err)
			return
		}
		if err := s.rejectPendingApprovalAfterTimeout(r.Context(), current, run); err != nil {
			writeAgentError(w, err)
			return
		}
		s.observeRunChange(current, run)
		writeJSON(w, http.StatusOK, run)
	case "approvals":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleApprovals(w, r, runID, actor)
	case "approve":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleApprove(w, r, runID, actor)
	case "reject":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleReject(w, r, runID, actor)
	case "cancel":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleCancel(w, r, runID, actor)
	default:
		http.NotFound(w, r)
	}
}

func (s *Service) handleApprovals(w http.ResponseWriter, r *http.Request, runID string, actor agent.Actor) {
	if _, err := s.loadTenantRun(r.Context(), runID, actor); err != nil {
		writeAgentError(w, err)
		return
	}
	approvals, err := s.approvalStore.ListRunApprovals(r.Context(), actor.TenantID, runID)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, approvals)
}

func (s *Service) handleApprove(w http.ResponseWriter, r *http.Request, runID string, actor agent.Actor) {
	if !hasPermission(actor, "agent:approve") {
		http.Error(w, "missing approval permission", http.StatusForbidden)
		return
	}
	var req approveRunRequest
	if err := decodeOptionalJSON(r.Body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	previous, err := s.loadTenantRun(r.Context(), runID, actor)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	run, err := s.orchestrator.ApplyLifecycle(r.Context(), runID, actor)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	if run.State != agent.StatePendingApproval {
		if err := s.rejectPendingApprovalAfterTimeout(r.Context(), previous, run); err != nil {
			writeAgentError(w, err)
			return
		}
		s.observeRunChange(previous, run)
		http.Error(w, "run is not pending approval", http.StatusConflict)
		return
	}
	if err := s.ensurePendingApproval(r.Context(), run); err != nil {
		writeAgentError(w, err)
		return
	}
	approval, toolName, err := s.currentApproval(r.Context(), actor.TenantID, run, req.ApprovalID, req.ToolName)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	if approval.RequestedBy == actor.UserID {
		http.Error(w, "approval requires a different administrator", http.StatusForbidden)
		return
	}
	if approval.Status == agent.ApprovalRejected {
		http.Error(w, "approval is already rejected", http.StatusConflict)
		return
	}
	if approval.Status != agent.ApprovalPending && approval.Status != agent.ApprovalApproved {
		http.Error(w, "approval status is invalid", http.StatusConflict)
		return
	}
	if approval.Status == agent.ApprovalPending {
		approval, err = s.approvalStore.DecideApproval(
			r.Context(),
			actor.TenantID,
			approval.ID,
			agent.ApprovalApproved,
			actor.UserID,
			strings.TrimSpace(req.Reason),
			time.Now().UTC(),
		)
		if err != nil {
			writeAgentError(w, err)
			return
		}
		s.observer.RecordAgentApprovalDecision(approval.Status, approval.ToolName)
	}

	actor.ApprovedTools = append(actor.ApprovedTools, toolName)
	previous = run
	run, err = s.orchestrator.RunToCompletion(r.Context(), runID, actor)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	if err := s.ensurePendingApproval(r.Context(), run); err != nil {
		writeAgentError(w, err)
		return
	}
	s.observeRunChange(previous, run)
	writeJSON(w, http.StatusOK, run)
}

func (s *Service) handleReject(w http.ResponseWriter, r *http.Request, runID string, actor agent.Actor) {
	if !hasPermission(actor, "agent:approve") {
		http.Error(w, "missing approval permission", http.StatusForbidden)
		return
	}
	var req rejectRunRequest
	if err := decodeOptionalJSON(r.Body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	previous, err := s.loadTenantRun(r.Context(), runID, actor)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	run, err := s.orchestrator.ApplyLifecycle(r.Context(), runID, actor)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	if run.State != agent.StatePendingApproval {
		if err := s.rejectPendingApprovalAfterTimeout(r.Context(), previous, run); err != nil {
			writeAgentError(w, err)
			return
		}
		s.observeRunChange(previous, run)
		http.Error(w, "run is not pending approval", http.StatusConflict)
		return
	}
	if err := s.ensurePendingApproval(r.Context(), run); err != nil {
		writeAgentError(w, err)
		return
	}
	approval, _, err := s.currentApproval(r.Context(), actor.TenantID, run, req.ApprovalID, req.ToolName)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	if approval.Status != agent.ApprovalPending {
		http.Error(w, "approval is not pending", http.StatusConflict)
		return
	}
	approval, err = s.approvalStore.DecideApproval(
		r.Context(),
		actor.TenantID,
		approval.ID,
		agent.ApprovalRejected,
		actor.UserID,
		strings.TrimSpace(req.Reason),
		time.Now().UTC(),
	)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	s.observer.RecordAgentApprovalDecision(approval.Status, approval.ToolName)

	previous = run
	run, err = s.orchestrator.RejectApproval(r.Context(), runID, actor, req.Reason)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	s.observeRunChange(previous, run)
	writeJSON(w, http.StatusOK, run)
}

func (s *Service) handleCancel(w http.ResponseWriter, r *http.Request, runID string, actor agent.Actor) {
	var req cancelRunRequest
	if err := decodeOptionalJSON(r.Body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	previous, err := s.loadTenantRun(r.Context(), runID, actor)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	current, err := s.orchestrator.ApplyLifecycle(r.Context(), runID, actor)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	if err := s.rejectPendingApprovalAfterTimeout(r.Context(), previous, current); err != nil {
		writeAgentError(w, err)
		return
	}
	s.observeRunChange(previous, current)
	if agentTerminalState(current.State) {
		http.Error(w, "terminal run cannot be cancelled", http.StatusConflict)
		return
	}
	run, err := s.orchestrator.Cancel(r.Context(), runID, actor, req.Reason)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	if err := s.rejectPendingApprovalAfterCancel(r.Context(), current, actor, req.Reason); err != nil {
		writeAgentError(w, err)
		return
	}
	s.observeRunChange(current, run)
	writeJSON(w, http.StatusOK, run)
}

func (s *Service) rejectPendingApprovalAfterTimeout(ctx context.Context, previousRun, currentRun agent.Run) error {
	if previousRun.State != agent.StatePendingApproval {
		return nil
	}
	if currentRun.State != agent.StateFailed || !agentTimeoutError(currentRun.Error) {
		return nil
	}
	if err := s.ensurePendingApproval(ctx, previousRun); err != nil {
		return err
	}
	approval, _, err := s.currentApproval(ctx, previousRun.TenantID, previousRun, "", "")
	if err != nil {
		return err
	}
	if approval.Status != agent.ApprovalPending {
		return nil
	}
	approval, err = s.approvalStore.DecideApproval(ctx, previousRun.TenantID, approval.ID, agent.ApprovalRejected, "system", currentRun.Error, time.Now().UTC())
	if err != nil {
		return err
	}
	s.observer.RecordAgentApprovalDecision(approval.Status, approval.ToolName)
	return nil
}

func (s *Service) rejectPendingApprovalAfterCancel(ctx context.Context, run agent.Run, actor agent.Actor, reason string) error {
	if run.State != agent.StatePendingApproval {
		return nil
	}
	if err := s.ensurePendingApproval(ctx, run); err != nil {
		return err
	}
	approval, _, err := s.currentApproval(ctx, actor.TenantID, run, "", "")
	if err != nil {
		return err
	}
	if approval.Status != agent.ApprovalPending {
		return nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "run cancelled"
	}
	approval, err = s.approvalStore.DecideApproval(ctx, actor.TenantID, approval.ID, agent.ApprovalRejected, actor.UserID, reason, time.Now().UTC())
	if err != nil {
		return err
	}
	s.observer.RecordAgentApprovalDecision(approval.Status, approval.ToolName)
	return nil
}

func (s *Service) ensurePendingApproval(ctx context.Context, run agent.Run) error {
	if run.State != agent.StatePendingApproval {
		return nil
	}
	step, err := pendingToolStep(run)
	if err != nil {
		return err
	}
	approvals, err := s.approvalStore.ListRunApprovals(ctx, run.TenantID, run.ID)
	if err != nil {
		return err
	}
	for _, approval := range approvals {
		if approval.StepIndex == step.Index && strings.EqualFold(approval.ToolName, step.ToolName) {
			return nil
		}
	}
	err = s.approvalStore.CreateApproval(ctx, agent.ApprovalRequest{
		ID:            agent.ApprovalIDForStep(run.ID, step.Index),
		RunID:         run.ID,
		TenantID:      run.TenantID,
		StepIndex:     step.Index,
		ToolName:      step.ToolName,
		ToolArguments: append([]byte(nil), step.ToolArguments...),
		Status:        agent.ApprovalPending,
		RequestedBy:   run.UserID,
		RequestedAt:   time.Now().UTC(),
	})
	if err != nil && strings.Contains(err.Error(), "already exists") {
		return nil
	}
	return err
}

func (s *Service) actorWithApprovedRunTools(ctx context.Context, run agent.Run, actor agent.Actor) (agent.Actor, error) {
	if run.State != agent.StatePendingApproval {
		return actor, nil
	}
	step, err := pendingToolStep(run)
	if err != nil {
		return actor, err
	}
	approvals, err := s.approvalStore.ListRunApprovals(ctx, actor.TenantID, run.ID)
	if err != nil {
		return actor, err
	}
	for _, approval := range approvals {
		if approval.Status == agent.ApprovalApproved &&
			approval.StepIndex == step.Index &&
			strings.EqualFold(approval.ToolName, step.ToolName) &&
			!containsFold(actor.ApprovedTools, step.ToolName) {
			actor.ApprovedTools = append(actor.ApprovedTools, step.ToolName)
		}
	}
	return actor, nil
}

func (s *Service) currentApproval(ctx context.Context, tenantID string, run agent.Run, approvalID, toolName string) (agent.ApprovalRequest, string, error) {
	step, err := pendingToolStep(run)
	if err != nil {
		return agent.ApprovalRequest{}, "", err
	}
	if strings.TrimSpace(toolName) != "" && !strings.EqualFold(toolName, step.ToolName) {
		return agent.ApprovalRequest{}, "", fmt.Errorf("approval tool does not match pending tool")
	}
	if strings.TrimSpace(approvalID) != "" {
		approval, err := s.approvalStore.LoadApproval(ctx, tenantID, strings.TrimSpace(approvalID))
		if err != nil {
			return agent.ApprovalRequest{}, "", err
		}
		if err := validateCurrentApproval(approval, run, step); err != nil {
			return agent.ApprovalRequest{}, "", err
		}
		return approval, step.ToolName, nil
	}

	if approval, err := s.approvalStore.LoadApproval(ctx, tenantID, agent.ApprovalIDForStep(run.ID, step.Index)); err == nil {
		if err := validateCurrentApproval(approval, run, step); err != nil {
			return agent.ApprovalRequest{}, "", err
		}
		return approval, step.ToolName, nil
	} else if !strings.Contains(err.Error(), "not found") {
		return agent.ApprovalRequest{}, "", err
	}

	approvals, err := s.approvalStore.ListRunApprovals(ctx, tenantID, run.ID)
	if err != nil {
		return agent.ApprovalRequest{}, "", err
	}
	for _, approval := range approvals {
		if approval.StepIndex == step.Index && strings.EqualFold(approval.ToolName, step.ToolName) {
			return approval, step.ToolName, nil
		}
	}
	return agent.ApprovalRequest{}, "", fmt.Errorf("approval for pending step not found")
}

func validateCurrentApproval(approval agent.ApprovalRequest, run agent.Run, step agent.Step) error {
	if approval.TenantID != run.TenantID || approval.RunID != run.ID {
		return fmt.Errorf("approval does not belong to run")
	}
	if approval.StepIndex != step.Index || !strings.EqualFold(approval.ToolName, step.ToolName) {
		return fmt.Errorf("approval tool does not match pending tool")
	}
	return nil
}

func (s *Service) loadTenantRun(ctx context.Context, runID string, actor agent.Actor) (agent.Run, error) {
	run, err := s.store.LoadRun(ctx, runID)
	if err != nil {
		return agent.Run{}, err
	}
	if run.TenantID != actor.TenantID {
		return agent.Run{}, fmt.Errorf("run %q not found", runID)
	}
	return run, nil
}

func registerRAGQueryTool(registry *agent.Registry, qs QueryService) error {
	return registry.Register(agent.ToolDefinition{
		Name:                ragQueryToolName,
		Description:         "Ask the tenant-scoped RAG query service and return an answer with sources.",
		RequiredPermissions: []string{"query"},
		Timeout:             45 * time.Second,
		Idempotent:          true,
		Parameters: agent.JSONSchema{
			Type:     "object",
			Required: []string{"question"},
			Properties: map[string]agent.SchemaProperty{
				"question": {Type: "string", Description: "User question to answer from indexed documents."},
				"top_k":    {Type: "integer", Description: "Maximum number of retrieved chunks."},
			},
		},
	}, func(ctx context.Context, inv agent.ToolInvocation) (agent.ToolResult, error) {
		question, _ := inv.Arguments["question"].(string)
		topK := 5
		if raw, ok := inv.Arguments["top_k"].(float64); ok && raw > 0 {
			topK = int(raw)
		}
		resp, err := qs.Ask(ctx, query.Request{Question: question, TopK: topK}, query.AccessContext{
			TenantID: inv.TenantID,
			UserID:   inv.UserID,
			Role:     inv.Role,
		})
		if err != nil {
			return agent.ToolResult{}, err
		}
		return agent.ToolResult{
			Content: resp.Answer,
			Data: map[string]interface{}{
				"answer":   resp.Answer,
				"sources":  resp.Sources,
				"duration": resp.Duration,
			},
		}, nil
	})
}

func registerTaskStatusTool(registry *agent.Registry, statusStore model.TaskStatusStore) error {
	return registry.Register(agent.ToolDefinition{
		Name:                etlTaskStatusToolName,
		Description:         "Read the tenant-scoped ETL document processing task status by task_id.",
		RequiredPermissions: []string{"agent"},
		Timeout:             5 * time.Second,
		Idempotent:          true,
		Parameters: agent.JSONSchema{
			Type:     "object",
			Required: []string{"task_id"},
			Properties: map[string]agent.SchemaProperty{
				"task_id": {Type: "string", Description: "Upload task id, usually the doc_id returned by /v1/upload."},
			},
		},
	}, func(ctx context.Context, inv agent.ToolInvocation) (agent.ToolResult, error) {
		taskID, _ := inv.Arguments["task_id"].(string)
		taskID = strings.TrimSpace(taskID)
		status, found, err := statusStore.Load(ctx, inv.TenantID, taskID)
		if err != nil {
			return agent.ToolResult{}, err
		}
		if !found {
			return agent.ToolResult{
				Content: fmt.Sprintf("未找到任务 %s，或当前租户无权访问该任务。", taskID),
				Data: map[string]interface{}{
					"task_id": taskID,
					"found":   false,
					"status":  "not_found",
				},
			}, nil
		}
		return agent.ToolResult{
			Content: fmt.Sprintf("任务 %s 当前状态为 %s，阶段为 %s。", status.TaskID, status.Status, status.Stage),
			Data: map[string]interface{}{
				"found":  true,
				"status": status,
			},
		}, nil
	})
}

// RulePlanner is the first deterministic planner: one RAG tool call, then final answer.
type RulePlanner struct{}

// Plan returns the next action for a simple knowledge-answering Agent run.
func (RulePlanner) Plan(_ context.Context, run agent.Run) (agent.PlanDecision, error) {
	if len(run.Steps) == 0 {
		args, err := json.Marshal(map[string]interface{}{
			"question": run.Task,
			"top_k":    5,
		})
		if err != nil {
			return agent.PlanDecision{}, err
		}
		return agent.PlanDecision{
			Type:      agent.DecisionToolCall,
			Thought:   "answer the task through tenant-scoped retrieval",
			ToolName:  ragQueryToolName,
			Arguments: args,
		}, nil
	}

	last := run.Steps[len(run.Steps)-1]
	if last.ToolResult == nil {
		return agent.PlanDecision{}, fmt.Errorf("last tool step has no result")
	}
	return agent.PlanDecision{
		Type:    agent.DecisionFinal,
		Thought: "use the retrieved answer as the final response",
		Final:   last.ToolResult.Content,
	}, nil
}

func observerOrNoop(observer Observer) Observer {
	if observer == nil {
		return noopObserver{}
	}
	return observer
}

type noopObserver struct{}

func (noopObserver) RecordAgentRunStarted(bool) {}

func (noopObserver) RecordAgentRunFinished(agent.RunState, string, time.Duration) {}

func (noopObserver) RecordAgentToolStep(string, agent.RunState, time.Duration) {}

func (noopObserver) RecordAgentApprovalDecision(agent.ApprovalStatus, string) {}

func (s *Service) observeRunChange(previous, current agent.Run) {
	previousStepStates := make(map[int]agent.RunState, len(previous.Steps))
	for _, step := range previous.Steps {
		previousStepStates[step.Index] = step.State
	}
	for _, step := range current.Steps {
		if step.Type != agent.StepToolCall || !observableStepState(step.State) {
			continue
		}
		previousState, existed := previousStepStates[step.Index]
		if existed && previousState == step.State {
			continue
		}
		s.observer.RecordAgentToolStep(step.ToolName, step.State, nonNegativeDuration(step.Duration))
	}
	if agentTerminalState(current.State) && !agentTerminalState(previous.State) {
		s.observer.RecordAgentRunFinished(current.State, agentRunErrorType(current), agentRunDuration(current))
	}
}

func observableStepState(state agent.RunState) bool {
	switch state {
	case agent.StatePendingApproval, agent.StateCompleted, agent.StateFailed, agent.StateCancelled:
		return true
	default:
		return false
	}
}

func agentRunDuration(run agent.Run) time.Duration {
	if run.CreatedAt.IsZero() || run.UpdatedAt.IsZero() {
		return 0
	}
	return nonNegativeDuration(run.UpdatedAt.Sub(run.CreatedAt))
}

func agentRunErrorType(run agent.Run) string {
	if run.Error == "" {
		return "none"
	}
	if agentTimeoutError(run.Error) {
		return run.Error
	}
	switch {
	case strings.HasPrefix(run.Error, "run cancelled"):
		return "cancelled"
	case strings.HasPrefix(run.Error, "approval rejected"):
		return "approval_rejected"
	case strings.Contains(run.Error, "max_steps_exceeded"):
		return "max_steps_exceeded"
	default:
		return "error"
	}
}

func nonNegativeDuration(duration time.Duration) time.Duration {
	if duration < 0 {
		return 0
	}
	return duration
}

func actorFromRequest(r *http.Request) (agent.Actor, error) {
	tenantID := auth.GetTenantID(r.Context())
	userID := auth.GetUserID(r.Context())
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(userID) == "" {
		return agent.Actor{}, fmt.Errorf("missing authenticated actor")
	}
	role := auth.GetPermission(r.Context())
	return agent.Actor{
		TenantID:    tenantID,
		UserID:      userID,
		Role:        role,
		Permissions: agentPermissions(role, auth.GetScopes(r.Context())),
	}, nil
}

func agentPermissions(role string, scopes []string) []string {
	seen := make(map[string]struct{}, len(scopes)+4)
	add := func(permission string) {
		permission = strings.TrimSpace(permission)
		if permission == "" {
			return
		}
		seen[permission] = struct{}{}
	}
	for _, scope := range scopes {
		add(scope)
	}
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "admin":
		add("agent")
		add("query")
		add("agent:approve")
	case "user":
		add("agent")
		add("query")
	case "readonly":
		add("agent")
		add("query")
	}
	out := make([]string, 0, len(seen))
	for permission := range seen {
		out = append(out, permission)
	}
	return out
}

func hasPermission(actor agent.Actor, permission string) bool {
	return containsFold(actor.Permissions, permission)
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func agentTerminalState(state agent.RunState) bool {
	switch state {
	case agent.StateCompleted, agent.StateFailed, agent.StateCancelled:
		return true
	default:
		return false
	}
}

func agentTimeoutError(reason string) bool {
	return reason == "approval_timeout_exceeded" || reason == "run_timeout_exceeded"
}

func parseRunPath(path string) (runID string, action string, ok bool) {
	const prefix = "/v1/agent/runs/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) == 1 && strings.TrimSpace(parts[0]) != "" {
		return parts[0], "", true
	}
	if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
		return parts[0], parts[1], true
	}
	return "", "", false
}

func pendingToolStep(run agent.Run) (agent.Step, error) {
	for i := len(run.Steps) - 1; i >= 0; i-- {
		step := run.Steps[i]
		if step.Type == agent.StepToolCall && step.State == agent.StatePendingApproval {
			return step, nil
		}
	}
	return agent.Step{}, fmt.Errorf("pending approval tool step not found")
}

func decodeOptionalJSON(body io.Reader, dst interface{}) error {
	if body == nil {
		return nil
	}
	err := json.NewDecoder(body).Decode(dst)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func writeAgentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, query.ErrQuestionRequired):
		http.Error(w, "question is required", http.StatusBadRequest)
	case errors.Is(err, query.ErrUnauthorized):
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	case strings.Contains(err.Error(), "missing cancel permission"):
		http.Error(w, err.Error(), http.StatusForbidden)
	case strings.Contains(err.Error(), "missing tool permission"):
		http.Error(w, err.Error(), http.StatusForbidden)
	case strings.Contains(err.Error(), "different tenant"):
		http.Error(w, "not found", http.StatusNotFound)
	case strings.Contains(err.Error(), "not found"):
		http.Error(w, "not found", http.StatusNotFound)
	case strings.Contains(err.Error(), "already terminal") ||
		strings.Contains(err.Error(), "cannot be resumed"):
		http.Error(w, err.Error(), http.StatusConflict)
	case strings.Contains(err.Error(), "approval") &&
		(strings.Contains(err.Error(), "pending") ||
			strings.Contains(err.Error(), "already") ||
			strings.Contains(err.Error(), "does not belong") ||
			strings.Contains(err.Error(), "does not match")):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
