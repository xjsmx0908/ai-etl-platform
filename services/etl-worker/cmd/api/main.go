// Package main is the Query API service: handles file upload and RAG queries with authentication.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/kafka"
	"ai-etl-pipeline/internal/metrics"
	"ai-etl-pipeline/internal/middleware"
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/prometheus"
	"ai-etl-pipeline/internal/query"
	"ai-etl-pipeline/internal/s3"
	"ai-etl-pipeline/internal/tracing"
)

var (
	version   = "dev"
	buildTime = "unknown"
)

func main() {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
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

	// Initialize auth
	jwtSecret := config.EnvStr("JWT_SECRET", "change-me-in-production")
	verifier := auth.NewVerifier(jwtSecret)

	// Initialize MinIO/S3 for file storage
	s3Client, err := s3.New(s3.Config{
		Endpoint:   config.EnvStr("S3_ENDPOINT", "localhost:9000"),
		AccessKey:  config.EnvStr("S3_ACCESS_KEY", "minioadmin"),
		SecretKey:  config.EnvStr("S3_SECRET_KEY", "minioadmin"),
		Bucket:     config.EnvStr("S3_BUCKET", "documents"),
		UseSSL:     config.EnvStr("S3_USE_SSL", "false") == "true",
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

	// Initialize services
	qs := query.NewService(cfg)
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
	apiV1.HandleFunc("/v1/upload", handleUpload(producer, s3Client, jwtSecret))
	apiV1.HandleFunc("/v1/query", qs.HandleQuery)

	// Apply middleware chain: version → auth → rate limit → CORS → timeout
	handler := middleware.APIVersion("1")(apiV1)
	handler = verifier.Middleware("upload", "query")(handler)
	handler = rateLimiter.Middleware(handler)
	handler = middleware.CORS([]string{"*"})(handler)
	handler = middleware.Timeout(60 * time.Second)(handler)

	// Mount v1 routes
	mux.Handle("/", handler)

	// Start server
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.HealthPort),
		Handler: mux,
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

func handleUpload(producer *kafka.Producer, s3Client *s3.Client, jwtSecret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Get tenant from context (set by auth middleware)
		tenantID := auth.GetTenantID(r.Context())

		// Parse multipart form
		if err := r.ParseMultipartForm(32 << 20); err != nil {
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

		// Upload to S3
		objectKey := fmt.Sprintf("%s/%s", tenantID, header.Filename)
		if err := s3Client.Upload(r.Context(), objectKey, file, header.Size, header.Header.Get("Content-Type")); err != nil {
			slog.Error("s3 upload failed", "error", err)
			http.Error(w, "storage failed", http.StatusInternalServerError)
			return
		}

		// Create and publish task
		task := model.Task{
			FilePath:  objectKey,
			DocID:     fmt.Sprintf("doc-%d", time.Now().UnixNano()),
			TenantID:  tenantID,
			CreatedAt: time.Now(),
		}

		if err := producer.Publish(r.Context(), task); err != nil {
			slog.Error("kafka publish failed", "error", err)
			http.Error(w, "enqueue failed", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"task_id": task.DocID,
			"status":  "processing",
			"file":    objectKey,
		})
	}
}
