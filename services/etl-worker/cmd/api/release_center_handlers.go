package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-etl-pipeline/internal/agent"
	"ai-etl-pipeline/internal/agentapi"
	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/publicationworkflow"
	"ai-etl-pipeline/internal/releasecenter"
)

type releaseRequestLister interface {
	ListRequests(context.Context, string, int) ([]releasecenter.ReleaseRequest, error)
}

type releaseRequestService interface {
	releaseRequestLister
	releasecenter.Store
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

type releaseCenterReviewer struct{ service *agentapi.Service }

func (r releaseCenterReviewer) Review(ctx context.Context, actor publicationworkflow.Actor, documentID string) (releasecenter.AgentReview, error) {
	runID, assessment, err := r.service.ReviewPublication(ctx, agent.Actor{TenantID: actor.TenantID, UserID: "release-center-agent", Role: "admin", Permissions: []string{"agent", "query"}}, documentID)
	if err != nil {
		return releasecenter.AgentReview{RunID: runID}, err
	}
	risk := releasecenter.RiskLow
	if len(assessment.Blockers) > 0 {
		risk = releasecenter.RiskMedium
	}
	return releasecenter.AgentReview{RunID: runID, Status: "completed", Recommendation: map[bool]string{true: "publish", false: "needs_info"}[assessment.Ready], RiskLevel: risk, Summary: strings.Join(assessment.Blockers, ", "), PromptVersion: "document-review-v1"}, nil
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
