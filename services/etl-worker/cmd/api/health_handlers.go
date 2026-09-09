package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"ai-etl-pipeline/internal/config"
)

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

func handleReadyz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ready")
}

func handleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"version":    version,
		"build_time": buildTime,
		"service":    "query-api",
	})
}

// serviceCheck describes one dependency to probe for system health.
type serviceCheck struct {
	name string
	url  string // base URL
	kind string // "http" | "redis" | "self"
	path string // health path (default /healthz)
}

type serviceHealthResult struct {
	Status    string `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
}

// handleSystemHealth aggregates dependency health for the demo observability
// panel. Each service is probed concurrently with a short timeout.
func handleSystemHealth(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		checks := []serviceCheck{
			{name: "query-api", kind: "self"},
			{name: "etl-worker", url: cfg.WorkerHealthURL, kind: "http"},
			{name: "parser-service", url: cfg.ParserEndpoint, kind: "http"},
			{name: "qdrant", url: cfg.StoreEndpoint, kind: "http"},
			{name: "elasticsearch", url: cfg.ESAddress, kind: "http", path: "/_cluster/health"},
			{name: "redis-cache", url: cfg.RedisCacheAddr, kind: "redis"},
			{name: "redis-state", url: cfg.RedisStateAddr, kind: "redis"},
		}

		results := make(map[string]serviceHealthResult, len(checks))
		var mu sync.Mutex
		var wg sync.WaitGroup
		client := &http.Client{Timeout: 2 * time.Second}

		for _, check := range checks {
			wg.Add(1)
			go func(c serviceCheck) {
				defer wg.Done()
				start := time.Now()
				status := probeService(r.Context(), client, c)
				mu.Lock()
				results[c.name] = serviceHealthResult{Status: status, LatencyMS: time.Since(start).Milliseconds()}
				mu.Unlock()
			}(check)
		}
		wg.Wait()

		overall := "up"
		for _, res := range results {
			if res.Status != "up" {
				overall = "degraded"
				break
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"overall":  overall,
			"services": results,
		})
	}
}

func probeService(ctx context.Context, client *http.Client, check serviceCheck) string {
	switch check.kind {
	case "self":
		return "up"
	case "redis":
		return probeTCP(ctx, check.url)
	default:
		return probeHTTP(ctx, client, check)
	}
}

func probeHTTP(ctx context.Context, client *http.Client, check serviceCheck) string {
	base := strings.TrimRight(strings.TrimSpace(check.url), "/")
	path := check.path
	if path == "" {
		path = "/healthz"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return "down"
	}
	resp, err := client.Do(req)
	if err != nil {
		return "down"
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return "up"
	}
	return "degraded"
}

func probeTCP(ctx context.Context, addr string) string {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "down"
	}
	_ = conn.Close()
	return "up"
}
