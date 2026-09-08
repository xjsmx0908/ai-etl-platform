package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ToolDefinition is the contract exposed to the planner and enforced before execution.
type ToolDefinition struct {
	Name                string
	Description         string
	Parameters          JSONSchema
	RequiredPermissions []string
	Timeout             time.Duration
	Idempotent          bool
	SideEffect          bool
	RequiresApproval    bool
}

// ToolInvocation is passed to a registered tool handler.
type ToolInvocation struct {
	RunID          string
	StepIndex      int
	TenantID       string
	UserID         string
	Role           string
	Permissions    []string
	ToolName       string
	Arguments      map[string]interface{}
	IdempotencyKey string
}

// ToolHandler executes a validated and authorized tool invocation.
type ToolHandler func(context.Context, ToolInvocation) (ToolResult, error)

// CompensationHandler compensates a failed side-effecting tool call.
type CompensationHandler func(context.Context, ToolInvocation, ToolResult) (ToolResult, error)

type registeredTool struct {
	def         ToolDefinition
	handler     ToolHandler
	compensator CompensationHandler
}

// Registry stores enterprise tool contracts and handlers.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]registeredTool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]registeredTool)}
}

// Register adds a tool definition and handler to the registry.
func (r *Registry) Register(def ToolDefinition, handler ToolHandler) error {
	return r.register(def, handler, nil)
}

// RegisterWithCompensation adds a tool with a compensation handler for partial side effects.
func (r *Registry) RegisterWithCompensation(def ToolDefinition, handler ToolHandler, compensator CompensationHandler) error {
	return r.register(def, handler, compensator)
}

func (r *Registry) register(def ToolDefinition, handler ToolHandler, compensator CompensationHandler) error {
	name := normalizeToolName(def.Name)
	if name == "" {
		return fmt.Errorf("tool name is required")
	}
	if handler == nil {
		return fmt.Errorf("tool %q handler is required", name)
	}
	def.Name = name
	if def.Parameters.Type == "" {
		def.Parameters.Type = "object"
	}
	if def.SideEffect && !def.Idempotent && compensator == nil {
		return fmt.Errorf("side-effecting tool %q must be idempotent or define compensation", name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[name]; exists {
		return fmt.Errorf("tool %q already registered", name)
	}
	r.tools[name] = registeredTool{def: def, handler: handler, compensator: compensator}
	return nil
}

// Definition returns a registered tool definition.
func (r *Registry) Definition(name string) (ToolDefinition, bool) {
	tool, ok := r.lookup(name)
	if !ok {
		return ToolDefinition{}, false
	}
	return tool.def, true
}

// HasCompensation reports whether a registered tool can undo a completed side effect.
func (r *Registry) HasCompensation(name string) bool {
	tool, ok := r.lookup(name)
	return ok && tool.compensator != nil
}

// Definitions returns registered tool definitions sorted by name.
func (r *Registry) Definitions() []ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	definitions := make([]ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		definitions = append(definitions, tool.def)
	}
	sort.Slice(definitions, func(i, j int) bool {
		return definitions[i].Name < definitions[j].Name
	})
	return definitions
}

func (r *Registry) execute(ctx context.Context, actor Actor, runID string, stepIndex int, toolName string, rawArgs []byte, authorizer Authorizer) (ToolResult, error) {
	tool, args, err := r.prepare(ctx, actor, toolName, rawArgs, authorizer)
	if err != nil {
		return ToolResult{}, err
	}
	if tool.def.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, tool.def.Timeout)
		defer cancel()
	}

	return tool.handler(ctx, ToolInvocation{
		RunID:          runID,
		StepIndex:      stepIndex,
		TenantID:       actor.TenantID,
		UserID:         actor.UserID,
		Role:           actor.Role,
		Permissions:    append([]string(nil), actor.Permissions...),
		ToolName:       tool.def.Name,
		Arguments:      args,
		IdempotencyKey: idempotencyKey(runID, stepIndex, tool.def.Name),
	})
}

func (r *Registry) compensate(ctx context.Context, actor Actor, runID string, stepIndex int, toolName string, rawArgs []byte, result ToolResult, authorizer Authorizer) (ToolResult, error) {
	tool, args, err := r.prepare(ctx, actor, toolName, rawArgs, authorizer)
	if err != nil {
		return ToolResult{}, err
	}
	if tool.compensator == nil {
		return ToolResult{}, fmt.Errorf("tool %q has no compensation handler", tool.def.Name)
	}
	return tool.compensator(ctx, ToolInvocation{
		RunID:          runID,
		StepIndex:      stepIndex,
		TenantID:       actor.TenantID,
		UserID:         actor.UserID,
		Role:           actor.Role,
		Permissions:    append([]string(nil), actor.Permissions...),
		ToolName:       tool.def.Name,
		Arguments:      args,
		IdempotencyKey: idempotencyKey(runID, stepIndex, tool.def.Name),
	}, result)
}

func (r *Registry) prepare(ctx context.Context, actor Actor, toolName string, rawArgs []byte, authorizer Authorizer) (registeredTool, map[string]interface{}, error) {
	tool, ok := r.lookup(toolName)
	if !ok {
		return registeredTool{}, nil, fmt.Errorf("unknown tool %q", toolName)
	}
	args, err := ValidateArguments(tool.def.Parameters, rawArgs)
	if err != nil {
		return registeredTool{}, nil, err
	}
	if authorizer != nil {
		if err := authorizer.Authorize(ctx, actor, tool.def); err != nil {
			return registeredTool{}, nil, err
		}
	}
	return tool, args, nil
}

func (r *Registry) lookup(name string) (registeredTool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[normalizeToolName(name)]
	return tool, ok
}

func normalizeToolName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func idempotencyKey(runID string, stepIndex int, toolName string) string {
	return fmt.Sprintf("agent:%s:%d:%s", runID, stepIndex, normalizeToolName(toolName))
}
