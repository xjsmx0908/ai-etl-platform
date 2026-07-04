package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestRegistry_ValidatesArgumentsBeforeExecutingTool(t *testing.T) {
	registry := NewRegistry()
	called := false
	err := registry.Register(ToolDefinition{
		Name:        "check_inventory",
		Description: "checks stock by sku",
		Parameters: JSONSchema{
			Type:     "object",
			Required: []string{"sku", "quantity"},
			Properties: map[string]SchemaProperty{
				"sku":      {Type: "string"},
				"quantity": {Type: "integer"},
			},
		},
	}, func(context.Context, ToolInvocation) (ToolResult, error) {
		called = true
		return ToolResult{Content: "ok"}, nil
	})
	if err != nil {
		t.Fatalf("register tool: %v", err)
	}

	_, err = registry.execute(context.Background(), Actor{TenantID: "tenant-a", UserID: "user-a"}, "run-1", 1, "check_inventory", []byte(`{"sku":"sku-1"}`), StaticAuthorizer{})
	if err == nil {
		t.Fatal("expected missing required argument error")
	}
	if called {
		t.Fatal("tool handler should not run when schema validation fails")
	}
}

func TestRegistry_RejectsUnauthorizedToolCall(t *testing.T) {
	registry := NewRegistry()
	called := false
	err := registry.Register(ToolDefinition{
		Name:                "refund_order",
		RequiredPermissions: []string{"order:refund"},
		Parameters: JSONSchema{
			Type:     "object",
			Required: []string{"order_id"},
			Properties: map[string]SchemaProperty{
				"order_id": {Type: "string"},
			},
		},
	}, func(context.Context, ToolInvocation) (ToolResult, error) {
		called = true
		return ToolResult{Content: "refunded"}, nil
	})
	if err != nil {
		t.Fatalf("register tool: %v", err)
	}

	_, err = registry.execute(context.Background(), Actor{TenantID: "tenant-a", UserID: "user-a", Permissions: []string{"inventory:read"}}, "run-1", 1, "refund_order", []byte(`{"order_id":"ord-1"}`), StaticAuthorizer{})
	if err == nil {
		t.Fatal("expected authorization error")
	}
	if called {
		t.Fatal("tool handler should not run when authorization fails")
	}
}

func TestRegistry_ExecutesValidatedAuthorizedToolWithIdempotencyKey(t *testing.T) {
	registry := NewRegistry()
	var got ToolInvocation
	err := registry.Register(ToolDefinition{
		Name:                "refund_order",
		RequiredPermissions: []string{"order:refund"},
		Timeout:             time.Second,
		Parameters: JSONSchema{
			Type:     "object",
			Required: []string{"order_id", "amount"},
			Properties: map[string]SchemaProperty{
				"order_id": {Type: "string"},
				"amount":   {Type: "number"},
			},
		},
	}, func(_ context.Context, inv ToolInvocation) (ToolResult, error) {
		got = inv
		return ToolResult{Content: "refunded", Data: map[string]interface{}{"status": "ok"}}, nil
	})
	if err != nil {
		t.Fatalf("register tool: %v", err)
	}

	raw, _ := json.Marshal(map[string]interface{}{"order_id": "ord-1", "amount": 12.5})
	result, err := registry.execute(context.Background(), Actor{
		TenantID:    "tenant-a",
		UserID:      "user-a",
		Permissions: []string{"order:refund"},
	}, "run-1", 3, "refund_order", raw, StaticAuthorizer{})
	if err != nil {
		t.Fatalf("execute tool: %v", err)
	}
	if result.Content != "refunded" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if got.IdempotencyKey != "agent:run-1:3:refund_order" {
		t.Fatalf("unexpected idempotency key: %q", got.IdempotencyKey)
	}
	if got.Arguments["order_id"] != "ord-1" {
		t.Fatalf("unexpected arguments: %+v", got.Arguments)
	}
}

func TestRegistry_RejectsUnsafeSideEffectToolWithoutIdempotencyOrCompensation(t *testing.T) {
	registry := NewRegistry()
	err := registry.Register(ToolDefinition{
		Name:       "refund_order",
		SideEffect: true,
		Idempotent: false,
		Parameters: JSONSchema{
			Type: "object",
		},
	}, func(context.Context, ToolInvocation) (ToolResult, error) {
		return ToolResult{Content: "ok"}, nil
	})
	if err == nil {
		t.Fatal("expected unsafe side-effecting tool registration to fail")
	}
}
