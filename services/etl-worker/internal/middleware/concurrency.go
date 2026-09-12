package middleware

import (
	"net/http"
	"sync"

	"ai-etl-pipeline/internal/auth"
)

// TenantConcurrencyLimiter caps in-flight requests per tenant.
type TenantConcurrencyLimiter struct {
	max      int
	mu       sync.Mutex
	inFlight map[string]int
}

// NewTenantConcurrencyLimiter returns a limiter. max <= 0 disables it.
func NewTenantConcurrencyLimiter(max int) *TenantConcurrencyLimiter {
	return &TenantConcurrencyLimiter{max: max, inFlight: make(map[string]int)}
}

// Middleware holds a slot until the handler returns, including SSE streams.
func (l *TenantConcurrencyLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l == nil || l.max <= 0 {
			next.ServeHTTP(w, r)
			return
		}
		tenantID := auth.GetTenantID(r.Context())
		if tenantID == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if !l.tryAcquire(tenantID) {
			http.Error(w, `{"error":"rate_limited","message":"too many concurrent requests"}`, http.StatusTooManyRequests)
			return
		}
		defer l.release(tenantID)
		next.ServeHTTP(w, r)
	})
}

func (l *TenantConcurrencyLimiter) tryAcquire(tenantID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight[tenantID] >= l.max {
		return false
	}
	l.inFlight[tenantID]++
	return true
}

func (l *TenantConcurrencyLimiter) release(tenantID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight[tenantID] <= 1 {
		delete(l.inFlight, tenantID)
		return
	}
	l.inFlight[tenantID]--
}
