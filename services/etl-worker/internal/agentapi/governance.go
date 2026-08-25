package agentapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ai-etl-pipeline/internal/agent"
	"ai-etl-pipeline/internal/publicationworkflow"
)

const (
	documentPublicationWorkflow = "document_publication"
	governanceTaskPrefix        = documentPublicationWorkflow + ":"
	assessPublicationToolName   = "assess_document_publication"
	publishDocumentToolName     = "publish_document"
)

type PublicationWorkflow interface {
	Assess(context.Context, publicationworkflow.Actor, string) (publicationworkflow.Assessment, error)
	PublishApproved(context.Context, publicationworkflow.Actor, string, string) (publicationworkflow.PublicationResult, error)
}

func registerPublicationWorkflowTools(registry *agent.Registry, workflow PublicationWorkflow) error {
	if workflow == nil {
		return fmt.Errorf("publication workflow is required")
	}
	if err := registry.Register(agent.ToolDefinition{
		Name: assessPublicationToolName, Description: "Assess whether a managed-space draft is ready for publication.",
		RequiredPermissions: []string{"agent"}, Timeout: 20 * time.Second, Idempotent: true,
		Parameters: agent.JSONSchema{Type: "object", Required: []string{"document_id"}, Properties: map[string]agent.SchemaProperty{
			"document_id": {Type: "string", Description: "Tenant-scoped document id."},
		}},
	}, func(ctx context.Context, inv agent.ToolInvocation) (agent.ToolResult, error) {
		docID, _ := inv.Arguments["document_id"].(string)
		assessment, err := workflow.Assess(ctx, publicationActor(inv), docID)
		if err != nil {
			return agent.ToolResult{}, err
		}
		return agent.ToolResult{Content: assessmentSummary(assessment), Data: structMap(assessment)}, nil
	}); err != nil {
		return err
	}
	return registry.Register(agent.ToolDefinition{
		Name: publishDocumentToolName, Description: "Publish a ready managed-space draft after explicit administrator approval.",
		RequiredPermissions: []string{"agent"}, Timeout: 20 * time.Second, Idempotent: true,
		SideEffect: true, RequiresApproval: true,
		Parameters: agent.JSONSchema{Type: "object", Required: []string{"document_id"}, Properties: map[string]agent.SchemaProperty{
			"document_id": {Type: "string", Description: "Ready tenant-scoped document id."},
		}},
	}, func(ctx context.Context, inv agent.ToolInvocation) (agent.ToolResult, error) {
		docID, _ := inv.Arguments["document_id"].(string)
		result, err := workflow.PublishApproved(ctx, publicationActor(inv), docID, inv.IdempotencyKey)
		if err != nil {
			return agent.ToolResult{}, err
		}
		return agent.ToolResult{Content: "文档已批准并发布。", Data: structMap(result)}, nil
	})
}

type GovernancePlanner struct{}

func (GovernancePlanner) Plan(_ context.Context, run agent.Run) (agent.PlanDecision, error) {
	docID, ok := governanceDocumentID(run.Task)
	if !ok {
		return agent.PlanDecision{}, fmt.Errorf("invalid document publication task")
	}
	args, _ := json.Marshal(map[string]string{"document_id": docID})
	if len(run.Steps) == 0 {
		return agent.PlanDecision{Type: agent.DecisionToolCall, ToolName: assessPublicationToolName, Arguments: args}, nil
	}
	last := run.Steps[len(run.Steps)-1]
	if last.ToolResult == nil {
		return agent.PlanDecision{}, fmt.Errorf("governance step has no result")
	}
	switch last.ToolName {
	case assessPublicationToolName:
		ready, _ := last.ToolResult.Data["ready"].(bool)
		if !ready {
			return agent.PlanDecision{Type: agent.DecisionFinal, Final: last.ToolResult.Content}, nil
		}
		return agent.PlanDecision{Type: agent.DecisionToolCall, ToolName: publishDocumentToolName, Arguments: args}, nil
	case publishDocumentToolName:
		return agent.PlanDecision{Type: agent.DecisionFinal, Final: last.ToolResult.Content}, nil
	default:
		return agent.PlanDecision{}, fmt.Errorf("unexpected governance tool %q", last.ToolName)
	}
}

type routingPlanner struct {
	fallback   agent.Planner
	governance agent.Planner
}

func (p routingPlanner) Plan(ctx context.Context, run agent.Run) (agent.PlanDecision, error) {
	if _, ok := governanceDocumentID(run.Task); ok {
		return p.governance.Plan(ctx, run)
	}
	return p.fallback.Plan(ctx, run)
}

func governanceDocumentID(task string) (string, bool) {
	if !strings.HasPrefix(task, governanceTaskPrefix) {
		return "", false
	}
	docID := strings.TrimSpace(strings.TrimPrefix(task, governanceTaskPrefix))
	return docID, docID != ""
}

func publicationActor(inv agent.ToolInvocation) publicationworkflow.Actor {
	return publicationworkflow.Actor{TenantID: inv.TenantID, UserID: inv.UserID, Role: inv.Role}
}

func assessmentSummary(assessment publicationworkflow.Assessment) string {
	if assessment.Ready {
		return "文档检查通过，等待管理员审批发布。"
	}
	return "文档暂不可发布：" + strings.Join(assessment.Blockers, "、")
}

func structMap(value any) map[string]interface{} {
	data, _ := json.Marshal(value)
	out := map[string]interface{}{}
	_ = json.Unmarshal(data, &out)
	return out
}
