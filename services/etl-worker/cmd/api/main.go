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
	"mime/multipart"
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

	"ai-etl-pipeline/internal/agent"
	"ai-etl-pipeline/internal/agentapi"
	"ai-etl-pipeline/internal/audit"
	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/circuit"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/deletionworkflow"
	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/es"
	"ai-etl-pipeline/internal/externalidentity"
	"ai-etl-pipeline/internal/idempotency"
	"ai-etl-pipeline/internal/identitylifecycle"
	"ai-etl-pipeline/internal/indexmanifest"
	"ai-etl-pipeline/internal/ingestion"
	"ai-etl-pipeline/internal/kafka"
	"ai-etl-pipeline/internal/knowledgecatalog"
	"ai-etl-pipeline/internal/metrics"
	"ai-etl-pipeline/internal/middleware"
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/oidcauth"
	"ai-etl-pipeline/internal/prometheus"
	"ai-etl-pipeline/internal/publicationrelease"
	"ai-etl-pipeline/internal/publicationworkflow"
	"ai-etl-pipeline/internal/query"
	"ai-etl-pipeline/internal/retrieval"
	"ai-etl-pipeline/internal/s3"
	"ai-etl-pipeline/internal/scim"
	"ai-etl-pipeline/internal/store"
	"ai-etl-pipeline/internal/taskstatus"
	"ai-etl-pipeline/internal/tracing"
	"ai-etl-pipeline/internal/userstore"

	"github.com/google/uuid"
)

var (
	version   = "dev"
	buildTime = "unknown"
)

var allowedUploadExtensions = map[string]struct{}{
	".pdf":      {},
	".docx":     {},
	".doc":      {},
	".xls":      {},
	".xlsx":     {},
	".pptx":     {},
	".txt":      {},
	".md":       {},
	".markdown": {},
	".csv":      {},
	".log":      {},
	".rtf":      {},
	".odt":      {},
	".png":      {},
	".jpg":      {},
	".jpeg":     {},
	".webp":     {},
	".bmp":      {},
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

// objectPrefixPruner is the optional capability upload-upsert needs to wipe a
// document's old objects while sparing the replacement it just wrote. Stores
// that do not implement it simply keep the superseded objects (harmless: they are
// unreferenced once the registry and indexes point at the new version).
type objectPrefixPruner interface {
	DeleteByPrefixExcept(ctx context.Context, prefix, keepKey string) error
}

type exactObjectDeleter interface {
	Delete(ctx context.Context, key string) error
}

// uploadDeleteObjectStore is what the upload handler needs: write a new object
// and, for doc_id upsert, wipe the old document's objects first.
type uploadDeleteObjectStore interface {
	documentObjectStore
	Upload(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error
}

type uploadAcceptedResponse struct {
	TaskID    string `json:"task_id"`
	JobID     string `json:"job_id,omitempty"`
	EventID   string `json:"event_id,omitempty"`
	DocID     string `json:"doc_id"`
	Status    string `json:"status"`
	File      string `json:"file"`
	FileHash  string `json:"file_hash"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
	// DuplicateOf is set when the upload was recognised as byte-identical to an
	// already-indexed document. The upload is then a no-op and this names the
	// existing doc_id. Status is "duplicate" in that case, not "processing".
	DuplicateOf string `json:"duplicate_of,omitempty"`
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

	// Initialize PostgreSQL (users/tenants/document registry) and provision the
	// initial admin on first start. The API owns the registry, so failure here
	// is fatal.
	pgPool, err := openPostgres(context.Background(), cfg)
	if err != nil {
		slog.Error("failed to initialize postgres", "error", err)
		os.Exit(1)
	}
	defer pgPool.Close()
	userStore := userstore.New(pgPool)
	if err := bootstrapAdmin(context.Background(), cfg, userStore); err != nil {
		slog.Error("failed to bootstrap admin", "error", err)
		os.Exit(1)
	}
	docStore := docstore.New(pgPool)
	admissionStore := ingestion.NewPostgresStore(pgPool)
	auditStore := audit.New(pgPool)
	externalIdentityManager := externalidentity.NewPostgresManager(pgPool)
	externalIdentityDirectory := externalidentity.NewPostgresDirectory(pgPool)
	knowledgeCatalog := knowledgecatalog.New(knowledgecatalog.NewPostgresStore(pgPool))
	generationVisibility := indexmanifest.NewPostgresStore(pgPool)
	releaseVisibility := publicationrelease.NewPostgresStore(pgPool)
	deletionStore := deletionworkflow.NewPostgresStore(pgPool)
	var generationRollbacker generationRollbacker

	// Initialize auth. JWT remains the compatibility path. The opt-in personal
	// demo profile additionally validates versioned opaque session credentials;
	// both paths resolve mutable authority from the user store.
	var verifier auth.Authenticator = newPlatformAuthenticator(cfg, userStore)
	sessionManager, err := newPlatformSessionManager(cfg, pgPool)
	if err != nil {
		slog.Error("failed to initialize platform sessions", "error", err)
		os.Exit(1)
	}
	if sessionManager != nil {
		verifier = newSessionCredentialAuthenticator(verifier, sessionManager, userStore)
	}
	var oidcFlow *oidcauth.Flow
	if cfg.OIDCEnabled {
		oidcAuthenticator, oidcErr := oidcauth.New(context.Background(), oidcauth.Config{
			Issuer: cfg.OIDCIssuer, ClientID: cfg.OIDCClientID,
			ClientSecret: cfg.OIDCClientSecret, RedirectURI: cfg.OIDCRedirectURI,
			LogoutRedirectURI: cfg.OIDCLogoutRedirectURI,
		}, externalIdentityDirectory)
		if oidcErr != nil {
			slog.Error("failed to initialize OIDC", "error", oidcErr)
			os.Exit(1)
		}
		var transactionStore oidcauth.TransactionStore
		if cfg.IsDev() {
			transactionStore = oidcauth.NewMemoryTransactionStore()
		} else {
			redisTransactions, transactionErr := oidcauth.NewRedisTransactionStore(
				cfg.RedisStateAddr, cfg.RedisStatePassword, cfg.RedisStateDB, cfg.OIDCClientID,
			)
			if transactionErr != nil {
				slog.Error("failed to initialize OIDC transaction store", "error", transactionErr)
				os.Exit(1)
			}
			defer redisTransactions.Close()
			transactionStore = redisTransactions
		}
		oidcFlow = oidcauth.NewFlow(oidcAuthenticator, transactionStore, cfg.OIDCTransactionTTL)
	}
	var scimHandler http.Handler
	if cfg.SCIMEnabled {
		policy := identitylifecycle.ConnectorPolicy{
			ID: cfg.SCIMConnectorID, TenantID: cfg.SCIMTenantID, Issuer: cfg.SCIMIssuer,
			SubjectAttribute: cfg.SCIMSubjectAttribute, DefaultRole: cfg.SCIMDefaultRole,
		}
		if err := identitylifecycle.EnsureConnector(context.Background(), pgPool, policy); err != nil {
			slog.Error("failed to configure SCIM connector", "error", err)
			os.Exit(1)
		}
		lifecycle := identitylifecycle.NewPostgresProvisioner(pgPool)
		scimHandler, err = scim.NewHandler(scim.Config{
			ConnectorID: cfg.SCIMConnectorID, Issuer: cfg.SCIMIssuer,
			SubjectAttribute: cfg.SCIMSubjectAttribute, BearerTokens: cfg.SCIMBearerTokens,
			MaxBodyBytes: cfg.SCIMMaxBodyBytes,
		}, lifecycle, lifecycle)
		if err != nil {
			slog.Error("failed to initialize SCIM", "error", err)
			os.Exit(1)
		}
	}

	// Opt-in one-shot backfill of the registry from existing Qdrant vectors
	// (legacy data present before PostgreSQL was introduced).
	if cfg.ReconcileDocsOnStartup {
		reconcileDocuments(context.Background(), cfg, docStore)
	}

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
	relayCtx, stopRelay := context.WithCancel(context.Background())
	relayDone := make(chan struct{})
	defer func() {
		stopRelay()
		<-relayDone
	}()
	go func() {
		defer close(relayDone)
		runOutboxRelay(relayCtx, ingestion.Relay{
			Store: admissionStore, Publisher: producer,
			BatchSize: cfg.OutboxRelayBatchSize, Lease: cfg.OutboxRelayLease,
		}, cfg.OutboxRelayPollInterval)
	}()
	metricsCtx, stopIngestionMetrics := context.WithCancel(context.Background())
	metricsDone := make(chan struct{})
	defer func() {
		stopIngestionMetrics()
		<-metricsDone
	}()
	go func() {
		defer close(metricsDone)
		runIngestionOperationsMonitor(metricsCtx, admissionStore, prom, cfg.IngestionMetricsInterval)
	}()
	orphanCtx, stopOrphanCollector := context.WithCancel(context.Background())
	orphanDone := make(chan struct{})
	defer func() {
		stopOrphanCollector()
		<-orphanDone
	}()
	go func() {
		defer close(orphanDone)
		runOrphanCollector(orphanCtx, ingestion.OrphanCollector{
			Objects: s3Client, References: admissionStore,
			GracePeriod: cfg.OrphanCleanupGracePeriod, BatchSize: cfg.OrphanCleanupBatchSize,
		}, cfg.OrphanCleanupInterval)
	}()

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

	// Initialize services. The registry is attached so retrieval can exclude
	// superseded/archived documents from the evidence set and disclose conflicting
	// sources — governance the chunk index cannot express, because chunk payloads
	// are written once at ingest and never updated in place.
	qs := query.NewServiceWithObserver(cfg, prom).
		WithGovernance(docStore).
		WithKnowledgeCatalog(knowledgeCatalog).
		WithReleaseVisibility(releaseVisibility)
	taskStatusStore, err := newTaskStatusStore(cfg)
	if err != nil {
		slog.Error("failed to create task status store", "error", err)
		os.Exit(1)
	}
	defer taskStatusStore.Close()

	if cfg.IndexRetentionWindow > 0 {
		rollbackQdrant, rollbackQdrantErr := store.NewQdrantStorer(cfg.StoreEndpoint, cfg.StoreAPIKey, cfg.StoreCollection, cfg.EmbedDimension)
		rollbackElasticsearch, rollbackElasticsearchErr := es.NewHTTPIndexer(cfg.ESAddress, cfg.ESAPIKey, cfg.ESIndex)
		if rollbackQdrantErr != nil || rollbackElasticsearchErr != nil {
			slog.Warn("generation rollback unavailable", "qdrant_error", rollbackQdrantErr, "elasticsearch_error", rollbackElasticsearchErr)
			if rollbackQdrant != nil {
				_ = rollbackQdrant.Close()
			}
			if rollbackElasticsearch != nil {
				_ = rollbackElasticsearch.Close()
			}
		} else {
			defer rollbackQdrant.Close()
			defer rollbackElasticsearch.Close()
			generationRollbacker = indexmanifest.NewRollbacker(generationVisibility, rollbackQdrant, rollbackElasticsearch).WithObserver(prom)
		}
	}
	exactPublication := publicationworkflow.NewPostgresPublication(pgPool, qs)
	publicationWorkflow := publicationworkflow.New(docStore, exactPublication).
		WithPublisher(exactPublication)
	agentSvc, err := agentapi.NewServiceWithDependencies(cfg, qs, taskStatusStore, prom, agentapi.Dependencies{
		ApprovalStore:       agent.NewPostgresApprovalStore(pgPool),
		PublicationWorkflow: publicationWorkflow,
	})
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

	// Login is unauthenticated. Registering on the outer mux (longest-prefix
	// match beats "/") lets it bypass the JWT middleware chain.
	mux.Handle("/v1/auth/methods", middleware.CORS(cfg.CORSAllowedOrigins)(handleAuthMethods(cfg)))
	mux.Handle("/v1/auth/login", middleware.CORS(cfg.CORSAllowedOrigins)(
		middleware.Timeout(60*time.Second)(http.HandlerFunc(handleLogin(cfg, userStore, auditStore, sessionManager)))))
	mux.Handle("/v1/auth/logout", middleware.CORS(cfg.CORSAllowedOrigins)(
		middleware.Timeout(30*time.Second)(handleLogout(sessionManager, oidcFlow, auditStore))))
	if oidcFlow != nil {
		mux.Handle("/v1/auth/logout/callback", middleware.CORS(cfg.CORSAllowedOrigins)(
			middleware.Timeout(30*time.Second)(handleLogoutCallback(oidcFlow))))
	}
	if oidcFlow != nil {
		mux.Handle("/v1/auth/oidc/start", middleware.CORS(cfg.CORSAllowedOrigins)(
			middleware.Timeout(30*time.Second)(handleOIDCStart(oidcFlow, sessionManager != nil))))
		mux.Handle("/v1/auth/oidc/callback", middleware.CORS(cfg.CORSAllowedOrigins)(
			middleware.Timeout(30*time.Second)(handleOIDCCallback(cfg, oidcFlow, userStore, auditStore, sessionManager))))
	}
	if scimHandler != nil {
		wrappedSCIM := prom.SCIMMiddleware(cfg.SCIMConnectorID, middleware.Timeout(30*time.Second)(scimHandler))
		mux.Handle("/scim/v2/Users", wrappedSCIM)
		mux.Handle("/scim/v2/Users/", wrappedSCIM)
	}

	// API v1 routes (auth required)
	apiV1 := http.NewServeMux()
	apiV1.Handle("/v1/auth/session", handleCurrentSession(userStore))
	apiV1.Handle("/v1/auth/sessions", handleSessionManagement(sessionManager))
	apiV1.Handle("/v1/auth/sessions/{handle}", handleSessionManagement(sessionManager))
	if oidcFlow != nil && sessionManager != nil {
		apiV1.Handle("/v1/auth/reauth/start", handleReauthenticationStart(oidcFlow, sessionManager, userStore, auditStore))
		apiV1.Handle("/v1/auth/reauth/callback", handleReauthenticationCallback(oidcFlow, sessionManager, userStore, auditStore))
	}
	apiV1.Handle("/v1/upload", requireScopes("upload")(http.HandlerFunc(handleUploadWithAdmission(cfg, qs, producer, s3Client, idemStore, taskStatusStore, docStore, auditStore, admissionStore))))
	apiV1.Handle("/v1/query", requireScopes("query")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			qs.HandleQueryStreaming(w, r)
			return
		}
		qs.HandleQuery(w, r)
	})))
	apiV1.Handle("/v1/agent/runs", requireScopes("agent", "query")(http.HandlerFunc(agentSvc.HandleRuns)))
	apiV1.Handle("/v1/agent/runs/", requireScopes("agent", "query")(http.HandlerFunc(agentSvc.HandleRun)))
	// Document registry: list/detail open to any authenticated role (filtered by
	// the role→permission matrix); DELETE checks upload scope in-handler.
	apiV1.Handle("/v1/documents", http.HandlerFunc(handleDocuments(docStore, qs)))
	apiV1.Handle("/v1/knowledge-spaces", http.HandlerFunc(handleKnowledgeSpaces(knowledgeCatalog)))
	apiV1.Handle("/v1/documents/", http.HandlerFunc(handleDocument(cfg, qs, s3Client, docStore, auditStore, deletionStore)))
	// Document content search (ES BM25) and chunk-level detail (Qdrant). Both use
	// long-lived clients: a per-request storer would re-run ensureCollection on
	// every call.
	esRetriever := retrieval.NewElasticRetriever(cfg.ESAddress, cfg.ESAPIKey, cfg.ESIndex, &http.Client{Timeout: 15 * time.Second})
	apiV1.Handle("/v1/documents/search", http.HandlerFunc(handleDocumentSearch(cfg, docStore, esRetriever, qs, releaseVisibility)))
	var chunksHandler http.Handler
	if chunkStorer, err := store.NewQdrantStorer(cfg.StoreEndpoint, cfg.StoreAPIKey, cfg.StoreCollection, cfg.EmbedDimension); err != nil {
		slog.Warn("qdrant storer for chunk detail failed", "error", err)
		chunksHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "vector store unavailable")
		})
	} else {
		defer chunkStorer.Close()
		chunksHandler = http.HandlerFunc(handleDocumentChunks(docStore, chunkStorer, qs))
	}
	apiV1.Handle("/v1/documents/{docID}/chunks", chunksHandler)
	apiV1.Handle("/v1/system/health", requireScopes("query")(http.HandlerFunc(handleSystemHealth(cfg))))
	apiV1.Handle("/v1/tasks/", requireScopes("upload")(http.HandlerFunc(handleTaskStatus(taskStatusStore))))
	apiV1.Handle("/v1/users", requireScopes(auth.ScopeAdmin)(http.HandlerFunc(handleUsers(userStore))))
	apiV1.Handle("/v1/users/{userID}/external-identities", requireScopes(auth.ScopeAdmin)(http.HandlerFunc(handleExternalIdentities(externalIdentityManager))))
	apiV1.Handle("/v1/users/{userID}/external-identities/{bindingID}", requireScopes(auth.ScopeAdmin)(http.HandlerFunc(handleExternalIdentities(externalIdentityManager))))
	apiV1.Handle("/v1/users/", requireScopes(auth.ScopeAdmin)(http.HandlerFunc(handleUser(userStore))))
	apiV1.Handle("/v1/tenants", requireScopes(auth.ScopeAdmin)(http.HandlerFunc(handleTenants(userStore))))
	apiV1.Handle("/v1/audit", requireScopes(auth.ScopeAdmin)(http.HandlerFunc(handleAuditList(auditStore))))
	apiV1.Handle("/v1/index-generations/rollback", requireScopes(auth.ScopeAdmin)(http.HandlerFunc(
		handleGenerationRollback(generationRollbacker, qs, auditStore, cfg.IndexRetentionWindow))))

	// Apply middleware chain: version → JWT auth → rate limit → route scope checks → CORS → timeout.
	// Wrapper execution is outside-in, so compose in reverse.
	handler := http.Handler(apiV1)
	handler = rateLimiter.Middleware(handler)
	handler = auth.Middleware(verifier)(handler)
	handler = middleware.APIVersion("1")(handler)
	handler = middleware.CORS(cfg.CORSAllowedOrigins)(handler)
	handler = middleware.Timeout(cfg.HTTPHandlerTimeout)(handler)
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

func runOutboxRelay(ctx context.Context, relay ingestion.Relay, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := relay.RunOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("ingestion outbox relay pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type ingestionOperationsReader interface {
	OperationsSnapshot(context.Context) (ingestion.OperationsSnapshot, error)
}

type ingestionOperationsObserver interface {
	SetIngestionOperations(int, int, time.Duration, map[string]int, int)
}

func runIngestionOperationsMonitor(ctx context.Context, reader ingestionOperationsReader, observer ingestionOperationsObserver, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		snapshot, err := reader.OperationsSnapshot(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("ingestion operations snapshot failed", "error", err)
			}
		} else {
			observer.SetIngestionOperations(snapshot.PendingOutbox, snapshot.RetriedOutbox,
				snapshot.OldestOutboxAge, snapshot.Jobs, snapshot.ExpiredProcessingLeases)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runOrphanCollector(ctx context.Context, collector ingestion.OrphanCollector, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		deleted, err := collector.RunOnce(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("orphan object collection failed", "error", err)
			}
		} else if deleted > 0 {
			slog.Info("orphan objects collected", "deleted", deleted)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
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
//
// All backends are scoped to tenantID: deleting by doc_id alone would let one
// tenant wipe another tenant's vectors/full-text when doc_ids collide.
func cascadeDeleteDoc(ctx context.Context, cfg config.Config, s3Client documentObjectStore, tenantID, docID string) []string {
	return cascadeDeleteDocExcept(ctx, cfg, s3Client, tenantID, docID, "")
}

// cascadeDeleteDocExcept is cascadeDeleteDoc with one object key spared. Upload
// upsert needs this: it writes the replacement object *before* wiping the old
// version (so a later failure cannot leave the document with neither), and the
// new object shares the tenant/{docID} prefix the wipe would otherwise match.
// keepObjectKey == "" deletes the whole prefix, which is what DELETE wants.
func cascadeDeleteDocExcept(ctx context.Context, cfg config.Config, s3Client documentObjectStore, tenantID, docID, keepObjectKey string) []string {
	var errs []string

	qs, err := store.NewQdrantStorer(cfg.StoreEndpoint, cfg.StoreAPIKey, cfg.StoreCollection, cfg.EmbedDimension)
	if err != nil {
		errs = append(errs, "qdrant init: "+err.Error())
	} else {
		if err := qs.DeleteByDocIDAndTenant(ctx, tenantID, docID); err != nil {
			errs = append(errs, "qdrant: "+err.Error())
		}
		_ = qs.Close()
	}

	idx, err := es.NewHTTPIndexer(cfg.ESAddress, cfg.ESAPIKey, cfg.ESIndex)
	if err != nil {
		errs = append(errs, "es init: "+err.Error())
	} else {
		if err := idx.DeleteByDocIDAndTenant(ctx, tenantID, docID); err != nil {
			errs = append(errs, "es: "+err.Error())
		}
		_ = idx.Close()
	}

	if s3Client != nil {
		if keepObjectKey == "" {
			if err := s3Client.DeleteByPrefix(ctx, tenantID+"/"+docID); err != nil {
				errs = append(errs, "s3: "+err.Error())
			}
		} else if pruner, ok := s3Client.(objectPrefixPruner); ok {
			// Replacement already written: delete every old object under the
			// prefix except the one just uploaded.
			if err := pruner.DeleteByPrefixExcept(ctx, tenantID+"/"+docID, keepObjectKey); err != nil {
				errs = append(errs, "s3: "+err.Error())
			}
		}
	}
	return errs
}

// handleDeleteDocument deletes a document and everything derived from it.
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

func handleUpload(cfg config.Config, qs *query.Service, producer uploadProducer, s3Client uploadDeleteObjectStore, idemStore idempotency.Store, taskStatusStore model.TaskStatusStore, docStore docstore.Store, audits audit.Store) http.HandlerFunc {
	return handleUploadWithAdmission(cfg, qs, producer, s3Client, idemStore, taskStatusStore, docStore, audits, nil)
}

func handleUploadWithAdmission(cfg config.Config, qs *query.Service, producer uploadProducer, s3Client uploadDeleteObjectStore, idemStore idempotency.Store, taskStatusStore model.TaskStatusStore, docStore docstore.Store, audits audit.Store, admissionStore ingestion.Store) http.HandlerFunc {
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

		// Classification is an authorization decision, not a free-form field: a
		// caller may only label a document at a level it is also allowed to read.
		// Without this, a `user` could write confidential docs it can never read
		// back (the read path filters at the source), which is both a privilege
		// escalation and a data-availability trap.
		role := auth.GetPermission(r.Context())
		if !canWritePermission(role, permission) {
			recordAudit(r.Context(), audits, audit.Entry{
				TenantID: tenantID, ActorUserID: auth.GetUserID(r.Context()),
				ActorRole: role,
				Action:    "upload", ResourceType: "document",
				Result: audit.ResultFailure,
				Detail: map[string]any{
					"file_name": header.Filename, "permission": permission,
					"reason": "role may not classify at this permission level",
				},
			})
			http.Error(w, "insufficient role for requested permission", http.StatusForbidden)
			return
		}
		// Controlled-document governance, all optional. Setting these is an
		// editorial act about a document's authority, not about its content, so it
		// is restricted to admins; a normal upload simply omits them and the store
		// layer leaves any existing values untouched.
		governance, err := parseUploadGovernance(r, role)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		metadata, err := parseUploadMetadata(r.FormValue("metadata"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		space, err := qs.ResolveKnowledgeSpace(r.Context(), r.FormValue("knowledge_space_id"), query.AccessContext{
			TenantID: tenantID, UserID: auth.GetUserID(r.Context()), Role: role,
		}, knowledgecatalog.CapabilityUpload)
		if err != nil {
			status := http.StatusServiceUnavailable
			if errors.Is(err, knowledgecatalog.ErrForbidden) || errors.Is(err, knowledgecatalog.ErrNotFound) {
				status = http.StatusForbidden
			}
			http.Error(w, "knowledge space unavailable", status)
			return
		}
		metadata["knowledge_space_id"] = space.ID
		metadata["knowledge_base_id"] = space.ID
		metadata["applicable_scope"] = string(space.Kind)

		idempotencyKey := readIdempotencyKey(r)
		requestSig := buildUploadRequestSignature(
			tenantID,
			header.Filename,
			header.Size,
			header.Header.Get("Content-Type"),
			permission,
			metadata,
		)
		requestSig = extendUploadRequestSignature(requestSig, r.FormValue("doc_id"), governance)
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

		now := time.Now().UTC()
		jobID, eventID := "", ""
		if admissionStore != nil {
			jobID, eventID = admissionIDs(tenantID, idempotencyKey)
		}
		docID := strings.TrimSpace(r.FormValue("doc_id"))
		userSuppliedDocID := docID != ""
		if docID == "" {
			if admissionStore != nil && idempotencyKey != "" {
				docID = "doc-" + jobID
			} else {
				docID = fmt.Sprintf("doc-%d", now.UnixNano())
			}
		} else if !validDocID(docID) {
			http.Error(w, "invalid doc_id (allowed: letters, digits, . _ -)", http.StatusBadRequest)
			return
		}
		objectKey := fmt.Sprintf("%s/%s%s", tenantID, docID, ext)

		// Upsert semantics: a user-supplied doc_id replaces an existing document,
		// which is a destructive act on someone else's data unless we check who
		// owns it. Authorize BEFORE touching any backend.
		replacingExisting := false
		if userSuppliedDocID && docStore != nil {
			existing, found, err := docStore.Get(r.Context(), tenantID, docID)
			if err != nil {
				slog.Error("upsert ownership lookup failed", "tenant_id", tenantID, "doc_id", docID, "error", err)
				http.Error(w, "registry unavailable", http.StatusServiceUnavailable)
				return
			}
			if found {
				replacingExisting = true
				if existing.DeletionStatus == "pending" {
					http.Error(w, "document deletion is pending", http.StatusConflict)
					return
				}
				// The caller must be allowed to read the document it is about to
				// overwrite, and (unless admin) must be the one who uploaded it.
				// Otherwise any user could wipe a colleague's — or an admin's
				// confidential — document by guessing its doc_id.
				//
				// An empty UploadedBy means the row was backfilled by reconciliation
				// and its author is unknown. Unknown ownership is not open ownership:
				// only an admin may replace it.
				ownedByCaller := existing.UploadedBy != "" && existing.UploadedBy == auth.GetUserID(r.Context())
				if !canWritePermission(role, existing.Permission) || (!isAdminRole(role) && !ownedByCaller) {
					// Report the actual reason, not a blanket 403: whether the blocker
					// is "you cannot write this permission level" or "you are not this
					// document's author" changes what the user can do about it.
					reason := "not permitted to replace this document"
					status := http.StatusForbidden
					if !canWritePermission(role, existing.Permission) {
						reason = "existing document permission " + existing.Permission + " exceeds your write scope"
					} else if !isAdminRole(role) && !ownedByCaller {
						reason = "only the document's uploader or an admin may upload a new version"
					}
					recordAudit(r.Context(), audits, audit.Entry{
						TenantID: tenantID, ActorUserID: auth.GetUserID(r.Context()),
						ActorRole: role,
						Action:    "upload", ResourceType: "document", ResourceID: docID,
						Result: audit.ResultFailure,
						Detail: map[string]any{
							"file_name": header.Filename,
							"reason":    reason,
						},
					})
					http.Error(w, reason, status)
					return
				}
				if (existing.Permission != "" && existing.Permission != permission) ||
					(existing.KnowledgeSpaceID != "" && existing.KnowledgeSpaceID != space.ID) {
					http.Error(w, "replacement cannot change document permission or knowledge space", http.StatusConflict)
					return
				}
			}
		}

		// Content hash is computed up front, before anything is written, because
		// exact-duplicate detection has to happen before the upload commits: the
		// point is to NOT create a second copy of identical bytes.
		fileHash, err := hashUpload(file)
		if err != nil {
			slog.Error("hash upload failed", "error", err)
			http.Error(w, "storage failed", http.StatusInternalServerError)
			return
		}
		if admissionStore != nil {
			// The content digest prevents a conflicting retry from overwriting the
			// already admitted immutable object before PostgreSQL rejects it.
			objectKey = fmt.Sprintf("%s/%s/versions/%s-%s-%s%s", tenantID, docID, jobID, fileHash, requestSig, ext)
		}

		// Exact duplicate: the same bytes are already indexed under another doc_id.
		// Ingesting them again would put near-identical chunks into the same
		// candidate set, where they compete for the Top-K slots that should hold
		// distinct evidence. Only checked when the caller did NOT name a doc_id —
		// an explicit doc_id is an explicit intent to replace that document.
		if !userSuppliedDocID && docStore != nil {
			existing, found, err := docStore.GetByHash(r.Context(), tenantID, space.ID, fileHash)
			if err != nil {
				// Detection is an optimisation, not a safety control: on a registry
				// error keep ingesting rather than rejecting a legitimate upload.
				slog.Warn("duplicate lookup failed; proceeding with upload",
					"tenant_id", tenantID, "error", err)
			} else if found && !(admissionStore != nil && idempotencyKey != "" && existing.DocID == docID) {
				writeDuplicateResponse(w, r, existing, header.Filename, now,
					idemStore, idempotencyKey, requestSig, tenantID)
				return
			}
		}

		if err := s3Client.Upload(r.Context(), objectKey, file, header.Size, header.Header.Get("Content-Type")); err != nil {
			slog.Error("s3 upload failed", "error", err)
			http.Error(w, "storage failed", http.StatusInternalServerError)
			return
		}

		// The durable path stores this exact task snapshot in PostgreSQL. The relay
		// publishes it later, so Kafka availability is not part of HTTP admission.
		task := model.Task{
			JobID:      jobID,
			EventID:    eventID,
			FilePath:   objectKey,
			DocID:      docID,
			TenantID:   tenantID,
			Permission: permission,
			FileHash:   fileHash,
			Metadata:   metadata,
			CreatedAt:  now,
		}
		publicationStatus := "draft"
		if replacingExisting {
			// A replacement is not authoritative until the generation activation
			// phase. Preserve the current publication state in the single-row catalog
			// during this transitional slice.
			publicationStatus = ""
		}
		document := docstore.Document{
			TenantID: task.TenantID, DocID: task.DocID, FileName: header.Filename,
			ObjectKey: task.FilePath, FileHash: task.FileHash, FileSize: header.Size,
			ContentType: header.Header.Get("Content-Type"), Permission: task.Permission,
			Status: docstore.StatusQueued, Stage: "queued", Metadata: task.Metadata,
			UploadedBy: auth.GetUserID(r.Context()), CreatedAt: task.CreatedAt, UpdatedAt: now,
			DocStatus: governance.DocStatus, EffectiveDate: governance.EffectiveDate,
			Supersedes: governance.Supersedes, Owner: governance.Owner,
			KnowledgeSpaceID: space.ID, PublicationStatus: publicationStatus,
		}

		if admissionStore != nil {
			receipt, err := admissionStore.Admit(r.Context(), ingestion.Submission{
				JobID: jobID, EventID: eventID, RequestSignature: requestSig + ":" + fileHash,
				Document: document, Task: task,
			})
			if err != nil {
				slog.Error("durable ingestion admission failed", "tenant_id", tenantID, "doc_id", task.DocID, "error", err)
				if deleter, ok := s3Client.(exactObjectDeleter); ok {
					if cleanupErr := deleter.Delete(r.Context(), objectKey); cleanupErr != nil {
						slog.Warn("orphan upload cleanup failed", "object_key", objectKey, "error", cleanupErr)
					}
				}
				if errors.Is(err, ingestion.ErrAdmissionConflict) {
					http.Error(w, "idempotency key reused with different request payload", http.StatusConflict)
				} else {
					http.Error(w, "admission unavailable", http.StatusServiceUnavailable)
				}
				return
			}
			jobID, eventID, docID = receipt.JobID, receipt.EventID, receipt.DocID
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
			}); err != nil && admissionStore == nil {
				slog.Error("task status save failed", "tenant_id", tenantID, "doc_id", task.DocID, "error", err)
				http.Error(w, "task status unavailable", http.StatusServiceUnavailable)
				return
			} else if err != nil {
				slog.Warn("non-authoritative task status save failed", "tenant_id", tenantID, "doc_id", task.DocID, "error", err)
			}
		}

		if admissionStore == nil {
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
		}

		// Replacement is durable (object written, task enqueued), so now retire the
		// previous version's derived data. Doing this *after* the write means a
		// failure above leaves the old document intact rather than deleting it and
		// then failing to ingest the new one — the upload path must never end with
		// neither version present.
		if replacingExisting && admissionStore == nil {
			if errs := cascadeDeleteDocExcept(r.Context(), cfg, s3Client, tenantID, docID, objectKey); len(errs) > 0 {
				// Stale vectors/full-text for the old version may linger; the new
				// version still lands. Reconciliation and the next upsert retry it.
				slog.Warn("upload upsert post-delete partial failure", "doc_id", docID, "tenant_id", tenantID, "errors", errs)
			}
		}

		// Registry write-through: a queued row so the document appears in the
		// inventory immediately. Failure is non-fatal for the upload path — the
		// message is still durable — but is logged for reconciliation.
		if docStore != nil && admissionStore == nil {
			if err := docStore.Upsert(r.Context(), document); err != nil {
				slog.Warn("document registry upsert failed", "tenant_id", task.TenantID, "doc_id", task.DocID, "error", err)
			}
		}

		recordAudit(r.Context(), audits, audit.Entry{
			TenantID: task.TenantID, ActorUserID: auth.GetUserID(r.Context()),
			ActorRole: auth.GetPermission(r.Context()),
			Action:    "upload", ResourceType: "document", ResourceID: task.DocID,
			Result: audit.ResultSuccess,
			Detail: map[string]any{
				"file_name": header.Filename, "size": header.Size, "permission": task.Permission,
			},
		})

		// A write (new or replaced document) can change which sources match a
		// query, so cached answers must not outlive the documents they cite.
		if err := qs.InvalidateSemanticCache(r.Context()); err != nil {
			slog.Warn("semantic cache flush failed after upload", "doc_id", task.DocID, "error", err)
		}

		resp := uploadAcceptedResponse{
			TaskID:    task.DocID,
			JobID:     jobID,
			EventID:   eventID,
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

func admissionIDs(tenantID, idempotencyKey string) (string, string) {
	if idempotencyKey == "" {
		return uuid.NewString(), uuid.NewString()
	}
	seed := tenantID + "/" + idempotencyKey
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("ingestion-job/"+seed)).String(),
		uuid.NewSHA1(uuid.NameSpaceURL, []byte("ingestion-event/"+seed)).String()
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

// hashUpload computes the SHA-256 of the uploaded file and rewinds it so the
// body can still be streamed to object storage afterwards. multipart.File is an
// io.ReadSeeker for both the in-memory and spooled-to-disk cases, so no copy of
// the payload is buffered here.
func hashUpload(file multipart.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind upload: %w", err)
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("hash upload: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind upload after hash: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// writeDuplicateResponse answers an upload whose content already exists in the
// registry. It is a 200 (not an error): from the user's perspective the content
// they wanted in the knowledge base is in the knowledge base, and the response
// points at the document that holds it.
func writeDuplicateResponse(
	w http.ResponseWriter, r *http.Request, existing docstore.Document, filename string,
	now time.Time, idemStore idempotency.Store, idempotencyKey, requestSig, tenantID string,
) {
	resp := uploadAcceptedResponse{
		TaskID:      existing.DocID,
		DocID:       existing.DocID,
		Status:      "duplicate",
		File:        existing.ObjectKey,
		FileHash:    existing.FileHash,
		DuplicateOf: existing.DocID,
		Message: fmt.Sprintf("file '%s' has identical content to existing document '%s'; not ingested again",
			filename, existing.DocID),
		Timestamp: now.Format(time.RFC3339),
	}
	respBytes, _ := json.Marshal(resp)

	if idempotencyKey != "" {
		if err := idemStore.Complete(r.Context(), tenantID, idempotencyKey, requestSig, idempotency.CachedResponse{
			StatusCode:  http.StatusOK,
			ContentType: "application/json",
			Body:        respBytes,
		}); err != nil {
			slog.Warn("idempotency completion failed for duplicate upload",
				"tenant_id", tenantID, "idempotency_key", idempotencyKey, "error", err)
		}
	}

	slog.Info("duplicate upload short-circuited",
		"tenant_id", tenantID, "doc_id", existing.DocID, "file_name", filename)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBytes)
}

// isAdminRole reports whether a role is the tenant administrator, which may
// replace documents it did not upload.
func isAdminRole(role string) bool {
	return strings.EqualFold(strings.TrimSpace(role), "admin")
}

// canWritePermission reports whether a role may classify (or replace) a
// document at the given permission level. Write scope mirrors read scope
// deliberately — query.AllowedPermissionsForRole is the single source of truth —
// so a caller can never create a document it is not allowed to retrieve.
func canWritePermission(role, permission string) bool {
	for _, allowed := range query.AllowedPermissionsForRole(role) {
		if allowed == permission {
			return true
		}
	}
	return false
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
		// Return an empty map, not nil: the registry column is NOT NULL and a
		// nil slice would violate it on uploads without a metadata field.
		return map[string]string{}, nil
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
		return map[string]string{}, nil
	}
	return clean, nil
}

func applyUploadScope(metadata map[string]string, knowledgeBaseID, applicableScope, role string) (map[string]string, error) {
	if metadata == nil {
		metadata = map[string]string{}
	}
	knowledgeBaseID = strings.TrimSpace(knowledgeBaseID)
	customScopeSupplied := knowledgeBaseID != ""
	if knowledgeBaseID == "" {
		knowledgeBaseID = strings.TrimSpace(metadata["knowledge_base_id"])
		customScopeSupplied = knowledgeBaseID != ""
	}
	if knowledgeBaseID == "" {
		knowledgeBaseID = "user-uploads"
	}
	applicableScope = strings.TrimSpace(applicableScope)
	customScopeSupplied = customScopeSupplied || applicableScope != ""
	if applicableScope == "" {
		applicableScope = strings.TrimSpace(metadata["applicable_scope"])
		customScopeSupplied = customScopeSupplied || applicableScope != ""
	}
	if applicableScope == "" {
		applicableScope = "organization"
	}
	if len(knowledgeBaseID) > 64 || len(applicableScope) > 128 {
		return nil, fmt.Errorf("knowledge_base_id/applicable_scope too long")
	}
	if customScopeSupplied && !isAdminRole(role) {
		return nil, fmt.Errorf("knowledge_base_id/applicable_scope require admin role")
	}
	if !validScopeIdentifier(knowledgeBaseID) {
		return nil, fmt.Errorf("invalid knowledge_base_id (allowed: letters, digits, . _ -)")
	}
	metadata["knowledge_base_id"] = knowledgeBaseID
	metadata["applicable_scope"] = applicableScope
	return metadata, nil
}

func validScopeIdentifier(value string) bool {
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return value != ""
}

// uploadGovernance carries the optional controlled-document fields of an upload.
// Zero values mean "not supplied", which the registry treats as "leave whatever
// is already there" rather than as a clearing write.
type uploadGovernance struct {
	DocStatus     string
	EffectiveDate time.Time
	Supersedes    string
	Owner         string
}

// parseUploadGovernance reads the governance form fields and validates them.
//
// These fields decide whether a document counts as authoritative evidence, which
// is a stronger power than uploading content: marking a document superseded
// removes it from every future answer. So writing them requires admin, and a
// non-admin supplying any of them is rejected outright rather than silently
// ignored — silently dropping a caller's obsolescence marking would leave them
// believing a document was retired when it was not.
func parseUploadGovernance(r *http.Request, role string) (uploadGovernance, error) {
	g := uploadGovernance{
		DocStatus:  strings.TrimSpace(r.FormValue("doc_status")),
		Supersedes: strings.TrimSpace(r.FormValue("supersedes")),
		Owner:      strings.TrimSpace(r.FormValue("owner")),
	}
	rawDate := strings.TrimSpace(r.FormValue("effective_date"))

	supplied := g.DocStatus != "" || g.Supersedes != "" || g.Owner != "" || rawDate != ""
	if !supplied {
		return uploadGovernance{}, nil
	}
	if !isAdminRole(role) {
		return uploadGovernance{}, fmt.Errorf("governance fields (doc_status/effective_date/supersedes/owner) require admin role")
	}

	switch g.DocStatus {
	case "", docstore.DocStatusActive, docstore.DocStatusSuperseded, docstore.DocStatusArchived:
	default:
		return uploadGovernance{}, fmt.Errorf("doc_status must be one of active, superseded, archived")
	}
	if rawDate != "" {
		// DATE column, so a date-only layout: accepting a timestamp would imply a
		// precision the registry does not store.
		parsed, err := time.Parse("2006-01-02", rawDate)
		if err != nil {
			return uploadGovernance{}, fmt.Errorf("effective_date must be YYYY-MM-DD")
		}
		g.EffectiveDate = parsed
	}
	if len(g.Supersedes) > 256 || len(g.Owner) > 256 {
		return uploadGovernance{}, fmt.Errorf("supersedes/owner too long")
	}
	// A document superseding itself would make the conflict detector report a
	// document as conflicting with itself.
	if g.Supersedes != "" && g.Supersedes == strings.TrimSpace(r.FormValue("doc_id")) {
		return uploadGovernance{}, fmt.Errorf("supersedes must not reference the document itself")
	}
	return g, nil
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

func extendUploadRequestSignature(base, docID string, governance uploadGovernance) string {
	s := fmt.Sprintf("%s|doc_id=%s|doc_status=%s|effective_date=%s|supersedes=%s|owner=%s",
		base, strings.TrimSpace(docID), governance.DocStatus,
		governance.EffectiveDate.Format("2006-01-02"), governance.Supersedes, governance.Owner)
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
