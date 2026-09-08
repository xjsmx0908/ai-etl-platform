package releasecenter

import (
	"context"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"ai-etl-pipeline/internal/notification"
)

const workflowDecisionPath = "/v1/release-center/workflow/decision"

func notifyRelease(ctx context.Context, enqueuer notification.Enqueuer, request ReleaseRequest, eventType string, extra map[string]string) {
	if enqueuer == nil {
		return
	}
	query := url.Values{}
	if strings.TrimSpace(request.DocumentID) != "" {
		query.Set("document", request.DocumentID)
	}
	if strings.TrimSpace(request.ID) != "" {
		query.Set("request", request.ID)
	}
	publicPath := "/agent"
	if encoded := query.Encode(); encoded != "" {
		publicPath += "?" + encoded
	}
	payload := map[string]string{
		"tenant_id":          request.TenantID,
		"request_id":         request.ID,
		"document_id":        request.DocumentID,
		"review_id":          request.ReviewID,
		"state":              string(request.State),
		"required_approvals": strconv.Itoa(request.RequiredApprovals),
		"approver_group_id":  request.ApproverGroupID,
		"policy_id":          request.PolicyID,
		"public_path":        publicPath,
		"decision_path":      workflowDecisionPath,
	}
	for key, value := range extra {
		if strings.TrimSpace(value) == "" {
			continue
		}
		payload[key] = value
	}
	event, err := notification.NewEvent(notification.SourceReleaseCenter, request.ID, eventType, request.TenantID, payload)
	if err != nil {
		slog.Error("release notification event rejected", "request_id", request.ID, "event_type", eventType, "error", err)
		return
	}
	if err := enqueuer.Enqueue(ctx, event); err != nil {
		slog.Error("release notification enqueue failed", "request_id", request.ID, "event_type", eventType, "error", err)
	}
}
