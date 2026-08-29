package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"ai-etl-pipeline/internal/indexmanifest"
)

type rollbackerStub struct {
	requests []indexmanifest.RollbackRequest
	err      error
}

func (s *rollbackerStub) Rollback(_ context.Context, request indexmanifest.RollbackRequest) error {
	s.requests = append(s.requests, request)
	return s.err
}

func TestGenerationRollbackRequiresAdministrator(t *testing.T) {
	rollbacker := &rollbackerStub{}
	handler := handleGenerationRollback(rollbacker, nil, nil, 30*24*time.Hour)
	body := map[string]string{
		"document_id": "doc-1", "document_version_id": "job-1",
		"target_generation_id": "gen-old", "expected_active_generation_id": "gen-current",
	}
	rec := doRequest(handler, http.MethodPost, "/v1/index-generations/rollback", body, ctxWithRole("acme", "user", "query"))
	if rec.Code != http.StatusForbidden || len(rollbacker.requests) != 0 {
		t.Fatalf("status=%d requests=%+v", rec.Code, rollbacker.requests)
	}
}

func TestGenerationRollbackIsTenantScopedAndCompareAndSet(t *testing.T) {
	rollbacker := &rollbackerStub{}
	handler := handleGenerationRollback(rollbacker, nil, nil, 30*24*time.Hour)
	body := map[string]string{
		"document_id": "doc-1", "document_version_id": "job-1",
		"target_generation_id": "gen-old", "expected_active_generation_id": "gen-current",
	}
	rec := doRequest(handler, http.MethodPost, "/v1/index-generations/rollback", body, ctxWithRole("acme", "admin", "admin"))
	if rec.Code != http.StatusOK || len(rollbacker.requests) != 1 {
		t.Fatalf("status=%d body=%s requests=%+v", rec.Code, rec.Body.String(), rollbacker.requests)
	}
	request := rollbacker.requests[0]
	if request.Version.TenantID != "acme" || request.Version.DocumentID != "doc-1" || request.Version.DocumentVersionID != "job-1" ||
		request.TargetGenerationID != "gen-old" || request.ExpectedActiveGenerationID != "gen-current" || request.Window != 30*24*time.Hour {
		t.Fatalf("request=%+v", request)
	}
}

func TestGenerationRollbackMapsStaleWriterToConflict(t *testing.T) {
	rollbacker := &rollbackerStub{err: indexmanifest.ErrConflict}
	handler := handleGenerationRollback(rollbacker, nil, nil, time.Hour)
	body := map[string]string{
		"document_id": "doc-1", "document_version_id": "job-1",
		"target_generation_id": "gen-old", "expected_active_generation_id": "gen-stale",
	}
	rec := doRequest(handler, http.MethodPost, "/v1/index-generations/rollback", body, ctxWithRole("acme", "admin", "admin"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGenerationRollbackFailsClosedWhenDependencyUnavailable(t *testing.T) {
	rollbacker := &rollbackerStub{err: errors.New("qdrant unavailable")}
	handler := handleGenerationRollback(rollbacker, nil, nil, time.Hour)
	body := map[string]string{
		"document_id": "doc-1", "document_version_id": "job-1",
		"target_generation_id": "gen-old", "expected_active_generation_id": "gen-current",
	}
	rec := doRequest(handler, http.MethodPost, "/v1/index-generations/rollback", body, ctxWithRole("acme", "admin", "admin"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
