package main

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-etl-pipeline/internal/audit"
	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/deletionworkflow"
	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/indexmanifest"
	"ai-etl-pipeline/internal/knowledgecatalog"
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/query"
	"ai-etl-pipeline/internal/retrieval"
	"ai-etl-pipeline/internal/store"
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
	// Controlled-document governance. DocStatus is the document's lifecycle as a
	// knowledge source (active/superseded/archived) and is a different axis from
	// Status above, which is the ETL processing state.
	DocStatus         string             `json:"doc_status,omitempty"`
	EffectiveDate     string             `json:"effective_date,omitempty"` // YYYY-MM-DD
	Supersedes        string             `json:"supersedes,omitempty"`
	Owner             string             `json:"owner,omitempty"`
	KnowledgeSpaceID  string             `json:"knowledge_space_id"`
	PublicationStatus string             `json:"publication_status"`
	DeletionStatus    string             `json:"deletion_status"`
	StageTimings      model.StageTimings `json:"stage_timings,omitempty"`
}

func toDocView(d docstore.Document) documentView {
	v := documentView{
		DocID:             d.DocID,
		TenantID:          d.TenantID,
		FileName:          d.FileName,
		Permission:        d.Permission,
		Status:            d.Status,
		Stage:             d.Stage,
		ChunksDone:        d.ChunksDone,
		ChunksTotal:       d.ChunksTotal,
		FileSize:          d.FileSize,
		Error:             d.Error,
		Metadata:          d.Metadata,
		UploadedBy:        d.UploadedBy,
		CreatedAt:         d.CreatedAt,
		UpdatedAt:         d.UpdatedAt,
		DocStatus:         d.DocStatus,
		Supersedes:        d.Supersedes,
		Owner:             d.Owner,
		KnowledgeSpaceID:  d.KnowledgeSpaceID,
		PublicationStatus: d.PublicationStatus,
		DeletionStatus:    d.DeletionStatus,
		StageTimings:      d.StageTimings,
	}
	if !d.CompletedAt.IsZero() {
		t := d.CompletedAt
		v.CompletedAt = &t
	}
	// Rendered as a plain date: the column is DATE, so a timestamp would imply a
	// precision the registry does not have.
	if !d.EffectiveDate.IsZero() {
		v.EffectiveDate = d.EffectiveDate.Format("2006-01-02")
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
func handleDocuments(docs docstore.Store, qs *query.Service) http.HandlerFunc {
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

		access := query.AccessContext{TenantID: auth.GetTenantID(r.Context()), UserID: auth.GetUserID(r.Context()), Role: role}
		spaces, err := qs.ListKnowledgeSpaces(r.Context(), access)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "knowledge catalog unavailable")
			return
		}
		spaceIDs := make([]string, 0, len(spaces))
		for _, space := range spaces {
			spaceIDs = append(spaceIDs, space.ID)
		}
		listed, total, err := docs.List(r.Context(), docstore.ListQuery{
			TenantID:          auth.GetTenantID(r.Context()),
			Status:            q.Get("status"),
			Permissions:       permissions,
			KnowledgeSpaceIDs: spaceIDs,
			Search:            q.Get("q"),
			Limit:             limit,
			Offset:            offset,
		})
		if err != nil {
			slog.Error("document list failed", "error", err)
			writeError(w, http.StatusInternalServerError, "failed to list documents")
			return
		}
		views := make([]documentView, 0, len(listed))
		for _, d := range listed {
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
type deletionAccepter interface {
	Accept(context.Context, deletionworkflow.AcceptRequest) (deletionworkflow.Job, error)
}

func handleDocument(cfg config.Config, qs *query.Service, s3Client documentObjectStore, docs docstore.Store, audits audit.Store, deletion ...deletionAccepter) http.HandlerFunc {
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
			if _, err := qs.ResolveKnowledgeSpace(r.Context(), doc.KnowledgeSpaceID, query.AccessContext{TenantID: tenantID, UserID: auth.GetUserID(r.Context()), Role: auth.GetPermission(r.Context())}, knowledgecatalog.CapabilityQuery); err != nil {
				writeError(w, http.StatusNotFound, "document not found")
				return
			}
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
			ownedByCaller := doc.UploadedBy != "" && doc.UploadedBy == auth.GetUserID(r.Context())
			if !canWritePermission(auth.GetPermission(r.Context()), doc.Permission) ||
				(!isAdminRole(auth.GetPermission(r.Context())) && !ownedByCaller) {
				writeError(w, http.StatusForbidden, "only the document's uploader or an admin may delete it")
				return
			}
			if len(deletion) > 0 && deletion[0] != nil {
				job, err := deletion[0].Accept(r.Context(), deletionworkflow.AcceptRequest{
					TenantID: tenantID, DocumentID: docID, ActorUserID: auth.GetUserID(r.Context()), ActorRole: auth.GetPermission(r.Context()),
				})
				if err != nil {
					slog.Error("document deletion acceptance failed", "doc_id", docID, "error", err)
					writeError(w, http.StatusServiceUnavailable, "deletion unavailable")
					return
				}
				if err := qs.InvalidateSemanticCache(r.Context()); err != nil {
					slog.Warn("semantic cache flush failed after deletion acceptance", "doc_id", docID, "error", err)
				}
				writeJSON(w, http.StatusAccepted, map[string]any{"job_id": job.JobID, "status": job.State})
				return
			}
			deleteDocument(w, r, cfg, qs, s3Client, docs, tenantID, docID)
		case http.MethodPatch:
			if auth.GetPermission(r.Context()) != "admin" {
				writeError(w, http.StatusForbidden, "admin role required")
				return
			}
			var input struct {
				PublicationStatus string `json:"publication_status"`
				Owner             string `json:"owner"`
				EffectiveDate     string `json:"effective_date"`
				DocStatus         string `json:"doc_status"`
				Supersedes        string `json:"supersedes"`
			}
			if !decodeJSONBody(w, r, &input, defaultJSONBodyBytes, "invalid request body") {
				return
			}
			if input.PublicationStatus != "" && input.PublicationStatus != "draft" && input.PublicationStatus != "published" && input.PublicationStatus != "retired" {
				writeError(w, http.StatusBadRequest, "invalid publication_status")
				return
			}
			if input.PublicationStatus == "published" && doc.KnowledgeSpaceID != "" && doc.KnowledgeSpaceID != "user-uploads" {
				writeError(w, http.StatusConflict, "managed documents require the publication governance workflow")
				return
			}
			if input.Owner != "" || input.EffectiveDate != "" || input.DocStatus != "" || input.Supersedes != "" {
				parsedDate := time.Time{}
				var err error
				if input.EffectiveDate != "" {
					parsedDate, err = time.Parse("2006-01-02", input.EffectiveDate)
				}
				if err != nil {
					writeError(w, http.StatusBadRequest, "effective_date must be YYYY-MM-DD")
					return
				}
				if input.DocStatus != "" && input.DocStatus != docstore.DocStatusActive && input.DocStatus != docstore.DocStatusSuperseded && input.DocStatus != docstore.DocStatusArchived {
					writeError(w, http.StatusBadRequest, "doc_status must be one of active, superseded, archived")
					return
				}
				if len(input.Owner) > 256 || len(input.Supersedes) > 256 {
					writeError(w, http.StatusBadRequest, "governance field too long")
					return
				}
				if strings.TrimSpace(input.Supersedes) == docID {
					writeError(w, http.StatusBadRequest, "supersedes must not reference the document itself")
					return
				}
				updater, ok := docs.(docstore.GovernanceUpdater)
				if !ok {
					writeError(w, http.StatusServiceUnavailable, "governance management unavailable")
					return
				}
				if err := updater.UpdateGovernance(r.Context(), tenantID, docID, input.Owner, parsedDate, input.DocStatus, input.Supersedes); err != nil {
					writeError(w, http.StatusConflict, "document governance cannot be updated")
					return
				}
				recordAudit(r.Context(), audits, audit.Entry{
					TenantID: tenantID, ActorUserID: auth.GetUserID(r.Context()), ActorRole: "admin",
					Action: "document.governance.update", ResourceType: "document", ResourceID: docID,
					Result: audit.ResultSuccess, Detail: map[string]any{
						"owner": input.Owner, "effective_date": input.EffectiveDate,
						"doc_status": input.DocStatus, "supersedes": input.Supersedes,
					},
				})
			}
			if input.PublicationStatus == "" {
				updated, found, getErr := docs.Get(r.Context(), tenantID, docID)
				if getErr != nil || !found {
					writeError(w, http.StatusInternalServerError, "failed to reload document")
					return
				}
				writeJSON(w, http.StatusOK, toDocView(updated))
				return
			}
			updater, ok := docs.(interface {
				UpdatePublication(context.Context, string, string, string) error
			})
			if !ok {
				writeError(w, http.StatusServiceUnavailable, "publication management unavailable")
				return
			}
			if err := updater.UpdatePublication(r.Context(), tenantID, docID, input.PublicationStatus); err != nil {
				writeError(w, http.StatusConflict, "document cannot enter requested publication state")
				return
			}
			if err := qs.InvalidateSemanticCache(r.Context()); err != nil {
				slog.Warn("semantic cache flush failed after publication update", "doc_id", docID, "error", err)
			}
			recordAudit(r.Context(), audits, audit.Entry{
				TenantID: tenantID, ActorUserID: auth.GetUserID(r.Context()), ActorRole: "admin",
				Action: "document.publication.update", ResourceType: "document", ResourceID: docID,
				Result: audit.ResultSuccess, Detail: map[string]any{"publication_status": input.PublicationStatus},
			})
			doc.PublicationStatus = input.PublicationStatus
			writeJSON(w, http.StatusOK, toDocView(doc))
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// documentChunkLister reads a document's chunks from the vector store.
type documentChunkLister interface {
	ListChunksByDoc(ctx context.Context, tenantID, docID string, allowedPermissions []string) ([]store.StoredChunk, error)
}

type chunkView struct {
	ChunkID  string            `json:"chunk_id"`
	Index    int               `json:"index"`
	Content  string            `json:"content"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// handleDocumentChunks serves GET /v1/documents/{docID}/chunks: the chunks of a
// single document, tenant- and permission-scoped exactly like the registry
// detail (missing/cross-tenant/not-allowed all 404).
func handleDocumentChunks(docs docstore.Store, chunks documentChunkLister, qs *query.Service, visibility ...retrieval.VisibilityResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		docID := r.PathValue("docID")
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
		if !found || !permissionAllowed(auth.GetPermission(r.Context()), doc.Permission) {
			writeError(w, http.StatusNotFound, "document not found")
			return
		}
		if _, err := qs.ResolveKnowledgeSpace(r.Context(), doc.KnowledgeSpaceID, query.AccessContext{TenantID: tenantID, UserID: auth.GetUserID(r.Context()), Role: auth.GetPermission(r.Context())}, knowledgecatalog.CapabilityQuery); err != nil {
			writeError(w, http.StatusNotFound, "document not found")
			return
		}
		allowed := query.AllowedPermissionsForRole(auth.GetPermission(r.Context()))
		list, err := chunks.ListChunksByDoc(r.Context(), tenantID, docID, allowed)
		if err != nil {
			slog.Error("chunk listing failed", "doc_id", docID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if len(visibility) > 0 && visibility[0] != nil && len(list) > 0 {
			refs := make([]indexmanifest.GenerationReference, len(list))
			for i, chunk := range list {
				refs[i] = indexmanifest.GenerationReference{
					DocumentID:        chunk.DocID,
					DocumentVersionID: chunk.DocumentVersionID,
					GenerationID:      chunk.GenerationID,
				}
			}
			visible, visErr := visibility[0].ResolveVisibility(r.Context(), tenantID, refs)
			if visErr != nil || len(visible) != len(list) {
				slog.Error("document chunk publication visibility failed", "doc_id", docID, "error", visErr)
				writeError(w, http.StatusServiceUnavailable, "publication visibility unavailable")
				return
			}
			filtered := make([]store.StoredChunk, 0, len(list))
			for i, chunk := range list {
				if visible[i] {
					filtered = append(filtered, chunk)
				}
			}
			list = filtered
		}
		items := make([]chunkView, 0, len(list))
		for _, c := range list {
			items = append(items, chunkView{ChunkID: c.ChunkID, Index: c.Index, Content: c.Content, Metadata: c.Metadata})
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"doc_id": docID,
			"total":  len(items),
			"items":  items,
		})
	}
}

// documentSearcher searches indexed chunk content (ES BM25, tenant+permission
// filtered). The existing ElasticRetriever implements it.
type documentSearcher interface {
	Search(ctx context.Context, req retrieval.SearchRequest) ([]retrieval.Candidate, error)
}

type documentSearchResultView struct {
	DocID      string  `json:"doc_id"`
	FileName   string  `json:"file_name"`
	Permission string  `json:"permission"`
	Status     string  `json:"status"`
	HitCount   int     `json:"hit_count"`
	Snippet    string  `json:"snippet"`
	BestScore  float64 `json:"best_score"`
}

// handleDocumentSearch serves GET /v1/documents/search?q=: full-text content
// search over indexed chunks, aggregated to document level. Requires ES to be
// enabled; enrichment comes from the tenant-scoped registry.
func handleDocumentSearch(cfg config.Config, docs docstore.Store, searcher documentSearcher, qs *query.Service, visibility ...retrieval.VisibilityResolver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if q == "" {
			writeError(w, http.StatusBadRequest, "q is required")
			return
		}
		if !cfg.RetrievalEnableES {
			writeError(w, http.StatusServiceUnavailable, "full-text search disabled")
			return
		}
		tenantID := auth.GetTenantID(r.Context())
		if tenantID == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		role := auth.GetPermission(r.Context())
		space, err := qs.ResolveKnowledgeSpace(r.Context(), r.URL.Query().Get("knowledge_space_id"), query.AccessContext{TenantID: tenantID, UserID: auth.GetUserID(r.Context()), Role: role}, knowledgecatalog.CapabilityQuery)
		if err != nil {
			writeError(w, http.StatusForbidden, "knowledge space forbidden")
			return
		}
		limit := searchLimit(r.URL.Query().Get("limit"))

		candidates, err := searcher.Search(r.Context(), retrieval.SearchRequest{
			Question:           q,
			Limit:              limit,
			TenantID:           tenantID,
			AllowedPermissions: query.AllowedPermissionsForRole(role),
			ExactSchemaFields:  cfg.RetrievalExactSchemaFields,
			KnowledgeBaseID:    space.ID,
		})
		if err != nil {
			slog.Error("document search failed", "q", q, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if len(visibility) > 0 && visibility[0] != nil && len(candidates) > 0 {
			refs := make([]indexmanifest.GenerationReference, len(candidates))
			for i, candidate := range candidates {
				refs[i] = indexmanifest.GenerationReference{
					DocumentID:        candidate.DocID,
					DocumentVersionID: candidate.DocumentVersionID,
					GenerationID:      candidate.GenerationID,
				}
			}
			visible, err := visibility[0].ResolveVisibility(r.Context(), tenantID, refs)
			if err != nil || len(visible) != len(candidates) {
				slog.Error("document search publication visibility failed", "error", err)
				writeError(w, http.StatusServiceUnavailable, "publication visibility unavailable")
				return
			}
			filtered := make([]retrieval.Candidate, 0, len(candidates))
			for i, candidate := range candidates {
				if visible[i] {
					filtered = append(filtered, candidate)
				}
			}
			candidates = filtered
		}

		// Aggregate chunk candidates by document.
		byDoc := map[string]*documentSearchResultView{}
		for _, c := range candidates {
			v := byDoc[c.DocID]
			if v == nil {
				v = &documentSearchResultView{DocID: c.DocID}
				byDoc[c.DocID] = v
			}
			v.HitCount++
			if c.Score > v.BestScore {
				v.BestScore = c.Score
				if c.Content != "" {
					v.Snippet = c.Content
				}
			}
		}

		// Enrich with registry metadata; skip docs not present in this tenant.
		results := make([]documentSearchResultView, 0, len(byDoc))
		for docID, v := range byDoc {
			doc, found, err := docs.Get(r.Context(), tenantID, docID)
			if err != nil {
				slog.Warn("document enrichment failed", "doc_id", docID, "error", err)
				continue
			}
			if !found {
				continue
			}
			v.FileName = doc.FileName
			v.Permission = doc.Permission
			v.Status = doc.Status
			results = append(results, *v)
		}
		sort.Slice(results, func(i, j int) bool { return results[i].BestScore > results[j].BestScore })

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"q":     q,
			"total": len(results),
			"items": results,
		})
	}
}

// searchLimit parses a request limit for content search, defaulting to 100 and
// capping at 200 (wider than the registry page size of 100).
func searchLimit(s string) int {
	if s == "" {
		return 100
	}
	if n, err := strconv.Atoi(s); err == nil && n > 0 {
		if n > 200 {
			return 200
		}
		return n
	}
	return 100
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
