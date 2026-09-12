package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-etl-pipeline/internal/middleware"
	"ai-etl-pipeline/internal/prometheus"
)

func TestMetricsRequiresBearerToken(t *testing.T) {
	prom := prometheus.New("test_query_api_metrics")
	handler := middleware.RequireBearerToken("metrics-secret")(prom.Handler())

	unauth := httptest.NewRecorder()
	handler.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d", unauth.Code)
	}

	wrong := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer nope")
	handler.ServeHTTP(wrong, req)
	if wrong.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token status=%d", wrong.Code)
	}

	ok := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer metrics-secret")
	handler.ServeHTTP(ok, req)
	if ok.Code != http.StatusOK {
		t.Fatalf("valid token status=%d body=%s", ok.Code, ok.Body.String())
	}
}
