package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"ai-etl-pipeline/internal/audit"
)

// recordAudit appends one audit entry as a best-effort side effect. An audit
// write failure is logged and never allowed to fail the primary action.
func recordAudit(ctx context.Context, audits audit.Store, e audit.Entry) {
	if audits == nil {
		return
	}
	if err := audits.Record(ctx, e); err != nil {
		slog.Warn("audit record failed", "action", e.Action, "resource_id", e.ResourceID, "error", err)
	}
}

// writeJSON writes a JSON response with the given status code. Encode errors are
// intentionally ignored: the headers are already committed by then.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error envelope: {"error": "<message>"}.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
