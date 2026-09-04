package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-etl-pipeline/internal/agent"
	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/publicationworkflow"
	"ai-etl-pipeline/internal/releasecenter"
	"ai-etl-pipeline/internal/store"
)

type releaseRequestLister interface {
	ListRequests(context.Context, string, int) ([]releasecenter.ReleaseRequest, error)
}

type releaseRequestService interface {
	releaseRequestLister
	releasecenter.Store
}

func handleReleaseCenterOverview(store releasecenter.OverviewStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if auth.GetPermission(r.Context()) != "admin" {
			writeError(w, http.StatusForbidden, "admin role required")
			return
		}
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 200 {
				limit = parsed
			}
		}
		items, err := store.ListOverview(r.Context(), auth.GetTenantID(r.Context()), limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list release overview")
			return
		}
		if items == nil {
			items = []releasecenter.OverviewItem{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

// handleReleaseCenterReviewReport exposes read-only, tenant-scoped review
// evidence for overview records that no longer have a durable request.
func handleReleaseCenterReviewReport(store interface {
	GetReview(context.Context, string, string) (releasecenter.ReviewReport, error)
}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if auth.GetPermission(r.Context()) != "admin" {
			writeError(w, http.StatusForbidden, "admin role required")
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 4 || parts[0] != "v1" || parts[1] != "release-center" || parts[2] != "review-reports" || parts[3] == "" {
			http.NotFound(w, r)
			return
		}
		review, err := store.GetReview(r.Context(), auth.GetTenantID(r.Context()), parts[3])
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"review": review})
	}
}

// handleReleaseCenterRequests serves the durable business queue. It does not
// expose Agent Run storage and derives tenant scope only from authentication.
func handleReleaseCenterRequests(store releaseRequestLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if auth.GetPermission(r.Context()) != "admin" {
			writeError(w, http.StatusForbidden, "admin role required")
			return
		}
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 200 {
				limit = parsed
			}
		}
		requests, err := store.ListRequests(r.Context(), auth.GetTenantID(r.Context()), limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list release requests")
			return
		}
		if requests == nil {
			requests = []releasecenter.ReleaseRequest{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": requests})
	}
}

func handleReleaseCenterDecision(store releasecenter.Store, workflow releasecenter.PublicationWorkflow) http.HandlerFunc {
	approval := releasecenter.NewApprovalService(workflow, store)
	return func(w http.ResponseWriter, r *http.Request) {
		if auth.GetPermission(r.Context()) != "admin" {
			writeError(w, http.StatusForbidden, "admin role required")
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 4 || parts[0] != "v1" || parts[1] != "release-center" || parts[2] != "requests" || parts[3] == "" {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 4 && r.Method == http.MethodGet {
			request, err := store.GetRequest(r.Context(), auth.GetTenantID(r.Context()), parts[3])
			if err != nil {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			review, err := store.GetReview(r.Context(), auth.GetTenantID(r.Context()), request.ReviewID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			decisions, err := store.ListDecisions(r.Context(), auth.GetTenantID(r.Context()), request.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if decisions == nil {
				decisions = []releasecenter.Decision{}
			}
			writeJSON(w, http.StatusOK, map[string]any{"request": request, "review": review, "decisions": decisions})
			return
		}
		if len(parts) != 5 || parts[4] != "decision" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var body struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		result, err := approval.Decide(r.Context(), publicationworkflow.Actor{TenantID: auth.GetTenantID(r.Context()), UserID: auth.GetUserID(r.Context()), Role: auth.GetPermission(r.Context())}, parts[3], body.Decision, body.Reason)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		review, err := store.GetReview(r.Context(), auth.GetTenantID(r.Context()), result.Request.ReviewID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"request": result.Request, "review": review, "decisions": result.Decisions})
	}
}

type releaseCenterChunkReader interface {
	ListChunksByDoc(context.Context, string, string, []string) ([]store.StoredChunk, error)
}

// releaseCenterAgentReviewService is the narrow seam needed by the release
// center. Keeping the adapter independent from the concrete Agent service
// makes exact-candidate/content review behavior testable without a live
// orchestrator.
type releaseCenterAgentReviewService interface {
	ReviewPublication(context.Context, agent.Actor, string) (string, publicationworkflow.Assessment, error)
}

type releaseCenterReviewer struct {
	service   releaseCenterAgentReviewService
	documents docstore.Store
	chunks    releaseCenterChunkReader
}

func (r releaseCenterReviewer) Review(ctx context.Context, actor publicationworkflow.Actor, documentID string) (releasecenter.AgentReview, error) {
	if r.service == nil || r.documents == nil || r.chunks == nil {
		return releasecenter.AgentReview{}, fmt.Errorf("release center content review is not configured")
	}
	runID, assessment, err := r.service.ReviewPublication(ctx, agent.Actor{TenantID: actor.TenantID, UserID: "release-center-agent", Role: "admin", Permissions: []string{"agent", "query"}}, documentID)
	if err != nil {
		return releasecenter.AgentReview{RunID: runID}, err
	}
	if !assessment.Ready || assessment.Candidate == nil {
		return releasecenter.AgentReview{RunID: runID, Status: "failed", Recommendation: "manual_review", RiskLevel: releasecenter.RiskHigh, Summary: "预审未返回可绑定的精确候选"}, nil
	}
	doc, found, err := r.documents.Get(ctx, actor.TenantID, documentID)
	if err != nil || !found {
		if err != nil {
			return releasecenter.AgentReview{RunID: runID}, err
		}
		return releasecenter.AgentReview{RunID: runID}, docstore.ErrNotFound
	}
	chunks, err := r.chunks.ListChunksByDoc(ctx, actor.TenantID, documentID, nil)
	if err != nil {
		return releasecenter.AgentReview{RunID: runID}, err
	}
	risk := releasecenter.RiskLow
	if len(assessment.Blockers) > 0 {
		risk = releasecenter.RiskMedium
	}
	content := make([]releasecenter.ContentChunk, 0, len(chunks))
	for _, chunk := range chunks {
		if chunk.DocumentVersionID != assessment.Candidate.DocumentVersionID || chunk.GenerationID != assessment.Candidate.GenerationID {
			continue
		}
		content = append(content, releasecenter.ContentChunk{ChunkID: chunk.ChunkID, Content: chunk.Content})
	}
	contentReview := releasecenter.AnalyzeContent(doc.Permission, content)
	if contentReview.Failed {
		return releasecenter.AgentReview{RunID: runID, Status: "failed", Recommendation: "manual_review", RiskLevel: contentReview.Risk, Summary: contentReview.Summary, Findings: contentReview.Findings, PromptVersion: "document-review-v1"}, nil
	}
	if contentReview.Risk == releasecenter.RiskHigh || contentReview.Risk == releasecenter.RiskCritical {
		risk = contentReview.Risk
	}
	recommendation := map[bool]string{true: "publish", false: "needs_info"}[assessment.Ready]
	if contentReview.Recommendation != "publish" {
		recommendation = contentReview.Recommendation
	}
	findings := contentReview.Findings
	summary := strings.Join(append(append([]string{}, assessment.Blockers...), contentReview.Summary), ", ")
	return releasecenter.AgentReview{RunID: runID, Status: "completed", Recommendation: recommendation, RiskLevel: risk, Summary: summary, Findings: findings, PromptVersion: "document-review-v1"}, nil
}

func handleReleaseCenterReview(coordinator *releasecenter.Coordinator) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if auth.GetPermission(r.Context()) != "admin" {
			writeError(w, http.StatusForbidden, "admin role required")
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) != 4 || parts[0] != "v1" || parts[1] != "release-center" || parts[2] != "reviews" || parts[3] == "" {
			http.NotFound(w, r)
			return
		}
		report, request, err := coordinator.StartManagedReview(r.Context(), publicationworkflow.Actor{
			TenantID: auth.GetTenantID(r.Context()), UserID: auth.GetUserID(r.Context()), Role: auth.GetPermission(r.Context()),
		}, parts[3])
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"review": report, "request": request})
	}
}

func runReleaseReviewCollector(ctx context.Context, coordinator *releasecenter.Coordinator, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := coordinator.RunPendingReviews(ctx, publicationworkflow.Actor{UserID: "release-center-system", Role: "admin"}, 25); err != nil && ctx.Err() == nil {
			slog.Warn("automatic release review failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
