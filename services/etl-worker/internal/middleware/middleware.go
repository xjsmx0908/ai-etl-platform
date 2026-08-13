// Package middleware provides HTTP middleware for rate limiting and API versioning.
package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"ai-etl-pipeline/internal/auth"
)

// TenantRateLimiter provides per-tenant rate limiting.
type TenantRateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
	rate     float64
	burst    int
}

// NewTenantRateLimiter creates a per-tenant rate limiter.
func NewTenantRateLimiter(rps float64, burst int) *TenantRateLimiter {
	return &TenantRateLimiter{
		limiters: make(map[string]*rate.Limiter),
		rate:     rps,
		burst:    burst,
	}
}

func (rl *TenantRateLimiter) getLimiter(tenantID string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	lim, ok := rl.limiters[tenantID]
	if !ok {
		lim = rate.NewLimiter(rate.Limit(rl.rate), rl.burst)
		rl.limiters[tenantID] = lim
	}
	return lim
}

// Middleware returns an HTTP middleware that enforces per-tenant rate limits.
func (rl *TenantRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID := auth.GetTenantID(r.Context())
		if tenantID == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}

		lim := rl.getLimiter(tenantID)
		if !lim.Allow() {
			http.Error(w, `{"error":"rate_limited","message":"too many requests"}`, http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// APIVersion returns a middleware that enforces API version prefix.
func APIVersion(version string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, "/v"+version) &&
				!strings.HasPrefix(r.URL.Path, "/healthz") &&
				!strings.HasPrefix(r.URL.Path, "/readyz") &&
				!strings.HasPrefix(r.URL.Path, "/metrics") {
				http.Error(w, `{"error":"invalid_version","message":"use /v`+version+`/..."}`, http.StatusBadRequest)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout returns a middleware that adds request timeout.
func Timeout(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// http.TimeoutHandler buffers the response and drops http.Flusher,
			// which breaks SSE. Streams are excluded; they carry their own
			// context timeout via the upstream LLM call.
			if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
				next.ServeHTTP(w, r)
				return
			}
			http.TimeoutHandler(next, timeout, `{"error":"timeout"}`).ServeHTTP(w, r)
		})
	}
}

// CORS returns a middleware that adds CORS headers.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			allowAll := false
			allowed := false

			for _, v := range allowedOrigins {
				if v == "*" {
					allowAll = true
					allowed = true
					break
				}
				if origin != "" && v == origin {
					allowed = true
				}
			}

			if origin != "" && allowed {
				if allowAll {
					w.Header().Set("Access-Control-Allow-Origin", "*")
				} else {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
				}
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Idempotency-Key")
				w.Header().Set("Access-Control-Expose-Headers", "X-Trace-ID")
				w.Header().Set("Access-Control-Max-Age", "86400")
			}

			if r.Method == http.MethodOptions {
				if origin != "" && !allowed {
					http.Error(w, `{"error":"cors_forbidden"}`, http.StatusForbidden)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
