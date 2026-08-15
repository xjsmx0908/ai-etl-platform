package main

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-etl-pipeline/internal/audit"
	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/query"
)

type documentView struct {
	DocID       string            `json:"doc_id"`
	TenantID    string            `json:"tenant_id"`
	FileName    string            `json:"file_name"`
	Permission  string            `json:"permission"`
	Status      string            `json:"status"`
	Stage       string            `json:"stage"`
	ChunksDone  int               `json:"chunks_done"`
	ChunksTotal int               `json:"chunks_total"`
	FileSize    int64             `json:"file_size"`
	Error       string            `json:"error,omitempty"`
	Metadata    map[string]string `json:"metadata"`
	UploadedBy  string            `json:"uploaded_by"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	CompletedAt *time.Time        `json:"completed_at,omitempty"`
}

func toDocView(d docstore.Document) documentView {
	v := documentView{
		DocID:       d.DocID,
		TenantID:    d.TenantID,
		FileName:    d.FileName,
		Permission:  d.Permission,
		Status:      d.Status,
		Stage:       d.Stage,
		ChunksDone:  d.ChunksDone,
		ChunksTotal: d.ChunksTotal,
		FileSize:    d.FileSize,
		Error:       d.Error,
		Metadata:    d.Metadata,
		UploadedBy:  d.UploadedBy,
		CreatedAt:   d.CreatedAt,
		UpdatedAt:   d.UpdatedAt,
	}
	if !d.CompletedAt.IsZero() {
		t := d.CompletedAt
		v.CompletedAt = &t
	}
	return v
}

// permissionAllowed reports whether a role may access a document with the given
// permission level. It uses the same matrix as query filtering, so a document
// that would never appear in query results is also invisible in the registry.
func permissionAllowed(role, permission string) bool {
	for _, p := range query.AllowedPermissionsForRole(role) {
		if p == permission {
			return true
		}
	}
	return false
}

func hasScope(scopes []string, required string) bool {
	for _, s := range scopes {
		if s == required {
			return true
		}
	}
	return false
}

// handleDocuments serves GET /v1/documents: paginated registry listing scoped to
// the caller's tenant and filtered to the permissions their role may access.
func handleDocuments(docs docstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		q := r.URL.Query()
		limit, offset := parsePaging(q.Get("limit"), q.Get("offset"))

		role := auth.GetPermission(r.Context())
		// The role's allowed set bounds what the caller may ever see; a requested
		// permission filter intersects with it rather than widening it.
		requested := q.Get("permission")
		allowed := query.AllowedPermissionsForRole(role)
		var permissions []string
		if requested != "" {
			for _, p := range allowed {
				if p == requested {
					permissions = append(permissions, p)
				}
			}
			if len(permissions) == 0 {
				writeJSON(w, http.StatusOK, map[string]any{"items": []documentView{}, "total": 0, "limit": limit, "offset": offset})
				return
			}
		} else {
			permissions = allowed
		}

		docs, total, err := docs.List(r.Context(), docstore.ListQuery{
			TenantID:    auth.GetTenantID(r.Context()),
			Status:      q.Get("status"),
			Permissions: permissions,
			Search:      q.Get("q"),
			Limit:       limit,
			Offset:      offset,
		})
		if err != nil {
			slog.Error("document list failed", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list documents")
			return
		}
		views := make([]documentView, 0, len(docs))
		for _, d := range docs {
			views = append(views, toDocView(d))
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"items":  views,
			"total":  total,
			"limit":  limit,
			"offset": offset,
		})
	}
}

// handleDocument serves /v1/documents/{doc_id}: GET detail (role-filtered),
// DELETE (requires upload scope). Both are tenant-scoped: a document in another
// tenant 404s instead of leaking existence.
func handleDocument(cfg config.Config, qs *query.Service, s3Client documentObjectStore, docs docstore.Store, audits audit.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		docID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/documents/"), "/")
		if docID == "" {
			writeError(w, http.StatusBadRequest, "doc_id is required")
			return
		}
		tenantID := auth.GetTenantID(r.Context())
		if tenantID == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		doc, found, err := docs.Get(r.Context(), tenantID, docID)
		if err != nil {
			slog.Error("document lookup failed", "doc_id", docID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		// A missing document (or one in another tenant) is indistinguishable:
		// both 404, so existence does not leak across tenants.
		if !found {
			writeError(w, http.StatusNotFound, "document not found")
			return
		}

		switch r.Method {
		case http.MethodGet:
			if !permissionAllowed(auth.GetPermission(r.Context()), doc.Permission) {
				writeError(w, http.StatusNotFound, "document not found")
				return
			}
			writeJSON(w, http.StatusOK, toDocView(doc))
		case http.MethodDelete:
			if !hasScope(auth.GetScopes(r.Context()), "upload") {
				writeError(w, http.StatusForbidden, "missing scope: upload")
				return
			}
			deleteDocument(w, r, cfg, qs, s3Client, docs, tenantID, docID)
			recordAudit(r.Context(), audits, audit.Entry{
				TenantID: tenantID, ActorUserID: auth.GetUserID(r.Context()),
				ActorRole: auth.GetPermission(r.Context()),
				Action:    "delete", ResourceType: "document", ResourceID: docID,
				Result: audit.ResultSuccess,
			})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// deleteDocument removes the registry row and everything derived from it. The
// registry row is deleted only after the cascade succeeds so a partial failure
// leaves a retryable, visible record.
func deleteDocument(w http.ResponseWriter, r *http.Request, cfg config.Config, qs *query.Service, s3Client documentObjectStore, docs docstore.Store, tenantID, docID string) {
	errs := cascadeDeleteDoc(r.Context(), cfg, s3Client, tenantID, docID)
	if len(errs) > 0 {
		slog.Error("document delete partial failure", "doc_id", docID, "tenant_id", tenantID, "errors", errs)
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "partial delete failure", "details": errs,
		})
		return
	}
	if err := docs.Delete(r.Context(), tenantID, docID); err != nil {
		slog.Error("document registry delete failed", "doc_id", docID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to remove document metadata")
		return
	}
	// Dropping a document may invalidate answers grounded in it.
	if err := qs.InvalidateSemanticCache(r.Context()); err != nil {
		slog.Warn("semantic cache flush failed after delete", "doc_id", docID, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func parsePaging(limitStr, offsetStr string) (int, int) {
	limit := defaultPageSize
	offset := 0
	if v := limitStr; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	if v := offsetStr; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}
