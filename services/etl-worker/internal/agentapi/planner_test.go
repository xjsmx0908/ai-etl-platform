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
		writePlannerResponse(t, w, `{"type":"tool_call","thought":"need retrieval","tool_name":"rag_query","arguments":{"question":"报销制度是什么","top_k":3}}`)
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
