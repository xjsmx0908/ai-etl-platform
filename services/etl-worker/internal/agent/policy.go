package agent

import (
	"context"
	"fmt"
	"strings"
)

// ApprovalRequiredError signals that a tool call must wait for explicit approval.
type ApprovalRequiredError struct {
	ToolName string
}

func (e ApprovalRequiredError) Error() string {
	return fmt.Sprintf("tool %q requires approval", e.ToolName)
}

// Authorizer decides whether an actor may execute a tool.
type Authorizer interface {
	Authorize(ctx context.Context, actor Actor, tool ToolDefinition) error
}

// StaticAuthorizer enforces tenant/user presence, tool permissions, and explicit approval.
type StaticAuthorizer struct{}

// Authorize checks the actor against a tool definition.
func (StaticAuthorizer) Authorize(_ context.Context, actor Actor, tool ToolDefinition) error {
	if strings.TrimSpace(actor.TenantID) == "" {
		return fmt.Errorf("tenant_id is required")
	}
	if strings.TrimSpace(actor.UserID) == "" {
		return fmt.Errorf("user_id is required")
	}
	for _, required := range tool.RequiredPermissions {
		required = strings.TrimSpace(required)
		if required == "" {
			continue
		}
		if !hasString(actor.Permissions, required) {
			return fmt.Errorf("missing tool permission %q", required)
		}
	}
	if tool.RequiresApproval && !hasString(actor.ApprovedTools, tool.Name) {
		return ApprovalRequiredError{ToolName: tool.Name}
	}
	return nil
}

func hasString(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}
