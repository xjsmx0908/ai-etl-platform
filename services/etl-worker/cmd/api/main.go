// Package main is the Query API service: handles file upload and RAG queries with authentication.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
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
	"ai-etl-pipeline/internal/notification"
	"ai-etl-pipeline/internal/oidcauth"
	"ai-etl-pipeline/internal/prometheus"
	"ai-etl-pipeline/internal/publicationrelease"
	"ai-etl-pipeline/internal/publicationworkflow"
	"ai-etl-pipeline/internal/query"
	"ai-etl-pipeline/internal/releasecenter"
	"ai-etl-pipeline/internal/retrieval"
	"ai-etl-pipeline/internal/s3"
	"ai-etl-pipeline/internal/scim"
	"ai-etl-pipeline/internal/store"
	"ai-etl-pipeline/internal/tracing"
	"ai-etl-pipeline/internal/userstore"
)

var (
	version   = "dev"
	buildTime = "unknown"
)

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

func newLoginGuard(cfg config.Config) middleware.LoginGuard {
	guardCfg := middleware.LoginGuardConfig{
		PerIP:         cfg.LoginRateLimitPerIP,
		PerUser:       cfg.LoginRateLimitPerUser,
		LockThreshold: cfg.LoginLockThreshold,
		LockDuration:  cfg.LoginLockDuration,
	}
	if cfg.UseRedisLoginGuard() {
		guard, err := middleware.NewRedisLoginGuard(cfg.RedisStateAddr, cfg.RedisStatePassword, cfg.RedisStateDB, guardCfg)
		if err != nil {
			if !cfg.IsDev() && cfg.APIReplicas > 1 {
				slog.Error("redis login guard unavailable", "error", err)
				os.Exit(1)
			}
			slog.Warn("falling back to memory login guard", "error", err)
		} else {
			return guard
		}
	}
	return middleware.NewMemoryLoginGuard(guardCfg)
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
	if err := ensureDemoAccounts(context.Background(), cfg, userStore); err != nil {
		slog.Error("failed to ensure demo accounts", "error", err)
		os.Exit(1)
	}
	if err := ensureDemoShowcase(context.Background(), cfg, pgPool); err != nil {
		slog.Error("failed to ensure demo showcase", "error", err)
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
	producer, err := kafka.NewTopicRouter(cfg.KafkaBrokers, cfg.KafkaTopic, cfg.OCRKafkaTopic)
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
		WithDocuments(docStore).
		WithKnowledgeCatalog(knowledgeCatalog).
		WithReleaseVisibility(releaseVisibility).
		WithSensitiveAnswerHook(func(ctx context.Context, access query.AccessContext, _ string) {
			recordAudit(ctx, auditStore, audit.Entry{
				TenantID: access.TenantID, ActorUserID: access.UserID, ActorRole: access.Role,
				Action: "query_sensitive_answer_blocked", Result: audit.ResultFailure,
				Detail: map[string]any{"reason": "sensitive_data_detected"},
			})
		})
	go func() {
		warmCtx, warmCancel := context.WithTimeout(context.Background(), cfg.EmbedTimeout)
		defer warmCancel()
		if err := qs.WarmEmbeddings(warmCtx); err != nil {
			slog.Warn("query embedding warmup failed", "model", cfg.EmbedModel, "error", err)
			return
		}
		slog.Info("query embedding model kept warm", "model", cfg.EmbedModel, "keep_alive", cfg.EmbedKeepAlive)
	}()
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
	releaseCenterStore := releasecenter.NewPostgresStore(pgPool)
	notificationStore := notification.NewPostgresStore(pgPool)
	var notificationDispatcher notification.Dispatcher = notification.SkipDispatcher{}
	if strings.TrimSpace(cfg.NotificationWebhookURL) != "" {
		notificationDispatcher = notification.WebhookDispatcher{
			URL: cfg.NotificationWebhookURL, Token: cfg.NotificationWebhookToken, Timeout: cfg.NotificationWebhookTimeout,
		}
	}
	go func() {
		runNotificationRelay(relayCtx, notification.Relay{
			Store: notificationStore, Dispatcher: notificationDispatcher,
			BatchSize: cfg.OutboxRelayBatchSize, Lease: cfg.OutboxRelayLease,
		}, cfg.OutboxRelayPollInterval)
	}()
	go func() {
		runNotificationOperationsMonitor(metricsCtx, notificationStore, prom, cfg.IngestionMetricsInterval)
	}()
	var chunkStorerForReview *store.QdrantStorer
	if chunkStorer, chunkErr := store.NewQdrantStorer(cfg.StoreEndpoint, cfg.StoreAPIKey, cfg.StoreCollection, cfg.EmbedDimension); chunkErr != nil {
		slog.Warn("qdrant storer for review Agent unavailable", "error", chunkErr)
	} else {
		chunkStorerForReview = chunkStorer
		defer chunkStorer.Close()
	}
	agentSvc, err := agentapi.NewServiceWithDependencies(cfg, qs, taskStatusStore, prom, agentapi.Dependencies{
		ApprovalStore:       agent.NewPostgresApprovalStore(pgPool),
		PublicationWorkflow: publicationWorkflow,
		ReviewDocuments:     docStore,
		ReviewChunks:        chunkStorerForReview,
		ReviewSpaces:        knowledgeCatalog,
	})
	if err != nil {
		slog.Error("failed to create agent api service", "error", err)
		os.Exit(1)
	}
	// releaseCoordinator is wired after the long-lived chunk reader below.
	defer agentSvc.Close()
	rateLimiter := middleware.NewTenantRateLimiter(cfg.EmbedRateLimit, 100)
	loginGuard := newLoginGuard(cfg)
	queryConcurrency := middleware.NewTenantConcurrencyLimiter(cfg.QueryMaxConcurrency)
	uploadConcurrency := middleware.NewTenantConcurrencyLimiter(cfg.UploadMaxConcurrency)
	agentConcurrency := middleware.NewTenantConcurrencyLimiter(cfg.AgentMaxConcurrency)

	// Build HTTP server with enterprise middleware
	mux := http.NewServeMux()

	// Health checks stay anonymous and must not leak config or versions.
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/readyz", handleReadyz)
	mux.Handle("/metrics", middleware.RequireBearerToken(cfg.MetricsToken)(prom.Handler()))
	mux.Handle("/version", middleware.RequireBearerToken(cfg.MetricsToken)(http.HandlerFunc(handleVersion)))

	// Login is unauthenticated. Registering on the outer mux (longest-prefix
	// match beats "/") lets it bypass the JWT middleware chain.
	mux.Handle("/v1/auth/methods", middleware.CORS(cfg.CORSAllowedOrigins)(handleAuthMethods(cfg)))
	mux.Handle("/v1/auth/login", middleware.CORS(cfg.CORSAllowedOrigins)(
		middleware.Timeout(60*time.Second)(handleLoginWithGuard(cfg, userStore, auditStore, sessionManager, loginGuard))))
	mux.Handle("/v1/auth/demo-login", middleware.CORS(cfg.CORSAllowedOrigins)(
		middleware.Timeout(60*time.Second)(handleDemoLoginWithGuard(cfg, userStore, auditStore, sessionManager, loginGuard))))
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

	// External workflow engines authenticate with a service token, not a user JWT.
	mux.Handle("/v1/release-center/workflow/decision", middleware.CORS(cfg.CORSAllowedOrigins)(
		middleware.Timeout(30*time.Second)(handleReleaseCenterWorkflowDecision(
			releaseCenterStore, publicationWorkflow, userStore, notificationStore, cfg.WorkflowCallbackToken, releaseCenterStore,
		))))

	// API v1 routes (auth required)
	apiV1 := http.NewServeMux()
	apiV1.Handle("/v1/auth/session", handleCurrentSession(userStore))
	apiV1.Handle("/v1/auth/sessions", handleSessionManagement(sessionManager))
	apiV1.Handle("/v1/auth/sessions/{handle}", handleSessionManagement(sessionManager))
	if oidcFlow != nil && sessionManager != nil {
		apiV1.Handle("/v1/auth/reauth/start", handleReauthenticationStart(oidcFlow, sessionManager, userStore, auditStore))
		apiV1.Handle("/v1/auth/reauth/callback", handleReauthenticationCallback(oidcFlow, sessionManager, userStore, auditStore))
	}
	apiV1.Handle("/v1/upload", requireScopes("upload")(uploadConcurrency.Middleware(http.HandlerFunc(handleUploadWithAdmission(cfg, qs, producer, s3Client, idemStore, taskStatusStore, docStore, auditStore, admissionStore)))))
	apiV1.Handle("/v1/query", requireScopes("query")(queryConcurrency.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			qs.HandleQueryStreaming(w, r)
			return
		}
		qs.HandleQuery(w, r)
	}))))
	apiV1.Handle("/v1/agent/runs", requireScopes("agent", "query")(agentConcurrency.Middleware(http.HandlerFunc(agentSvc.HandleRuns))))
	apiV1.Handle("/v1/agent/runs/", requireScopes("agent", "query")(http.HandlerFunc(agentSvc.HandleRun)))
	apiV1.Handle("/v1/release-center/requests", requireScopes(auth.ScopeAdmin)(handleReleaseCenterRequests(releaseCenterStore)))
	apiV1.Handle("/v1/release-center/overview", requireScopes(auth.ScopeAdmin)(handleReleaseCenterOverview(releaseCenterStore)))
	apiV1.Handle("/v1/release-center/requests/", requireScopes(auth.ScopeAdmin)(handleReleaseCenterDecision(releaseCenterStore, publicationWorkflow, notificationStore, releaseCenterStore)))
	apiV1.Handle("/v1/release-center/approval-groups", requireScopes(auth.ScopeAdmin)(handleReleaseCenterApprovalGroups(releaseCenterStore)))
	apiV1.Handle("/v1/release-center/approval-groups/", requireScopes(auth.ScopeAdmin)(handleReleaseCenterApprovalGroups(releaseCenterStore)))
	apiV1.Handle("/v1/release-center/approval-policies", requireScopes(auth.ScopeAdmin)(handleReleaseCenterApprovalPolicies(releaseCenterStore)))
	// Document registry: list/detail open to any authenticated role (filtered by
	// the role→permission matrix); DELETE checks upload scope in-handler.
	apiV1.Handle("/v1/documents", http.HandlerFunc(handleDocuments(docStore, qs)))
	apiV1.Handle("/v1/knowledge-spaces", http.HandlerFunc(handleKnowledgeSpaces(knowledgeCatalog)))
	apiV1.Handle("/v1/knowledge-spaces/", http.HandlerFunc(handleKnowledgeSpace(knowledgeCatalog)))
	apiV1.Handle("/v1/documents/", http.HandlerFunc(handleDocument(cfg, qs, s3Client, docStore, auditStore, deletionStore)))
	// Document content search (ES BM25) and chunk-level detail (Qdrant). Both use
	// long-lived clients: a per-request storer would re-run ensureCollection on
	// every call.
	esRetriever := retrieval.NewElasticRetriever(cfg.ESAddress, cfg.ESAPIKey, cfg.ESIndex, &http.Client{Timeout: 15 * time.Second})
	apiV1.Handle("/v1/documents/search", http.HandlerFunc(handleDocumentSearch(cfg, docStore, esRetriever, qs, releaseVisibility)))
	var chunksHandler http.Handler
	if chunkStorerForHandler := chunkStorerForReview; chunkStorerForHandler == nil {
		slog.Warn("qdrant storer for chunk detail unavailable")
		chunksHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "vector store unavailable")
		})
	} else {
		chunksHandler = http.HandlerFunc(handleDocumentChunks(docStore, chunkStorerForHandler, qs, releaseVisibility))
	}
	releaseCoordinator := releasecenter.NewCoordinator(publicationWorkflow, docStore, releaseCenterReviewer{service: agentSvc}, releaseCenterStore, releaseCenterStore).WithNotifier(notificationStore).WithReviewTTL(cfg.ReleaseReviewTTL).WithReviewRetention(cfg.ReleaseReviewRetention)
	go runReleaseReviewCollector(relayCtx, releaseCoordinator, 5*time.Second)
	apiV1.Handle("/v1/release-center/reviews/", requireScopes(auth.ScopeAdmin)(handleReleaseCenterReview(releaseCoordinator)))
	apiV1.Handle("/v1/release-center/review-reports/", requireScopes(auth.ScopeAdmin)(handleReleaseCenterReviewReport(releaseCenterStore)))
	apiV1.Handle("/v1/documents/{docID}/chunks", chunksHandler)
	apiV1.Handle("/v1/system/health", requireScopes("query")(http.HandlerFunc(handleSystemHealth(cfg))))
	apiV1.Handle("/v1/tasks/", requireScopes("upload")(http.HandlerFunc(handleTaskStatus(taskStatusStore))))
	apiV1.Handle("/v1/tasks/{docID}/cancel", requireScopes("upload")(http.HandlerFunc(handleTaskCancel(taskStatusStore, admissionStore))))
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

func runNotificationRelay(ctx context.Context, relay notification.Relay, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := relay.RunOnce(ctx); err != nil && ctx.Err() == nil {
			slog.Error("governance notification relay pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type notificationSnapshotReader interface {
	Snapshot(context.Context) (notification.Snapshot, error)
}

type notificationOperationsObserver interface {
	SetNotificationOperations(int, int, time.Duration)
}

func runNotificationOperationsMonitor(ctx context.Context, reader notificationSnapshotReader, observer notificationOperationsObserver, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		snapshot, err := reader.Snapshot(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("governance notification snapshot failed", "error", err)
			}
		} else if observer != nil {
			observer.SetNotificationOperations(snapshot.Pending, snapshot.Retried, snapshot.OldestAge)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
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
