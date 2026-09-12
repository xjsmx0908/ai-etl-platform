package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"ai-etl-pipeline/internal/auth"
)

func TestTenantConcurrencyLimiterRejectsFifthInFlight(t *testing.T) {
	limiter := NewTenantConcurrencyLimiter(4)
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/v1/query", nil)
			req = req.WithContext(context.WithValue(req.Context(), auth.CtxTenantID, "acme"))
			handler.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for in-flight requests")
		}
	}

	fifth := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/query", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.CtxTenantID, "acme"))
	handler.ServeHTTP(fifth, req)
	if fifth.Code != http.StatusTooManyRequests {
		t.Fatalf("fifth in-flight request status=%d", fifth.Code)
	}

	close(release)
	wg.Wait()

	sixth := httptest.NewRecorder()
	handler.ServeHTTP(sixth, req)
	if sixth.Code != http.StatusOK {
		t.Fatalf("slot should release, status=%d", sixth.Code)
	}
}
