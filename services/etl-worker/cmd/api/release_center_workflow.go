package main

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"ai-etl-pipeline/internal/notification"
	"ai-etl-pipeline/internal/publicationworkflow"
	"ai-etl-pipeline/internal/releasecenter"
	"ai-etl-pipeline/internal/userstore"
)

type workflowDecisionRequest struct {
	TenantID    string `json:"tenant_id"`
	RequestID   string `json:"request_id"`
	ActorUserID string `json:"actor_user_id"`
	Decision    string `json:"decision"`
	Reason      string `json:"reason,omitempty"`
}

func handleReleaseCenterWorkflowDecision(
	store releasecenter.Store,
	workflow releasecenter.PublicationWorkflow,
	users userstore.Store,
	notifier notification.Enqueuer,
	callbackToken string,
	policies ...releasecenter.ApprovalPolicyStore,
) http.HandlerFunc {
	approval := releasecenter.NewApprovalService(workflow, store, policies...).WithNotifier(notifier)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if strings.TrimSpace(callbackToken) == "" {
			writeError(w, http.StatusServiceUnavailable, "workflow callback is not configured")
			return
		}
		if !workflowCallbackAuthorized(bearerCredential(r.Header.Get("Authorization")), callbackToken) {
			writeError(w, http.StatusUnauthorized, "invalid workflow callback token")
			return
		}
		var body workflowDecisionRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		body.TenantID = strings.TrimSpace(body.TenantID)
		body.RequestID = strings.TrimSpace(body.RequestID)
		body.ActorUserID = strings.TrimSpace(body.ActorUserID)
		if body.TenantID == "" || body.RequestID == "" || body.ActorUserID == "" || strings.TrimSpace(body.Decision) == "" {
			writeError(w, http.StatusBadRequest, "tenant_id, request_id, actor_user_id, and decision are required")
			return
		}
		actor, err := resolveWorkflowActor(r.Context(), users, body.TenantID, body.ActorUserID)
		if err != nil {
			writeWorkflowActorError(w, err)
			return
		}
		result, err := approval.Decide(r.Context(), actor, body.RequestID, body.Decision, body.Reason)
		if err != nil {
			writeWorkflowDecisionError(w, err)
			return
		}
		review, err := store.GetReview(r.Context(), actor.TenantID, result.Request.ReviewID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"request": result.Request, "review": review, "decisions": result.Decisions})
	}
}

func bearerCredential(header string) string {
	header = strings.TrimSpace(header)
	if len(header) < 7 || !strings.EqualFold(header[:7], "bearer ") {
		return ""
	}
	return strings.TrimSpace(header[7:])
}

func workflowCallbackAuthorized(provided, expected string) bool {
	if expected == "" || provided == "" {
		return false
	}
	return hmac.Equal([]byte(provided), []byte(expected))
}

var (
	errWorkflowDirectoryUnavailable = errors.New("workflow actor directory is not configured")
	errWorkflowActorNotFound        = errors.New("workflow actor was not found")
	errWorkflowActorInactive        = errors.New("workflow actor is not an active tenant administrator")
)

func resolveWorkflowActor(ctx context.Context, users userstore.Store, tenantID, actorRef string) (publicationworkflow.Actor, error) {
	if users == nil {
		return publicationworkflow.Actor{}, errWorkflowDirectoryUnavailable
	}
	user, found, err := users.GetByID(ctx, actorRef)
	if err != nil {
		return publicationworkflow.Actor{}, err
	}
	if !found {
		user, found, err = users.GetByUsername(ctx, actorRef)
		if err != nil {
			return publicationworkflow.Actor{}, err
		}
	}
	if !found {
		return publicationworkflow.Actor{}, errWorkflowActorNotFound
	}
	if !user.Active || user.TenantID != tenantID || !strings.EqualFold(user.Role, userstore.RoleAdmin) {
		return publicationworkflow.Actor{}, errWorkflowActorInactive
	}
	return publicationworkflow.Actor{TenantID: user.TenantID, UserID: user.ID, Role: user.Role}, nil
}

func writeWorkflowActorError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errWorkflowDirectoryUnavailable):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, errWorkflowActorNotFound), errors.Is(err, errWorkflowActorInactive):
		writeError(w, http.StatusForbidden, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func writeWorkflowDecisionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, releasecenter.ErrRequestNotFound), errors.Is(err, releasecenter.ErrReviewNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, publicationworkflow.ErrAdminRequired), errors.Is(err, releasecenter.ErrApprovalForbidden):
		writeError(w, http.StatusForbidden, err.Error())
	default:
		writeError(w, http.StatusConflict, err.Error())
	}
}
