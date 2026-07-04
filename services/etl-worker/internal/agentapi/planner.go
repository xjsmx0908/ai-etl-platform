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
	Endpoint  string
	APIKey    string
	Model     string
	MaxTokens int
	Timeout   time.Duration
	Tools     []agent.ToolDefinition
	Client    *http.Client
}

// LLMPlanner asks a chat-completions model for the next structured Agent decision.
type LLMPlanner struct {
	endpoint  string
	apiKey    string
	model     string
	maxTokens int
	client    *http.Client
	tools     map[string]agent.ToolDefinition
	toolList  []plannerTool
}

type plannerTool struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Parameters  agent.JSONSchema `json:"parameters"`
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
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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
		toolList = append(toolList, plannerTool{
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		})
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("agent planner requires at least one registered tool")
	}

	return &LLMPlanner{
		endpoint:  endpoint,
		apiKey:    opts.APIKey,
		model:     model,
		maxTokens: opts.MaxTokens,
		client:    client,
		tools:     tools,
		toolList:  toolList,
	}, nil
}

// Plan returns a validated tool call or final answer from the LLM planner.
func (p *LLMPlanner) Plan(ctx context.Context, run agent.Run) (agent.PlanDecision, error) {
	body, err := json.Marshal(chatCompletionRequest{
		Model:       p.model,
		Temperature: 0,
		MaxTokens:   p.maxTokens,
		ResponseFormat: map[string]string{
			"type": "json_object",
		},
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
	content := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	if content == "" {
		return agent.PlanDecision{}, fmt.Errorf("agent planner returned empty content")
	}
	return p.validateDecision([]byte(content))
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
	tools, _ := json.Marshal(p.toolList)
	return "You are a stateful enterprise Agent planner. Return exactly one JSON object and no markdown. " +
		"Allowed decisions: " +
		`{"type":"tool_call","thought":"why","tool_name":"registered_tool","arguments":{}} ` +
		"or " +
		`{"type":"final","thought":"why","final":"answer"}. ` +
		"Only call registered tools. Tool arguments must match the provided JSON schema. " +
		"If a tool observation already answers the task, return final. " +
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
