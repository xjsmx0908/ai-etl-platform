// Package main is the Query API service: handles file upload and RAG queries with authentication.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"ai-etl-pipeline/internal/agentapi"
	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/circuit"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/es"
	"ai-etl-pipeline/internal/idempotency"
	"ai-etl-pipeline/internal/kafka"
	"ai-etl-pipeline/internal/metrics"
	"ai-etl-pipeline/internal/middleware"
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/prometheus"
	"ai-etl-pipeline/internal/query"
	"ai-etl-pipeline/internal/s3"
	"ai-etl-pipeline/internal/store"
	"ai-etl-pipeline/internal/taskstatus"
	"ai-etl-pipeline/internal/tracing"
)

var (
	version   = "dev"
	buildTime = "unknown"
)

var allowedUploadExtensions = map[string]struct{}{
	".pdf":      {},
	".docx":     {},
	".doc":      {},
	".txt":      {},
	".md":       {},
	".markdown": {},
	".csv":      {},
	".log":      {},
	".rtf":      {},
	".odt":      {},
}

var allowedPermissionLevels = map[string]struct{}{
	"public":       {},
	"internal":     {},
	"confidential": {},
}

type uploadProducer interface {
	Publish(ctx context.Context, task model.Task) error
}

type uploadObjectStore interface {
	Upload(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error
}

// documentObjectStore is the object-store surface document deletion needs: wipe
// every object under tenant/{docID}* (the extension is not known at delete time).
type documentObjectStore interface {
	DeleteByPrefix(ctx context.Context, prefix string) error
}

// uploadDeleteObjectStore is what the upload handler needs: write a new object
// and, for doc_id upsert, wipe the old document's objects first.
type uploadDeleteObjectStore interface {
	documentObjectStore
	Upload(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error
}

type uploadAcceptedResponse struct {
	TaskID    string `json:"task_id"`
	DocID     string `json:"doc_id"`
	Status    string `json:"status"`
	File      string `json:"file"`
	FileHash  string `json:"file_hash"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func requireScopes(requiredScopes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scopes := auth.GetScopes(r.Context())
			for _, required := range requiredScopes {
				if !scopeAllowed(scopes, required) {
					http.Error(w, `{"error":"forbidden","message":"missing scope: `+required+`"}`, http.StatusForbidden)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func scopeAllowed(scopes []string, required string) bool {
	for _, scope := range scopes {
		if scope == required {
			return true
		}
	}
	return false
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func main() {
	cfg := config.Load()
	if err := cfg.ValidateAPI(); err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	metrics.InitLogger(cfg.Environment)
	slog.Info("starting ai-etl-api",
		"version", version, "build_time", buildTime,
		"env", cfg.Environment)

	// Initialize tracing
	tracingShutdown, err := tracing.Init(tracing.Config{
		ServiceName:    "query-api",
		ServiceVersion: version,
		Endpoint:       config.EnvStr("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		SampleRatio:    1.0,
	})
	if err != nil {
		slog.Warn("tracing init failed, continuing without tracing", "error", err)
	}
	defer tracingShutdown()

	// Initialize Prometheus metrics
	prom := prometheus.New("ai_etl")
	circuit.SetStateObserver(prom.SetCircuitState)

	// Initialize auth
	verifier := auth.NewVerifier(cfg.JWTSecret)

	// Initialize MinIO/S3 for file storage
	s3Client, err := s3.New(s3.Config{
		Endpoint:   cfg.S3Endpoint,
		AccessKey:  cfg.S3AccessKey,
		SecretKey:  cfg.S3SecretKey,
		Bucket:     cfg.S3Bucket,
		UseSSL:     cfg.S3UseSSL,
		AutoCreate: true,
	})
	if err != nil {
		slog.Error("failed to create S3 client", "error", err)
		os.Exit(1)
	}

	// Initialize Kafka producer for upload gateway
	producer, err := kafka.NewProducer(cfg.KafkaBrokers, cfg.KafkaTopic)
	if err != nil {
		slog.Error("failed to create kafka producer", "error", err)
		os.Exit(1)
	}
	defer producer.Close()

	// Initialize idempotency store for upload deduplication.
	var idemStore idempotency.Store
	if cfg.IsDev() {
		idemStore = idempotency.NewMemoryStore(cfg.IdempotencyTTL)
	} else {
		redisStore, err := idempotency.NewRedisStore(cfg.RedisStateAddr, cfg.RedisStatePassword, cfg.RedisStateDB, cfg.IdempotencyTTL)
		if err != nil {
			slog.Error("failed to create idempotency store", "error", err)
			os.Exit(1)
		}
		idemStore = redisStore
	}
	defer idemStore.Close()

	// Initialize services
	qs := query.NewServiceWithObserver(cfg, prom)
	taskStatusStore, err := newTaskStatusStore(cfg)
	if err != nil {
		slog.Error("failed to create task status store", "error", err)
		os.Exit(1)
	}
	defer taskStatusStore.Close()

	agentSvc, err := agentapi.NewServiceWithObserver(cfg, qs, taskStatusStore, prom)
	if err != nil {
		slog.Error("failed to create agent api service", "error", err)
		os.Exit(1)
	}
	defer agentSvc.Close()
	rateLimiter := middleware.NewTenantRateLimiter(cfg.EmbedRateLimit, 100)

	// Build HTTP server with enterprise middleware
	mux := http.NewServeMux()

	// Health checks (no auth required)
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/readyz", handleReadyz)
	mux.Handle("/metrics", prom.Handler())
	mux.HandleFunc("/version", handleVersion)

	// API v1 routes (auth required)
	apiV1 := http.NewServeMux()
	apiV1.Handle("/v1/upload", requireScopes("upload")(http.HandlerFunc(handleUpload(cfg, producer, s3Client, idemStore, taskStatusStore))))
	apiV1.Handle("/v1/query", requireScopes("query")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			qs.HandleQueryStreaming(w, r)
			return
		}
		qs.HandleQuery(w, r)
	})))
	apiV1.Handle("/v1/agent/runs", requireScopes("agent", "query")(http.HandlerFunc(agentSvc.HandleRuns)))
	apiV1.Handle("/v1/agent/runs/", requireScopes("agent", "query")(http.HandlerFunc(agentSvc.HandleRun)))
	apiV1.Handle("/v1/documents/", requireScopes("upload")(http.HandlerFunc(handleDeleteDocument(cfg, s3Client))))
	apiV1.Handle("/v1/system/health", requireScopes("query")(http.HandlerFunc(handleSystemHealth(cfg))))
	apiV1.Handle("/v1/tasks/", requireScopes("upload")(http.HandlerFunc(handleTaskStatus(taskStatusStore))))

	// Apply middleware chain: version → JWT auth → rate limit → route scope checks → CORS → timeout.
	// Wrapper execution is outside-in, so compose in reverse.
	handler := http.Handler(apiV1)
	handler = rateLimiter.Middleware(handler)
	handler = verifier.Middleware()(handler)
	handler = middleware.APIVersion("1")(handler)
	handler = middleware.CORS(cfg.CORSAllowedOrigins)(handler)
	handler = middleware.Timeout(60 * time.Second)(handler)
	handler = prom.HTTPMiddleware(handler)
	handler = tracing.HTTPMiddleware("query-api.http")(handler)

	// Mount v1 routes
	mux.Handle("/", handler)

	// Start server
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HealthPort),
		Handler:           mux,
		ReadTimeout:       cfg.HTTPReadTimeout,
		ReadHeaderTimeout: cfg.HTTPReadHeaderTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
		MaxHeaderBytes:    cfg.HTTPMaxHeaderBytes,
	}

	go func() {
		slog.Info("api server started", "port", cfg.HealthPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("api server error", "error", err)
		}
	}()

	// Wait for shutdown signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	received := <-sig
	slog.Info("shutdown signal received", "signal", received.String())

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("api server shutdown failed", "error", err)
	}

	slog.Info("api server shutdown complete")
}

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

func newTaskStatusStore(cfg config.Config) (model.TaskStatusStore, error) {
	if cfg.ResolvedTaskStatusStore() == config.TaskStatusStoreMemory {
		return taskstatus.NewMemoryStore(), nil
	}
	return taskstatus.NewRedisStore(cfg.RedisStateAddr, cfg.RedisStatePassword, cfg.RedisStateDB, cfg.TaskStatusTTL)
}

// cascadeDeleteDoc removes a document and everything derived from it: Qdrant
// points, ES documents, and MinIO objects under tenant/{docID}*. Used by both
// the DELETE API and by upload-upsert (a re-upload with the same doc_id replaces
// the old document). Returns a list of errors (empty means success).
func cascadeDeleteDoc(ctx context.Context, cfg config.Config, s3Client documentObjectStore, tenantID, docID string) []string {
	var errs []string

	qs, err := store.NewQdrantStorer(cfg.StoreEndpoint, cfg.StoreAPIKey, cfg.StoreCollection, cfg.EmbedDimension)
	if err != nil {
		errs = append(errs, "qdrant init: "+err.Error())
	} else {
		if err := qs.DeleteByDocID(ctx, docID); err != nil {
			errs = append(errs, "qdrant: "+err.Error())
		}
		_ = qs.Close()
	}

	idx, err := es.NewHTTPIndexer(cfg.ESAddress, cfg.ESAPIKey, cfg.ESIndex)
	if err != nil {
		errs = append(errs, "es init: "+err.Error())
	} else {
		if err := idx.DeleteByDocID(ctx, docID); err != nil {
			errs = append(errs, "es: "+err.Error())
		}
		_ = idx.Close()
	}

	if s3Client != nil {
		if err := s3Client.DeleteByPrefix(ctx, tenantID+"/"+docID); err != nil {
			errs = append(errs, "s3: "+err.Error())
		}
	}
	return errs
}

// handleDeleteDocument deletes a document and everything derived from it.
func handleDeleteDocument(cfg config.Config, s3Client documentObjectStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		docID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/documents/"), "/")
		if docID == "" {
			http.Error(w, "doc_id is required", http.StatusBadRequest)
			return
		}
		tenantID := auth.GetTenantID(r.Context())
		if tenantID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		errs := cascadeDeleteDoc(r.Context(), cfg, s3Client, tenantID, docID)
		if len(errs) > 0 {
			slog.Error("document delete partial failure", "doc_id", docID, "tenant_id", tenantID, "errors", errs)
			http.Error(w, `{"error":"partial delete failure","details":`+mustJSON(errs)+`}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func mustJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(data)
}

// validDocID restricts doc_id to safe characters so it cannot break the MinIO
// object key (tenant/{docID}{ext}) or URL paths.
func validDocID(docID string) bool {
	if docID == "" {
		return false
	}
	for _, c := range docID {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
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

// handleTaskStatus returns the async processing status for a document, so the
// demo can show the upload → parse → embed → store pipeline live.
func handleTaskStatus(taskStatusStore model.TaskStatusStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		docID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/tasks/"), "/")
		if docID == "" {
			http.Error(w, "doc_id is required", http.StatusBadRequest)
			return
		}
		tenantID := auth.GetTenantID(r.Context())
		if tenantID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		status, ok, err := taskStatusStore.Load(r.Context(), tenantID, docID)
		if err != nil {
			slog.Error("task status load failed", "doc_id", docID, "error", err)
			http.Error(w, `{"error":"status lookup failed"}`, http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, `{"error":"task not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	}
}

func handleUpload(cfg config.Config, producer uploadProducer, s3Client uploadDeleteObjectStore, idemStore idempotency.Store, taskStatusStore model.TaskStatusStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		w = rec
		defer func() {
			if r.MultipartForm != nil {
				if err := r.MultipartForm.RemoveAll(); err != nil {
					slog.Warn("failed to remove multipart temp files", "error", err)
				}
			}
		}()

		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Get tenant from context (set by auth middleware)
		tenantID := auth.GetTenantID(r.Context())
		if tenantID == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Enforce upload body size limit.
		r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxUploadSize)

		// Parse multipart form
		if err := r.ParseMultipartForm(cfg.MultipartMaxMemoryBytes); err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}

		// Get uploaded file
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "file required", http.StatusBadRequest)
			return
		}
		defer file.Close()

		if header.Size <= 0 || header.Size > cfg.MaxUploadSize {
			http.Error(w, "file too large", http.StatusRequestEntityTooLarge)
			return
		}

		ext, err := validateUploadExtension(header.Filename)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
			return
		}

		permission, err := normalizePermission(r.FormValue("permission"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		metadata, err := parseUploadMetadata(r.FormValue("metadata"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		idempotencyKey := readIdempotencyKey(r)
		requestSig := buildUploadRequestSignature(
			tenantID,
			header.Filename,
			header.Size,
			header.Header.Get("Content-Type"),
			permission,
			metadata,
		)
		if idempotencyKey != "" {
			if len(idempotencyKey) > 128 {
				http.Error(w, "idempotency key too long", http.StatusBadRequest)
				return
			}
			res, err := idemStore.Reserve(r.Context(), tenantID, idempotencyKey, requestSig)
			if err != nil {
				slog.Error("idempotency reserve failed", "tenant_id", tenantID, "error", err)
				http.Error(w, "idempotency service unavailable", http.StatusServiceUnavailable)
				return
			}
			reservedNew := res.State == idempotency.ReserveNew
			defer func() {
				// If processing fails after taking a new reservation, release lock for retry.
				if reservedNew && rec.statusCode >= 400 {
					if err := idemStore.Abort(r.Context(), tenantID, idempotencyKey, requestSig); err != nil {
						slog.Warn("idempotency abort failed",
							"tenant_id", tenantID,
							"idempotency_key", idempotencyKey,
							"error", err)
					}
				}
			}()

			switch res.State {
			case idempotency.ReserveReplay:
				if res.Cached != nil {
					w.Header().Set("Content-Type", res.Cached.ContentType)
					w.Header().Set("X-Idempotency-Replayed", "true")
					w.WriteHeader(res.Cached.StatusCode)
					_, _ = w.Write(res.Cached.Body)
					return
				}
			case idempotency.ReserveProcessing:
				http.Error(w, "request with same idempotency key is still processing", http.StatusConflict)
				return
			case idempotency.ReserveConflict:
				http.Error(w, "idempotency key reused with different request payload", http.StatusConflict)
				return
			}
		}

		now := time.Now()
		docID := strings.TrimSpace(r.FormValue("doc_id"))
		userSuppliedDocID := docID != ""
		if docID == "" {
			docID = fmt.Sprintf("doc-%d", now.UnixNano())
		} else if !validDocID(docID) {
			http.Error(w, "invalid doc_id (allowed: letters, digits, . _ -)", http.StatusBadRequest)
			return
		}
		objectKey := fmt.Sprintf("%s/%s%s", tenantID, docID, ext)

		// Upsert semantics: a user-supplied doc_id replaces the previous version,
		// so wipe the old document's vectors, full-text docs, and objects first.
		if userSuppliedDocID {
			if errs := cascadeDeleteDoc(r.Context(), cfg, s3Client, tenantID, docID); len(errs) > 0 {
				slog.Warn("upload upsert pre-delete partial failure", "doc_id", docID, "tenant_id", tenantID, "errors", errs)
			}
		}

		// Upload to S3 while computing SHA-256.
		hasher := sha256.New()
		reader := io.TeeReader(file, hasher)

		// Upload to S3
		if err := s3Client.Upload(r.Context(), objectKey, reader, header.Size, header.Header.Get("Content-Type")); err != nil {
			slog.Error("s3 upload failed", "error", err)
			http.Error(w, "storage failed", http.StatusInternalServerError)
			return
		}
		fileHash := hex.EncodeToString(hasher.Sum(nil))

		// Create and publish task
		task := model.Task{
			FilePath:   objectKey,
			DocID:      docID,
			TenantID:   tenantID,
			Permission: permission,
			FileHash:   fileHash,
			Metadata:   metadata,
			CreatedAt:  now,
		}

		if taskStatusStore != nil {
			if err := taskStatusStore.Save(r.Context(), model.TaskStatus{
				TaskID:     task.DocID,
				DocID:      task.DocID,
				TenantID:   task.TenantID,
				Status:     model.TaskStatusQueued,
				Stage:      "queued",
				FilePath:   task.FilePath,
				FileHash:   task.FileHash,
				Permission: task.Permission,
				Metadata:   task.Metadata,
				CreatedAt:  task.CreatedAt,
				UpdatedAt:  now,
			}); err != nil {
				slog.Error("task status save failed", "tenant_id", tenantID, "doc_id", task.DocID, "error", err)
				http.Error(w, "task status unavailable", http.StatusServiceUnavailable)
				return
			}
		}

		if err := producer.Publish(r.Context(), task); err != nil {
			slog.Error("kafka publish failed", "error", err)
			if taskStatusStore != nil {
				if statusErr := taskStatusStore.Save(r.Context(), model.TaskStatus{
					TaskID:     task.DocID,
					DocID:      task.DocID,
					TenantID:   task.TenantID,
					Status:     model.TaskStatusFailed,
					Stage:      "enqueue",
					Error:      err.Error(),
					FilePath:   task.FilePath,
					FileHash:   task.FileHash,
					Permission: task.Permission,
					Metadata:   task.Metadata,
					CreatedAt:  task.CreatedAt,
					UpdatedAt:  time.Now(),
				}); statusErr != nil {
					slog.Warn("task status failure update failed", "tenant_id", tenantID, "doc_id", task.DocID, "error", statusErr)
				}
			}
			http.Error(w, "enqueue failed", http.StatusInternalServerError)
			return
		}

		resp := uploadAcceptedResponse{
			TaskID:    task.DocID,
			DocID:     task.DocID,
			Status:    "processing",
			File:      objectKey,
			FileHash:  fileHash,
			Message:   fmt.Sprintf("file '%s' accepted, processing in background", header.Filename),
			Timestamp: now.Format(time.RFC3339),
		}
		respBytes, _ := json.Marshal(resp)

		if idempotencyKey != "" {
			if err := idemStore.Complete(r.Context(), tenantID, idempotencyKey, requestSig, idempotency.CachedResponse{
				StatusCode:  http.StatusAccepted,
				ContentType: "application/json",
				Body:        respBytes,
			}); err != nil {
				slog.Warn("idempotency completion failed",
					"tenant_id", tenantID,
					"idempotency_key", idempotencyKey,
					"error", err)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write(respBytes)
	}
}

func validateUploadExtension(filename string) (string, error) {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return "", fmt.Errorf("unsupported file type")
	}
	if _, ok := allowedUploadExtensions[ext]; !ok {
		return "", fmt.Errorf("unsupported file type")
	}
	return ext, nil
}

func normalizePermission(raw string) (string, error) {
	permission := strings.ToLower(strings.TrimSpace(raw))
	if permission == "" {
		return "internal", nil
	}
	if _, ok := allowedPermissionLevels[permission]; !ok {
		return "", fmt.Errorf("invalid permission")
	}
	return permission, nil
}

func readIdempotencyKey(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Idempotency-Key")); v != "" {
		return v
	}
	return strings.TrimSpace(r.Header.Get("Idempotency-Key"))
}

func parseUploadMetadata(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("metadata must be a JSON object with string values")
	}
	clean := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		if len(key) > 64 || len(value) > 512 {
			return nil, fmt.Errorf("metadata key/value too long")
		}
		clean[key] = value
	}
	if len(clean) == 0 {
		return nil, nil
	}
	return clean, nil
}

func buildUploadRequestSignature(tenantID, filename string, size int64, contentType, permission string, metadata map[string]string) string {
	s := fmt.Sprintf("tenant=%s|filename=%s|size=%d|content_type=%s|permission=%s|metadata=%s",
		tenantID,
		strings.ToLower(strings.TrimSpace(filename)),
		size,
		strings.ToLower(strings.TrimSpace(contentType)),
		permission,
		canonicalMetadata(metadata),
	)
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func canonicalMetadata(metadata map[string]string) string {
	if len(metadata) == 0 {
		return ""
	}
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var b strings.Builder
	for i, key := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(strings.ToLower(strings.TrimSpace(key)))
		b.WriteByte('=')
		b.WriteString(strings.TrimSpace(metadata[key]))
	}
	return b.String()
}
