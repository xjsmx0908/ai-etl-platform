package main

import (
	"net/http"
	"time"

	"ai-etl-pipeline/internal/audit"
	"ai-etl-pipeline/internal/auth"
)

type auditView struct {
	ActorUserID  string         `json:"actor_user_id"`
	ActorRole    string         `json:"actor_role"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type,omitempty"`
	ResourceID   string         `json:"resource_id,omitempty"`
	Result       string         `json:"result"`
	Detail       map[string]any `json:"detail,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

func toAuditView(e audit.Entry) auditView {
	return auditView{
		ActorUserID:  e.ActorUserID,
		ActorRole:    e.ActorRole,
		Action:       e.Action,
		ResourceType: e.ResourceType,
		ResourceID:   e.ResourceID,
		Result:       e.Result,
		Detail:       e.Detail,
		CreatedAt:    e.CreatedAt,
	}
}

// handleAuditList serves GET /v1/audit: paginated audit trail for the caller's
// tenant, optionally filtered by action. Admin scope only.
func handleAuditList(audits audit.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		limit, offset := parsePaging(r.URL.Query().Get("limit"), r.URL.Query().Get("offset"))

		entries, total, err := audits.List(r.Context(), audit.ListQuery{
			TenantID: auth.GetTenantID(r.Context()),
			Action:   r.URL.Query().Get("action"),
			Limit:    limit,
			Offset:   offset,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list audit")
			return
		}
		views := make([]auditView, 0, len(entries))
		for _, e := range entries {
			views = append(views, toAuditView(e))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"items":  views,
			"total":  total,
			"limit":  limit,
			"offset": offset,
		})
	}
}
