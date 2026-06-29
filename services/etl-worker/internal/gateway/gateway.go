// Package gateway provides the HTTP file upload API that receives documents,
// stores them locally, and publishes processing tasks to Kafka.
package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"ai-etl-pipeline/internal/kafka"
	"ai-etl-pipeline/internal/model"
)

// Handler serves the file upload HTTP endpoint.
type Handler struct {
	producer    *kafka.Producer
	uploadDir   string
	maxFileSize int64
	taskCounter atomic.Int64
}

// NewHandler creates a gateway handler.
// uploadDir: directory to store uploaded files.
// maxFileSize: maximum upload size in bytes.
func NewHandler(producer *kafka.Producer, uploadDir string, maxFileSize int64) (*Handler, error) {
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		return nil, fmt.Errorf("create upload dir %s: %w", uploadDir, err)
	}
	return &Handler{
		producer:    producer,
		uploadDir:   uploadDir,
		maxFileSize: maxFileSize,
	}, nil
}

// UploadResponse is the JSON response returned after accepting a file.
type UploadResponse struct {
	TaskID    string `json:"task_id"`
	DocID     string `json:"doc_id"`
	Status    string `json:"status"`
	FileHash  string `json:"file_hash"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

// HandleUpload handles POST /upload for file ingestion.
// Expects multipart/form-data with fields: file, tenant_id, permission (optional).
func (h *Handler) HandleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// Limit request body size
	r.Body = http.MaxBytesReader(w, r.Body, h.maxFileSize)

	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32MB in memory
		slog.Warn("upload parse error", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "file too large or invalid multipart form",
		})
		return
	}

	// Extract fields
	tenantID := r.FormValue("tenant_id")
	if tenantID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "tenant_id is required",
		})
		return
	}
	permission := r.FormValue("permission")
	if permission == "" {
		permission = "internal" // default permission level
	}
	metadata, err := parseMetadataFormValue(r.FormValue("metadata"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": err.Error(),
		})
		return
	}

	// Get uploaded file
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "file field is required",
		})
		return
	}
	defer file.Close()

	// Generate unique DocID
	now := time.Now()
	seq := h.taskCounter.Add(1)
	docID := fmt.Sprintf("doc-%s-%04d", now.Format("20060102-150405"), seq)

	// Save file to upload directory
	ext := filepath.Ext(header.Filename)
	destPath := filepath.Join(h.uploadDir, docID+ext)
	destFile, err := os.Create(destPath)
	if err != nil {
		slog.Error("failed to create dest file", "path", destPath, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "internal server error",
		})
		return
	}

	// Stream file to disk while computing SHA-256
	hasher := sha256.New()
	writer := io.MultiWriter(destFile, hasher)
	written, err := io.Copy(writer, file)
	destFile.Close()
	if err != nil {
		os.Remove(destPath)
		slog.Error("file write error", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to save file",
		})
		return
	}

	fileHash := hex.EncodeToString(hasher.Sum(nil))

	// Build task
	task := model.Task{
		FilePath:   destPath,
		DocID:      docID,
		TenantID:   tenantID,
		Permission: permission,
		FileHash:   fileHash,
		Metadata:   metadata,
		CreatedAt:  now,
	}

	// Publish to Kafka
	if err := h.producer.Publish(r.Context(), task); err != nil {
		slog.Error("kafka publish failed", "doc_id", docID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "failed to enqueue task, please retry",
		})
		return
	}

	slog.Info("file uploaded and task enqueued",
		"doc_id", docID,
		"tenant_id", tenantID,
		"file", header.Filename,
		"size", written,
		"hash", fileHash[:12]+"...",
	)

	// Return immediate response
	writeJSON(w, http.StatusAccepted, UploadResponse{
		TaskID:    docID,
		DocID:     docID,
		Status:    "processing",
		FileHash:  fileHash,
		Message:   fmt.Sprintf("file '%s' accepted, processing in background", header.Filename),
		Timestamp: now.Format(time.RFC3339),
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func parseMetadataFormValue(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("metadata must be a JSON object with string values")
	}
	clean := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		if len(key) > 64 || len(value) > 512 {
			return nil, fmt.Errorf("metadata key/value too long")
		}
		clean[key] = value
	}
	if len(clean) == 0 {
		return nil, nil
	}
	return clean, nil
}
