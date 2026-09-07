package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-etl-pipeline/internal/agent"
)

// LLMPlannerOptions configures an OpenAI-compatible planner client.
type LLMPlannerOptions struct {
	Endpoint     string
	APIKey       string
	Model        string
	MaxTokens    int
	Timeout      time.Duration
	Tools        []agent.ToolDefinition
	Client       *http.Client
	SystemPrompt string
}

// LLMPlanner asks a chat-completions model for the next structured Agent decision.
type LLMPlanner struct {
	endpoint             string
	apiKey               string
	model                string
	maxTokens            int
	client               *http.Client
	tools                map[string]agent.ToolDefinition
	toolList             []plannerTool
	systemPromptOverride string
}

// plannerTool is the OpenAI function-calling shape the provider expects:
// {"type":"function","function":{"name":...,"description":...,"parameters":...}}.
type plannerTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string           `json:"name"`
		Description string           `json:"description"`
		Parameters  agent.JSONSchema `json:"parameters"`
	} `json:"function"`
}

type plannerDecisionPayload struct {
	Type      agent.DecisionType `json:"type"`
	Thought   string             `json:"thought,omitempty"`
	ToolName  string             `json:"tool_name,omitempty"`
	Arguments json.RawMessage    `json:"arguments,omitempty"`
	Final     string             `json:"final,omitempty"`
}

type chatCompletionRequest struct {
	Model          string            `json:"model"`
	Messages       []chatMessage     `json:"messages"`
	Temperature    float64           `json:"temperature"`
	MaxTokens      int               `json:"max_tokens,omitempty"`
	ResponseFormat map[string]string `json:"response_format,omitempty"`
	// Tools enables native function calling. Models that do not support it
	// ignore the field and answer in the JSON text format instead.
	Tools []plannerTool `json:"tools,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls is populated in assistant messages when native function calling
	// is used; the planner translates them into validated tool decisions.
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

type plannerRunSnapshot struct {
	Task  string                `json:"task"`
	State agent.RunState        `json:"state"`
	Steps []plannerStepSnapshot `json:"steps"`
}

type plannerStepSnapshot struct {
	Index         int             `json:"index"`
	Type          agent.StepType  `json:"type"`
	State         agent.RunState  `json:"state"`
	ToolName      string          `json:"tool_name,omitempty"`
	ToolArguments json.RawMessage `json:"tool_arguments,omitempty"`
	Observation   string          `json:"observation,omitempty"`
	Error         string          `json:"error,omitempty"`
}

// NewLLMPlanner creates a planner that accepts only decisions backed by registered tool definitions.
func NewLLMPlanner(opts LLMPlannerOptions) (*LLMPlanner, error) {
	endpoint := normalizeChatCompletionsEndpoint(opts.Endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("agent planner endpoint is required")
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		return nil, fmt.Errorf("agent planner model is required")
	}
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = 512
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: opts.Timeout}
	}

	tools := make(map[string]agent.ToolDefinition, len(opts.Tools))
	toolList := make([]plannerTool, 0, len(opts.Tools))
	for _, tool := range opts.Tools {
		name := strings.ToLower(strings.TrimSpace(tool.Name))
		if name == "" {
			continue
		}
		tool.Name = name
		tools[name] = tool
		var pt plannerTool
		pt.Type = "function"
		pt.Function.Name = tool.Name
		pt.Function.Description = tool.Description
		pt.Function.Parameters = tool.Parameters
		toolList = append(toolList, pt)
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("agent planner requires at least one registered tool")
	}

	return &LLMPlanner{
		endpoint:             endpoint,
		apiKey:               opts.APIKey,
		model:                model,
		maxTokens:            opts.MaxTokens,
		client:               client,
		tools:                tools,
		toolList:             toolList,
		systemPromptOverride: strings.TrimSpace(opts.SystemPrompt),
	}, nil
}

// Plan returns a validated tool call or final answer from the LLM planner.
// Plan returns a validated tool call or final answer, retrying once when the
// model returns empty or truncated JSON (a transient LLM glitch).
func (p *LLMPlanner) Plan(ctx context.Context, run agent.Run) (agent.PlanDecision, error) {
	decision, err := p.planOnce(ctx, run)
	if err != nil && retryablePlannerError(err) {
		return p.planOnce(ctx, run)
	}
	return decision, err
}

// retryablePlannerError reports whether a planner error is a transient LLM
// output problem worth one retry, as opposed to a real validation failure.
func retryablePlannerError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "empty content") || strings.Contains(msg, "invalid JSON decision")
}

func (p *LLMPlanner) planOnce(ctx context.Context, run agent.Run) (agent.PlanDecision, error) {
	body, err := json.Marshal(chatCompletionRequest{
		Model:       p.model,
		Temperature: 0,
		MaxTokens:   p.maxTokens,
		ResponseFormat: map[string]string{
			"type": "json_object",
		},
		Tools: p.toolList,
		Messages: []chatMessage{
			{Role: "system", Content: p.systemPrompt()},
			{Role: "user", Content: p.userPrompt(run)},
		},
	})
	if err != nil {
		return agent.PlanDecision{}, fmt.Errorf("marshal planner request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return agent.PlanDecision{}, fmt.Errorf("create planner request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return agent.PlanDecision{}, fmt.Errorf("agent planner request failed: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return agent.PlanDecision{}, fmt.Errorf("read planner response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return agent.PlanDecision{}, fmt.Errorf("agent planner returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatCompletionResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return agent.PlanDecision{}, fmt.Errorf("decode planner response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return agent.PlanDecision{}, fmt.Errorf("agent planner returned no choices")
	}
	message := chatResp.Choices[0].Message

	// Native function calling takes precedence: the model selected a tool and
	// supplied structured arguments directly. Falls back to the JSON-text
	// protocol when the model ignored the tools field (content only).
	if len(message.ToolCalls) > 0 {
		return p.decideFromToolCalls(message.ToolCalls)
	}

	content := strings.TrimSpace(message.Content)
	if content == "" {
		return agent.PlanDecision{}, fmt.Errorf("agent planner returned empty content")
	}
	return p.validateDecision([]byte(content))
}

// decideFromToolCalls converts native tool_calls into a validated decision.
// Multiple tool calls collapse to the first valid one — this planner schedules
// one tool per step.
func (p *LLMPlanner) decideFromToolCalls(calls []toolCall) (agent.PlanDecision, error) {
	for _, call := range calls {
		toolName := strings.ToLower(strings.TrimSpace(call.Function.Name))
		if toolName == "" {
			continue
		}
		_, ok := p.tools[toolName]
		if !ok {
			return agent.PlanDecision{}, fmt.Errorf("agent planner selected unregistered tool %q", call.Function.Name)
		}
		args := json.RawMessage(strings.TrimSpace(call.Function.Arguments))
		if len(args) == 0 || string(args) == "" {
			args = json.RawMessage(`{}`)
		}
		if _, err := agent.ValidateArguments(p.tools[toolName].Parameters, args); err != nil {
			return agent.PlanDecision{}, fmt.Errorf("agent planner arguments for tool %q are invalid: %w", toolName, err)
		}
		return agent.PlanDecision{
			Type:      agent.DecisionToolCall,
			ToolName:  toolName,
			Arguments: append(json.RawMessage(nil), args...),
		}, nil
	}
	return agent.PlanDecision{}, fmt.Errorf("agent planner returned tool_calls with no valid tool name")
}

func (p *LLMPlanner) validateDecision(raw []byte) (agent.PlanDecision, error) {
	var payload plannerDecisionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return agent.PlanDecision{}, fmt.Errorf("agent planner returned invalid JSON decision: %w", err)
	}

	switch payload.Type {
	case agent.DecisionFinal:
		if strings.TrimSpace(payload.Final) == "" {
			return agent.PlanDecision{}, fmt.Errorf("agent planner final decision requires final")
		}
		return agent.PlanDecision{
			Type:    agent.DecisionFinal,
			Thought: payload.Thought,
			Final:   payload.Final,
		}, nil
	case agent.DecisionToolCall:
		toolName := strings.ToLower(strings.TrimSpace(payload.ToolName))
		tool, ok := p.tools[toolName]
		if !ok {
			return agent.PlanDecision{}, fmt.Errorf("agent planner selected unregistered tool %q", payload.ToolName)
		}
		args := payload.Arguments
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		if _, err := agent.ValidateArguments(tool.Parameters, args); err != nil {
			return agent.PlanDecision{}, fmt.Errorf("agent planner arguments for tool %q are invalid: %w", toolName, err)
		}
		return agent.PlanDecision{
			Type:      agent.DecisionToolCall,
			Thought:   payload.Thought,
			ToolName:  toolName,
			Arguments: append(json.RawMessage(nil), args...),
		}, nil
	default:
		return agent.PlanDecision{}, fmt.Errorf("agent planner returned unsupported decision type %q", payload.Type)
	}
}

func (p *LLMPlanner) systemPrompt() string {
	if p.systemPromptOverride != "" {
		tools, _ := json.Marshal(p.toolList)
		return p.systemPromptOverride + " Registered tools JSON: " + string(tools)
	}
	tools, _ := json.Marshal(p.toolList)
	return "You are a stateful enterprise Agent planner. Return exactly one JSON object and no markdown. " +
		"Allowed decisions: " +
		`{"type":"tool_call","thought":"why","tool_name":"registered_tool","arguments":{}} ` +
		"or " +
		`{"type":"final","thought":"why","final":"answer"}. ` +
		"Only call registered tools. Tool arguments must match the provided JSON schema. " +
		"If a tool observation already answers the task, return final. " +
		"If the latest tool result contains no useful answer (e.g. \"未找到相关文档\" or an error), " +
		"return final stating the knowledge base could not answer — do not repeat the same or " +
		"a similar tool call more than once. " +
		"Registered tools JSON: " + string(tools)
}

func (p *LLMPlanner) userPrompt(run agent.Run) string {
	snapshot := plannerRunSnapshot{
		Task:  run.Task,
		State: run.State,
		Steps: make([]plannerStepSnapshot, 0, len(run.Steps)),
	}
	for _, step := range run.Steps {
		snapshot.Steps = append(snapshot.Steps, plannerStepSnapshot{
			Index:         step.Index,
			Type:          step.Type,
			State:         step.State,
			ToolName:      step.ToolName,
			ToolArguments: step.ToolArguments,
			Observation:   step.Observation,
			Error:         step.Error,
		})
	}
	data, _ := json.Marshal(snapshot)
	return "Plan the next action for this durable run JSON: " + string(data)
}

func normalizeChatCompletionsEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}

	path := strings.TrimSuffix(u.Path, "/")
	switch path {
	case "":
		u.Path = "/v1/chat/completions"
	case "/v1":
		u.Path = "/v1/chat/completions"
	default:
		if !strings.Contains(path, "/chat/completions") {
			u.Path = path + "/chat/completions"
		}
	}
	return u.String()
}
