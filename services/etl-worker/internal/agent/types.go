// Package agent implements a durable, tool-driven state machine for enterprise Agent runs.
package agent

import (
	"encoding/json"
	"time"
)

// RunState describes the durable lifecycle state of an Agent run.
type RunState string

const (
	StateCreated         RunState = "created"
	StateRunning         RunState = "running"
	StateWaitingTool     RunState = "waiting_tool"
	StatePendingApproval RunState = "pending_approval"
	StateCompleted       RunState = "completed"
	StateFailed          RunState = "failed"
	StateCancelled       RunState = "cancelled"
)

// StepType describes the kind of persisted state-machine step.
type StepType string

const (
	StepToolCall StepType = "tool_call"
	StepFinal    StepType = "final"
	StepError    StepType = "error"
)

// Actor is the authenticated principal executing an Agent run.
type Actor struct {
	TenantID      string
	UserID        string
	Role          string
	Permissions   []string
	ApprovedTools []string
}

// Run is the durable execution record for a long-running Agent task.
type Run struct {
	ID           string                 `json:"id"`
	TenantID     string                 `json:"tenant_id"`
	UserID       string                 `json:"user_id"`
	Version      int64                  `json:"version"`
	FencingToken int64                  `json:"fencing_token"`
	Task         string                 `json:"task"`
	State        RunState               `json:"state"`
	MaxSteps     int                    `json:"max_steps"`
	Steps        []Step                 `json:"steps"`
	Memory       map[string]interface{} `json:"memory,omitempty"`
	Final        string                 `json:"final,omitempty"`
	Error        string                 `json:"error,omitempty"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
}

// Step is an append-only record of a state-machine transition.
type Step struct {
	Index              int             `json:"index"`
	Type               StepType        `json:"type"`
	State              RunState        `json:"state"`
	Thought            string          `json:"thought,omitempty"`
	ToolName           string          `json:"tool_name,omitempty"`
	ToolArguments      json.RawMessage `json:"tool_arguments,omitempty"`
	ToolResult         *ToolResult     `json:"tool_result,omitempty"`
	CompensationResult *ToolResult     `json:"compensation_result,omitempty"`
	Observation        string          `json:"observation,omitempty"`
	IdempotencyKey     string          `json:"idempotency_key,omitempty"`
	Compensated        bool            `json:"compensated,omitempty"`
	StartedAt          time.Time       `json:"started_at"`
	CompletedAt        time.Time       `json:"completed_at,omitempty"`
	Duration           time.Duration   `json:"duration,omitempty"`
	Error              string          `json:"error,omitempty"`
	CompensationError  string          `json:"compensation_error,omitempty"`
}

// ToolResult is the structured output returned by a registered tool.
type ToolResult struct {
	Content              string                 `json:"content"`
	Data                 map[string]interface{} `json:"data,omitempty"`
	Retryable            bool                   `json:"retryable,omitempty"`
	CompensationRequired bool                   `json:"compensation_required,omitempty"`
}

// DecisionType describes what the planner wants the state machine to do next.
type DecisionType string

const (
	DecisionToolCall DecisionType = "tool_call"
	DecisionFinal    DecisionType = "final"
)

// PlanDecision is the planner output consumed by the Orchestrator.
type PlanDecision struct {
	Type      DecisionType    `json:"type"`
	Thought   string          `json:"thought,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Final     string          `json:"final,omitempty"`
}

func terminalState(state RunState) bool {
	switch state {
	case StateCompleted, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}
