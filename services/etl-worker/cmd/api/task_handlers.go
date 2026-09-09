package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/ingestion"
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/taskstatus"
)

func newTaskStatusStore(cfg config.Config) (model.TaskStatusStore, error) {
	if cfg.ResolvedTaskStatusStore() == config.TaskStatusStoreMemory {
		return taskstatus.NewMemoryStore(), nil
	}
	return taskstatus.NewRedisStore(cfg.RedisStateAddr, cfg.RedisStatePassword, cfg.RedisStateDB, cfg.TaskStatusTTL)
}

// handleTaskStatus returns the async processing status for a document, so the
// demo can show the upload → parse → embed → store pipeline live.
func handleTaskStatus(taskStatusStore model.TaskStatusStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		docID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/")
		if docID == "" {
			http.Error(w, "doc_id is required", http.StatusBadRequest)
			return
		}
		tenantID := auth.GetTenantID(r.Context())
		if tenantID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		status, ok, err := taskStatusStore.Load(r.Context(), tenantID, docID)
		if err != nil {
			slog.Error("task status load failed", "doc_id", docID, "error", err)
			http.Error(w, `{"error":"status lookup failed"}`, http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	}
}

func handleTaskCancel(taskStatusStore model.TaskStatusStore, jobs ingestion.JobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			return
		}
		docID := r.PathValue("docID")
		if docID == "" {
			writeError(w, http.StatusBadRequest, "doc_id is required")
			return
		}
		tenantID := auth.GetTenantID(r.Context())
		status, found, err := taskStatusStore.Load(r.Context(), tenantID, docID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "status lookup failed")
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		if !isAdminRole(auth.GetPermission(r.Context())) && (status.UploadedBy == "" || status.UploadedBy != auth.GetUserID(r.Context())) {
			writeError(w, http.StatusForbidden, "only the uploader or an admin may cancel this task")
			return
		}
		if status.Status == model.TaskStatusCompleted || status.Status == model.TaskStatusFailed || status.Status == model.TaskStatusCancelled {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(status)
			return
		}
		status.Status = model.TaskStatusCancelled
		status.Stage = "cancelled"
		status.Error = "task cancelled by user"
		status.UpdatedAt = time.Now().UTC()
		if canceller, ok := jobs.(interface {
			Cancel(context.Context, model.Task, string, time.Time) error
		}); ok && status.JobID != "" && status.EventID != "" {
			if err := canceller.Cancel(r.Context(), model.Task{JobID: status.JobID, EventID: status.EventID, DocID: status.DocID, TenantID: status.TenantID, FilePath: status.FilePath, FileHash: status.FileHash}, status.Error, status.UpdatedAt); err != nil {
				writeError(w, http.StatusConflict, "task cancellation could not be persisted")
				return
			}
		}
		if err := taskStatusStore.Save(r.Context(), status); err != nil {
			writeError(w, http.StatusInternalServerError, "status update failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	}
}
