package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"ai-etl-pipeline/internal/audit"
	"ai-etl-pipeline/internal/config"
)

const defaultJSONBodyBytes int64 = 16 << 10

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

func jsonBodyLimit(cfg config.Config) int64 {
	if cfg.JSONMaxBodyBytes > 0 {
		return cfg.JSONMaxBodyBytes
	}
	return defaultJSONBodyBytes
}

func isMaxBytesError(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr)
}

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64, invalidMessage string) bool {
	if maxBytes <= 0 {
		maxBytes = defaultJSONBodyBytes
	}
	r.Body = http.MaxBytesReader(nil, r.Body, maxBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		if isMaxBytesError(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, invalidMessage)
		return false
	}
	return true
}
