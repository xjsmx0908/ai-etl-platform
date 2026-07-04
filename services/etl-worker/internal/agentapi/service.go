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
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/query"
)

const (
	ragQueryToolName      = "rag_query"
	etlTaskStatusToolName = "etl_task_status"
)

// QueryService is the Query module surface consumed by the Agent RAG tool.
type QueryService interface {
	Ask(ctx context.Context, req query.Request, access query.AccessContext) (query.Response, error)
}

// Service owns the HTTP adapter for Agent runs.
type Service struct {
	orchestrator *agent.Orchestrator
	store        agent.Store
	closer       io.Closer
}

type createRunRequest struct {
	Task        string `json:"task"`
	AutoExecute *bool  `json:"auto_execute,omitempty"`
}

type approveRunRequest struct {
	ToolName string `json:"tool_name,omitempty"`
}

// NewService wires the Agent API with the default store, lock manager, planner, and tools.
func NewService(cfg config.Config, qs QueryService, taskStatusStore model.TaskStatusStore) (*Service, error) {
	if qs == nil {
		return nil, fmt.Errorf("query service is required")
	}
	if taskStatusStore == nil {
		return nil, fmt.Errorf("task status store is required")
	}

	var store agent.Store
	var closer io.Closer
	if cfg.IsDev() {
		store = agent.NewMemoryStore()
	} else {
		redisStore, err := agent.NewRedisStore(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB, cfg.AgentRunTTL)
		if err != nil {
			return nil, err
		}
		store = redisStore
		closer = redisStore
	}

	registry := agent.NewRegistry()
	if err := registerRAGQueryTool(registry, qs); err != nil {
		return nil, err
	}
	if err := registerTaskStatusTool(registry, taskStatusStore); err != nil {
		return nil, err
	}
	planner, err := newPlanner(cfg, registry)
	if err != nil {
		return nil, err
	}
	orchestrator, err := agent.NewOrchestrator(store, agent.NewMemoryLockManager(), registry, planner, agent.Options{
		NodeID:     cfg.AgentNodeID,
		MaxSteps:   cfg.AgentMaxSteps,
		LockTTL:    cfg.AgentLockTTL,
		Authorizer: agent.StaticAuthorizer{},
	})
	if err != nil {
		return nil, err
	}
	return &Service{orchestrator: orchestrator, store: store, closer: closer}, nil
}

func newServiceWithComponents(orchestrator *agent.Orchestrator, store agent.Store) *Service {
	return &Service{orchestrator: orchestrator, store: store}
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

// Close releases resources owned by the service.
func (s *Service) Close() error {
	if s.closer == nil {
		return nil
	}
	return s.closer.Close()
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

	run, err := s.orchestrator.Start(r.Context(), actor, req.Task)
	if err != nil {
		writeAgentError(w, err)
		return
	}

	autoExecute := true
	if req.AutoExecute != nil {
		autoExecute = *req.AutoExecute
	}
	if autoExecute {
		run, err = s.orchestrator.RunToCompletion(r.Context(), run.ID, actor)
		if err != nil {
			writeAgentError(w, err)
			return
		}
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
		run, err := s.orchestrator.RunToCompletion(r.Context(), runID, actor)
		if err != nil {
			writeAgentError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, run)
	case "approve":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleApprove(w, r, runID, actor)
	default:
		http.NotFound(w, r)
	}
}

func (s *Service) handleApprove(w http.ResponseWriter, r *http.Request, runID string, actor agent.Actor) {
	var req approveRunRequest
	if err := decodeOptionalJSON(r.Body, &req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	run, err := s.loadTenantRun(r.Context(), runID, actor)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	if run.State != agent.StatePendingApproval {
		http.Error(w, "run is not pending approval", http.StatusConflict)
		return
	}
	toolName, err := pendingToolName(run)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	if strings.TrimSpace(req.ToolName) != "" && !strings.EqualFold(req.ToolName, toolName) {
		http.Error(w, "approval tool does not match pending tool", http.StatusConflict)
		return
	}

	actor.ApprovedTools = append(actor.ApprovedTools, toolName)
	run, err = s.orchestrator.RunToCompletion(r.Context(), runID, actor)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
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

func pendingToolName(run agent.Run) (string, error) {
	for i := len(run.Steps) - 1; i >= 0; i-- {
		step := run.Steps[i]
		if step.Type == agent.StepToolCall && step.State == agent.StatePendingApproval {
			return step.ToolName, nil
		}
	}
	return "", fmt.Errorf("pending approval tool step not found")
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
	case strings.Contains(err.Error(), "missing tool permission"):
		http.Error(w, err.Error(), http.StatusForbidden)
	case strings.Contains(err.Error(), "not found"):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
