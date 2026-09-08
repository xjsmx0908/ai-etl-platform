package releasecenter

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"ai-etl-pipeline/internal/notification"
)

func notifyRelease(ctx context.Context, enqueuer notification.Enqueuer, request ReleaseRequest, eventType string, extra map[string]string) {
	if enqueuer == nil {
		return
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
		"public_path":        "/agent",
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
