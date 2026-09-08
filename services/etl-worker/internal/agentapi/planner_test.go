package agentapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-etl-pipeline/internal/agent"
	"ai-etl-pipeline/internal/config"
)

func TestLLMPlannerPlansToolCall(t *testing.T) {
	var gotAuth string
	var gotResponseFormat map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode planner request: %v", err)
		}
		gotResponseFormat = req.ResponseFormat
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"type\":\"tool_call\",\"thought\":\"need retrieval\",\"tool_name\":\"rag_query\",\"arguments\":{\"question\":\"报销制度是什么\",\"top_k\":3}}"}}],"usage":{"prompt_tokens":13,"completion_tokens":5}}`))
	}))
	defer server.Close()

	planner := newTestLLMPlanner(t, server.URL, "planner-key")
	decision, err := planner.Plan(context.Background(), agent.Run{
		Task:  "报销制度是什么",
		State: agent.StateRunning,
	})
	if err != nil {
		t.Fatalf("plan tool call: %v", err)
	}
	if decision.Type != agent.DecisionToolCall || decision.ToolName != ragQueryToolName {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	var args map[string]interface{}
	if err := json.Unmarshal(decision.Arguments, &args); err != nil {
		t.Fatalf("decode decision args: %v", err)
	}
	if args["question"] != "报销制度是什么" || args["top_k"].(float64) != 3 {
		t.Fatalf("unexpected args: %+v", args)
	}
	if gotAuth != "Bearer planner-key" {
		t.Fatalf("expected bearer auth, got %q", gotAuth)
	}
	if gotResponseFormat["type"] != "json_object" {
		t.Fatalf("expected json_object response format, got %+v", gotResponseFormat)
	}
	if decision.Usage.PromptTokens != 13 || decision.Usage.CompletionTokens != 5 {
		t.Fatalf("unexpected planner usage: %+v", decision.Usage)
	}
}

// Native function calling: the model returns tool_calls instead of a JSON-text
// decision, and the planner must translate them into a validated decision.
func TestLLMPlannerNativeToolCall(t *testing.T) {
	var reqHasTools bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode planner request: %v", err)
		}
		reqHasTools = len(req.Tools) > 0
		resp := `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"rag_query","arguments":"{\"question\":\"报销制度\",\"top_k\":3}"}}]}}]}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	planner := newTestLLMPlanner(t, server.URL, "")
	decision, err := planner.Plan(context.Background(), agent.Run{Task: "报销制度", State: agent.StateRunning})
	if err != nil {
		t.Fatalf("plan native tool call: %v", err)
	}
	if !reqHasTools {
		t.Fatal("expected tools field in planner request for native function calling")
	}
	if decision.Type != agent.DecisionToolCall || decision.ToolName != ragQueryToolName {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	var args map[string]interface{}
	if err := json.Unmarshal(decision.Arguments, &args); err != nil {
		t.Fatalf("decode decision args: %v", err)
	}
	if args["question"] != "报销制度" {
		t.Fatalf("unexpected args: %+v", args)
	}
}

// Native tool calling with an unregistered tool must be rejected.
func TestLLMPlannerRejectsNativeUnregisteredTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"not_a_tool","arguments":"{}"}}]}}]}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
	defer server.Close()

	planner := newTestLLMPlanner(t, server.URL, "")
	_, err := planner.Plan(context.Background(), agent.Run{Task: "t", State: agent.StateRunning})
	if err == nil {
		t.Fatal("expected error for unregistered native tool")
	}
}

func TestLLMPlannerPlansFinalFromObservation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writePlannerResponse(t, w, `{"type":"final","thought":"observation answered","final":"报销需要发票和经理审批。"}`)
	}))
	defer server.Close()

	planner := newTestLLMPlanner(t, server.URL, "")
	decision, err := planner.Plan(context.Background(), agent.Run{
		Task:  "报销制度是什么",
		State: agent.StateRunning,
		Steps: []agent.Step{
			{Index: 1, Type: agent.StepToolCall, State: agent.StateRunning, ToolName: ragQueryToolName, Observation: "报销需要发票和经理审批。"},
		},
	})
	if err != nil {
		t.Fatalf("plan final: %v", err)
	}
	if decision.Type != agent.DecisionFinal || decision.Final != "报销需要发票和经理审批。" {
		t.Fatalf("unexpected final decision: %+v", decision)
	}
}

func TestLLMPlannerAcceptsMarkdownThinkAndObjectFinal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writePlannerResponse(t, w, "<think>ignore</think>\n```json\n{\"type\":\"final\",\"final\":{\"status\":\"completed\",\"recommendation\":\"publish\",\"risk_level\":\"low\",\"summary\":\"ok\",\"findings\":[]}}\n```")
	}))
	defer server.Close()

	decision, err := newTestLLMPlanner(t, server.URL, "").Plan(context.Background(), agent.Run{Task: "review", State: agent.StateRunning})
	if err != nil {
		t.Fatalf("plan wrapped final: %v", err)
	}
	if decision.Type != agent.DecisionFinal || !strings.Contains(decision.Final, `"recommendation":"publish"`) {
		t.Fatalf("unexpected wrapped final: %+v", decision)
	}
}

func TestLLMPlannerInfersToolCallAndFinalWithoutType(t *testing.T) {
	t.Run("tool name only", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writePlannerResponse(t, w, `{"name":"rag_query","arguments":{"question":"报销制度"}}`)
		}))
		defer server.Close()
		decision, err := newTestLLMPlanner(t, server.URL, "").Plan(context.Background(), agent.Run{Task: "q", State: agent.StateRunning})
		if err != nil || decision.Type != agent.DecisionToolCall || decision.ToolName != ragQueryToolName {
			t.Fatalf("infer tool call: %+v err=%v", decision, err)
		}
	})
	t.Run("report object", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writePlannerResponse(t, w, `{"status":"completed","recommendation":"publish","risk_level":"low","summary":"ok","findings":[]}`)
		}))
		defer server.Close()
		decision, err := newTestLLMPlanner(t, server.URL, "").Plan(context.Background(), agent.Run{Task: "q", State: agent.StateRunning})
		if err != nil || decision.Type != agent.DecisionFinal || !strings.Contains(decision.Final, `"status":"completed"`) {
			t.Fatalf("infer final: %+v err=%v", decision, err)
		}
	})
}

func TestLLMPlannerPlansTaskStatusToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writePlannerResponse(t, w, `{"type":"tool_call","thought":"need task status","tool_name":"etl_task_status","arguments":{"task_id":"doc-123"}}`)
	}))
	defer server.Close()

	planner := newTestLLMPlanner(t, server.URL, "")
	decision, err := planner.Plan(context.Background(), agent.Run{
		Task:  "查询任务 doc-123 状态",
		State: agent.StateRunning,
	})
	if err != nil {
		t.Fatalf("plan task status tool call: %v", err)
	}
	if decision.Type != agent.DecisionToolCall || decision.ToolName != etlTaskStatusToolName {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	var args map[string]interface{}
	if err := json.Unmarshal(decision.Arguments, &args); err != nil {
		t.Fatalf("decode decision args: %v", err)
	}
	if args["task_id"] != "doc-123" {
		t.Fatalf("unexpected args: %+v", args)
	}
}

func TestLLMPlannerRejectsInvalidDecision(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "invalid json",
			content: "not-json",
			want:    "invalid JSON decision",
		},
		{
			name:    "unknown tool",
			content: `{"type":"tool_call","tool_name":"delete_database","arguments":{}}`,
			want:    "unregistered tool",
		},
		{
			name:    "invalid arguments",
			content: `{"type":"tool_call","tool_name":"rag_query","arguments":{"top_k":3}}`,
			want:    "missing required argument",
		},
		{
			name:    "empty final",
			content: `{"type":"final","final":""}`,
			want:    "requires final",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writePlannerResponse(t, w, tc.content)
			}))
			defer server.Close()

			planner := newTestLLMPlanner(t, server.URL, "")
			_, err := planner.Plan(context.Background(), agent.Run{Task: "test", State: agent.StateRunning})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestLLMPlannerRetryAccumulatesTokenUsage(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"not-json"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"type\":\"final\",\"final\":\"done\"}"}}],"usage":{"prompt_tokens":7,"completion_tokens":4}}`))
	}))
	defer server.Close()

	decision, err := newTestLLMPlanner(t, server.URL, "").Plan(context.Background(), agent.Run{Task: "retry", State: agent.StateRunning})
	if err != nil || calls != 2 {
		t.Fatalf("retry failed: calls=%d err=%v", calls, err)
	}
	if decision.Usage.PromptTokens != 10 || decision.Usage.CompletionTokens != 6 {
		t.Fatalf("unexpected accumulated usage: %+v", decision.Usage)
	}
}

func TestLLMPlannerCapsCompletionAndEstimatesMissingUsage(t *testing.T) {
	var requestedMaxTokens int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		requestedMaxTokens = request.MaxTokens
		writePlannerResponse(t, w, `{"type":"final","final":"done"}`)
	}))
	defer server.Close()

	decision, err := newTestLLMPlanner(t, server.URL, "").Plan(context.Background(), agent.Run{
		Task: "budget", State: agent.StateRunning, MaxTokenBudget: 100, TokensUsed: 75,
	})
	if err != nil {
		t.Fatal(err)
	}
	if requestedMaxTokens != 25 {
		t.Fatalf("expected remaining completion budget 25, got %d", requestedMaxTokens)
	}
	if !decision.Usage.Estimated || decision.Usage.Total() <= 0 {
		t.Fatalf("expected conservative usage estimate, got %+v", decision.Usage)
	}
}

func TestNewPlannerSelectsRuleForDevAutoAndLLMForExplicitLLM(t *testing.T) {
	registry := agent.NewRegistry()
	if err := registry.Register(agent.ToolDefinition{
		Name: ragQueryToolName,
		Parameters: agent.JSONSchema{
			Type:     "object",
			Required: []string{"question"},
			Properties: map[string]agent.SchemaProperty{
				"question": {Type: "string"},
			},
		},
	}, func(context.Context, agent.ToolInvocation) (agent.ToolResult, error) {
		return agent.ToolResult{}, nil
	}); err != nil {
		t.Fatalf("register tool: %v", err)
	}

	devPlanner, err := newPlanner(testPlannerConfig("dev", ""), registry)
	if err != nil {
		t.Fatalf("new dev planner: %v", err)
	}
	if _, ok := devPlanner.(RulePlanner); !ok {
		t.Fatalf("expected dev auto planner to use RulePlanner, got %T", devPlanner)
	}

	cfg := testPlannerConfig("dev", "")
	cfg.AgentPlannerType = "llm"
	llmPlanner, err := newPlanner(cfg, registry)
	if err != nil {
		t.Fatalf("new explicit llm planner: %v", err)
	}
	if _, ok := llmPlanner.(*LLMPlanner); !ok {
		t.Fatalf("expected explicit llm planner, got %T", llmPlanner)
	}
}

func newTestLLMPlanner(t *testing.T, endpoint string, apiKey string) *LLMPlanner {
	t.Helper()
	planner, err := NewLLMPlanner(LLMPlannerOptions{
		Endpoint:  endpoint,
		APIKey:    apiKey,
		Model:     "planner-test",
		MaxTokens: 256,
		Timeout:   time.Second,
		Tools: []agent.ToolDefinition{
			{
				Name:        ragQueryToolName,
				Description: "query docs",
				Parameters: agent.JSONSchema{
					Type:     "object",
					Required: []string{"question"},
					Properties: map[string]agent.SchemaProperty{
						"question": {Type: "string"},
						"top_k":    {Type: "integer"},
					},
				},
			},
			{
				Name:        etlTaskStatusToolName,
				Description: "query task status",
				Parameters: agent.JSONSchema{
					Type:     "object",
					Required: []string{"task_id"},
					Properties: map[string]agent.SchemaProperty{
						"task_id": {Type: "string"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("new llm planner: %v", err)
	}
	return planner
}

func testPlannerConfig(environment string, endpoint string) config.Config {
	if endpoint == "" {
		endpoint = "http://planner.local/v1/chat/completions"
	}
	return config.Config{
		Environment:           environment,
		AgentPlannerType:      "auto",
		AgentPlannerEndpoint:  endpoint,
		AgentPlannerModel:     "planner-test",
		AgentPlannerTimeout:   time.Second,
		AgentPlannerMaxTokens: 256,
	}
}

func writePlannerResponse(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"choices": []map[string]interface{}{
			{"message": map[string]string{"content": content}},
		},
	}); err != nil {
		t.Fatalf("write response: %v", err)
	}
}
