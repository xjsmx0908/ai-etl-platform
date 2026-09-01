package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoad_Defaults(t *testing.T) {
	cfg := Load()

	if cfg.MaxWorkers != 10 {
		t.Errorf("expected MaxWorkers=10, got %d", cfg.MaxWorkers)
	}
	if cfg.BatchSize != 10 {
		t.Errorf("expected BatchSize=10, got %d", cfg.BatchSize)
	}
	if cfg.EmbedDimension != 1536 {
		t.Errorf("expected EmbedDimension=1536, got %d", cfg.EmbedDimension)
	}
	if cfg.MultipartMaxMemoryBytes != 4*1024*1024 {
		t.Errorf("expected MultipartMaxMemoryBytes=4MB, got %d", cfg.MultipartMaxMemoryBytes)
	}
	if cfg.Environment != "dev" {
		t.Errorf("expected Environment=dev, got %s", cfg.Environment)
	}
	if cfg.SparseK1 != 1.2 {
		t.Errorf("expected SparseK1=1.2, got %f", cfg.SparseK1)
	}
	if cfg.HealthPort != 8080 {
		t.Errorf("expected HealthPort=8080, got %d", cfg.HealthPort)
	}
	if cfg.HTTPHandlerTimeout != 60*time.Second {
		t.Errorf("expected HTTPHandlerTimeout=60s, got %v", cfg.HTTPHandlerTimeout)
	}
	if cfg.IdempotencyTTL != 24*time.Hour {
		t.Errorf("expected IdempotencyTTL=24h, got %v", cfg.IdempotencyTTL)
	}
	if cfg.TaskStatusTTL != 7*24*time.Hour {
		t.Errorf("expected TaskStatusTTL=168h, got %v", cfg.TaskStatusTTL)
	}
	if cfg.IngestionMetricsInterval != 15*time.Second {
		t.Errorf("expected IngestionMetricsInterval=15s, got %v", cfg.IngestionMetricsInterval)
	}
	if cfg.OrphanCleanupInterval != 15*time.Minute || cfg.OrphanCleanupGracePeriod != 24*time.Hour || cfg.OrphanCleanupBatchSize != 100 {
		t.Errorf("unexpected orphan cleanup defaults: interval=%v grace=%v batch=%d",
			cfg.OrphanCleanupInterval, cfg.OrphanCleanupGracePeriod, cfg.OrphanCleanupBatchSize)
	}
	if !cfg.IndexReconcileEnabled || cfg.IndexReconcileInterval != 5*time.Minute || cfg.IndexReconcileLease != 30*time.Minute || cfg.IndexReconcileBatchSize != 20 || cfg.IndexReconcileMaxRepairs != 3 {
		t.Fatalf("unexpected index reconciliation defaults: enabled=%v interval=%v lease=%v batch=%d max_repairs=%d",
			cfg.IndexReconcileEnabled, cfg.IndexReconcileInterval, cfg.IndexReconcileLease, cfg.IndexReconcileBatchSize, cfg.IndexReconcileMaxRepairs)
	}
	if cfg.IndexRetentionEnabled || cfg.IndexRetentionWindow != 0 || cfg.IndexRetentionInterval != time.Hour || cfg.IndexRetentionLease != 30*time.Minute || cfg.IndexRetentionBatchSize != 20 {
		t.Fatalf("unexpected index retention defaults: enabled=%v window=%v interval=%v lease=%v batch=%d",
			cfg.IndexRetentionEnabled, cfg.IndexRetentionWindow, cfg.IndexRetentionInterval, cfg.IndexRetentionLease, cfg.IndexRetentionBatchSize)
	}
	if cfg.TaskStatusStore != TaskStatusStoreAuto {
		t.Errorf("expected TaskStatusStore=auto, got %s", cfg.TaskStatusStore)
	}
	if cfg.ResolvedTaskStatusStore() != TaskStatusStoreMemory {
		t.Errorf("expected dev task status store memory, got %s", cfg.ResolvedTaskStatusStore())
	}
	if cfg.ESAddress != "http://elasticsearch:9200" {
		t.Errorf("expected ESAddress default, got %s", cfg.ESAddress)
	}
	if cfg.ESIndex != "documents_text" {
		t.Errorf("expected ESIndex default, got %s", cfg.ESIndex)
	}
	if cfg.ESQueueKey != "es:index:retry" {
		t.Errorf("expected ESQueueKey default, got %s", cfg.ESQueueKey)
	}
	if cfg.ESDeadLetterKey != "es:index:deadletter" {
		t.Errorf("expected ESDeadLetterKey default, got %s", cfg.ESDeadLetterKey)
	}
	if cfg.ESReplayPeriod != 2*time.Second {
		t.Errorf("expected ESReplayPeriod=2s, got %v", cfg.ESReplayPeriod)
	}
	if cfg.ESMaxRetries != 12 {
		t.Errorf("expected ESMaxRetries=12, got %d", cfg.ESMaxRetries)
	}
	if cfg.ESRetryBaseBackoff != 2*time.Second {
		t.Errorf("expected ESRetryBaseBackoff=2s, got %v", cfg.ESRetryBaseBackoff)
	}
	if cfg.ESRetryMaxBackoff != 5*time.Minute {
		t.Errorf("expected ESRetryMaxBackoff=5m, got %v", cfg.ESRetryMaxBackoff)
	}
	if cfg.ESRetryJitter != 0.2 {
		t.Errorf("expected ESRetryJitter=0.2, got %v", cfg.ESRetryJitter)
	}
	if cfg.RetrievalTimeout != 300*time.Millisecond {
		t.Errorf("expected RetrievalTimeout=300ms, got %v", cfg.RetrievalTimeout)
	}
	if cfg.RetrievalCandidateK != 50 {
		t.Errorf("expected RetrievalCandidateK=50, got %d", cfg.RetrievalCandidateK)
	}
	if cfg.RetrievalFinalTopK != 5 {
		t.Errorf("expected RetrievalFinalTopK=5, got %d", cfg.RetrievalFinalTopK)
	}
	if !cfg.RetrievalEnableES {
		t.Error("expected RetrievalEnableES=true")
	}
	if cfg.RetrievalEnableRerank {
		t.Error("expected RetrievalEnableRerank=false")
	}
	if cfg.RetrievalDiagnosticsEnabled {
		t.Error("expected RetrievalDiagnosticsEnabled=false")
	}
	if cfg.RetrievalRerankPolicy != RerankPolicyAuto {
		t.Errorf("expected RetrievalRerankPolicy=auto, got %s", cfg.RetrievalRerankPolicy)
	}
	if len(cfg.RetrievalExactSchemaFields) == 0 {
		t.Error("expected default RetrievalExactSchemaFields")
	}
	if !cfg.SemanticCacheEnabled {
		t.Error("expected SemanticCacheEnabled=true")
	}
	if cfg.SemanticCacheTTL != 10*time.Minute {
		t.Errorf("expected SemanticCacheTTL=10m, got %v", cfg.SemanticCacheTTL)
	}
	if cfg.SemanticCacheThreshold != 0.92 {
		t.Errorf("expected SemanticCacheThreshold=0.92, got %v", cfg.SemanticCacheThreshold)
	}
	if cfg.SemanticCacheMaxEntries != 128 {
		t.Errorf("expected SemanticCacheMaxEntries=128, got %d", cfg.SemanticCacheMaxEntries)
	}
	if cfg.AgentNodeID != "agent-api-1" {
		t.Errorf("expected AgentNodeID=agent-api-1, got %s", cfg.AgentNodeID)
	}
	if cfg.AgentMaxSteps != 8 {
		t.Errorf("expected AgentMaxSteps=8, got %d", cfg.AgentMaxSteps)
	}
	if cfg.AgentLockTTL != 30*time.Second {
		t.Errorf("expected AgentLockTTL=30s, got %v", cfg.AgentLockTTL)
	}
	if cfg.AgentRunTTL != 24*time.Hour {
		t.Errorf("expected AgentRunTTL=24h, got %v", cfg.AgentRunTTL)
	}
	if cfg.AgentRunTimeout != 30*time.Minute {
		t.Errorf("expected AgentRunTimeout=30m, got %v", cfg.AgentRunTimeout)
	}
	if cfg.AgentApprovalTimeout != 15*time.Minute {
		t.Errorf("expected AgentApprovalTimeout=15m, got %v", cfg.AgentApprovalTimeout)
	}
	if cfg.AgentPlannerType != AgentPlannerAuto {
		t.Errorf("expected AgentPlannerType=auto, got %s", cfg.AgentPlannerType)
	}
	if cfg.ResolvedAgentPlannerType() != AgentPlannerRule {
		t.Errorf("expected dev auto planner to resolve to rule, got %s", cfg.ResolvedAgentPlannerType())
	}
	if cfg.AgentPlannerEndpoint != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("expected default AgentPlannerEndpoint, got %s", cfg.AgentPlannerEndpoint)
	}
	if cfg.AgentPlannerModel != "deepseek-v4-flash" {
		t.Errorf("expected default AgentPlannerModel, got %s", cfg.AgentPlannerModel)
	}
	if cfg.AgentPlannerTimeout != 30*time.Second {
		t.Errorf("expected AgentPlannerTimeout=30s, got %v", cfg.AgentPlannerTimeout)
	}
	if cfg.AgentPlannerMaxTokens != 512 {
		t.Errorf("expected AgentPlannerMaxTokens=512, got %d", cfg.AgentPlannerMaxTokens)
	}
	if cfg.PGDSN != "" {
		t.Errorf("expected PGDSN empty default, got %s", cfg.PGDSN)
	}
	if cfg.BootstrapAdminUsername != "admin" {
		t.Errorf("expected BootstrapAdminUsername=admin, got %s", cfg.BootstrapAdminUsername)
	}
	if cfg.BootstrapAdminPassword != "" {
		t.Errorf("expected BootstrapAdminPassword empty default, got %s", cfg.BootstrapAdminPassword)
	}
	if cfg.BootstrapAdminTenant != "default" {
		t.Errorf("expected BootstrapAdminTenant=default, got %s", cfg.BootstrapAdminTenant)
	}
	if cfg.ReconcileDocsOnStartup {
		t.Error("expected ReconcileDocsOnStartup=false default")
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	os.Setenv("PIPELINE_MAX_WORKERS", "20")
	os.Setenv("HTTP_HANDLER_TIMEOUT", "7m")
	os.Setenv("ENVIRONMENT", "production")
	os.Setenv("EMBED_API_KEY", "sk-test")
	os.Setenv("ES_ADDRESS", "http://es:9200")
	os.Setenv("ES_INDEX", "docs_v2")
	os.Setenv("ES_QUEUE_KEY", "es:retry:v2")
	os.Setenv("ES_DEADLETTER_KEY", "es:dead:v2")
	os.Setenv("ES_REPLAY_PERIOD", "5s")
	os.Setenv("ES_MAX_RETRIES", "20")
	os.Setenv("ES_RETRY_BASE_BACKOFF", "3s")
	os.Setenv("ES_RETRY_MAX_BACKOFF", "30s")
	os.Setenv("ES_RETRY_JITTER", "0.4")
	os.Setenv("MULTIPART_MAX_MEMORY_MB", "8")
	os.Setenv("RETRIEVAL_TIMEOUT", "450ms")
	os.Setenv("RETRIEVAL_CANDIDATE_K", "80")
	os.Setenv("RETRIEVAL_FINAL_TOP_K", "7")
	os.Setenv("RETRIEVAL_ENABLE_ES", "false")
	os.Setenv("RETRIEVAL_ENABLE_RERANK", "true")
	os.Setenv("RETRIEVAL_RERANK_POLICY", "always")
	os.Setenv("RETRIEVAL_DIAGNOSTICS_ENABLED", "true")
	os.Setenv("RETRIEVAL_EXACT_SCHEMA_FIELDS", "contract_no,trace_id")
	os.Setenv("RERANK_ENDPOINT", "http://reranker:8080/rerank")
	os.Setenv("RERANK_API_KEY", "rk-test")
	os.Setenv("RERANK_MODEL", "test-reranker")
	os.Setenv("SEMANTIC_CACHE_ENABLED", "false")
	os.Setenv("SEMANTIC_CACHE_TTL", "2m")
	os.Setenv("SEMANTIC_CACHE_THRESHOLD", "0.88")
	os.Setenv("SEMANTIC_CACHE_MAX_ENTRIES", "64")
	os.Setenv("AGENT_NODE_ID", "agent-api-test")
	os.Setenv("AGENT_MAX_STEPS", "12")
	os.Setenv("AGENT_LOCK_TTL", "15s")
	os.Setenv("AGENT_RUN_TTL", "2h")
	os.Setenv("AGENT_RUN_TIMEOUT", "20m")
	os.Setenv("AGENT_APPROVAL_TIMEOUT", "5m")
	os.Setenv("AGENT_PLANNER_TYPE", "llm")
	os.Setenv("AGENT_PLANNER_ENDPOINT", "http://planner:8080/v1/chat/completions")
	os.Setenv("AGENT_PLANNER_API_KEY", "planner-key")
	os.Setenv("AGENT_PLANNER_MODEL", "planner-model")
	os.Setenv("AGENT_PLANNER_TIMEOUT", "12s")
	os.Setenv("AGENT_PLANNER_MAX_TOKENS", "768")
	os.Setenv("TASK_STATUS_STORE", "redis")
	os.Setenv("TASK_STATUS_TTL", "48h")
	os.Setenv("PG_DSN", "postgres://app:secret@pg:5432/ai_etl")
	os.Setenv("BOOTSTRAP_ADMIN_USERNAME", "root")
	os.Setenv("BOOTSTRAP_ADMIN_PASSWORD", "s3cr3t-password")
	os.Setenv("BOOTSTRAP_ADMIN_TENANT", "acme")
	os.Setenv("RECONCILE_DOCS_ON_STARTUP", "true")
	defer func() {
		os.Unsetenv("PIPELINE_MAX_WORKERS")
		os.Unsetenv("HTTP_HANDLER_TIMEOUT")
		os.Unsetenv("ENVIRONMENT")
		os.Unsetenv("EMBED_API_KEY")
		os.Unsetenv("ES_ADDRESS")
		os.Unsetenv("ES_INDEX")
		os.Unsetenv("ES_QUEUE_KEY")
		os.Unsetenv("ES_DEADLETTER_KEY")
		os.Unsetenv("ES_REPLAY_PERIOD")
		os.Unsetenv("ES_MAX_RETRIES")
		os.Unsetenv("ES_RETRY_BASE_BACKOFF")
		os.Unsetenv("ES_RETRY_MAX_BACKOFF")
		os.Unsetenv("ES_RETRY_JITTER")
		os.Unsetenv("MULTIPART_MAX_MEMORY_MB")
		os.Unsetenv("RETRIEVAL_TIMEOUT")
		os.Unsetenv("RETRIEVAL_CANDIDATE_K")
		os.Unsetenv("RETRIEVAL_FINAL_TOP_K")
		os.Unsetenv("RETRIEVAL_ENABLE_ES")
		os.Unsetenv("RETRIEVAL_ENABLE_RERANK")
		os.Unsetenv("RETRIEVAL_RERANK_POLICY")
		os.Unsetenv("RETRIEVAL_DIAGNOSTICS_ENABLED")
		os.Unsetenv("RETRIEVAL_EXACT_SCHEMA_FIELDS")
		os.Unsetenv("RERANK_ENDPOINT")
		os.Unsetenv("RERANK_API_KEY")
		os.Unsetenv("RERANK_MODEL")
		os.Unsetenv("SEMANTIC_CACHE_ENABLED")
		os.Unsetenv("SEMANTIC_CACHE_TTL")
		os.Unsetenv("SEMANTIC_CACHE_THRESHOLD")
		os.Unsetenv("SEMANTIC_CACHE_MAX_ENTRIES")
		os.Unsetenv("AGENT_NODE_ID")
		os.Unsetenv("AGENT_MAX_STEPS")
		os.Unsetenv("AGENT_LOCK_TTL")
		os.Unsetenv("AGENT_RUN_TTL")
		os.Unsetenv("AGENT_RUN_TIMEOUT")
		os.Unsetenv("AGENT_APPROVAL_TIMEOUT")
		os.Unsetenv("AGENT_PLANNER_TYPE")
		os.Unsetenv("AGENT_PLANNER_ENDPOINT")
		os.Unsetenv("AGENT_PLANNER_API_KEY")
		os.Unsetenv("AGENT_PLANNER_MODEL")
		os.Unsetenv("AGENT_PLANNER_TIMEOUT")
		os.Unsetenv("AGENT_PLANNER_MAX_TOKENS")
		os.Unsetenv("TASK_STATUS_STORE")
		os.Unsetenv("TASK_STATUS_TTL")
		os.Unsetenv("PG_DSN")
		os.Unsetenv("BOOTSTRAP_ADMIN_USERNAME")
		os.Unsetenv("BOOTSTRAP_ADMIN_PASSWORD")
		os.Unsetenv("BOOTSTRAP_ADMIN_TENANT")
		os.Unsetenv("RECONCILE_DOCS_ON_STARTUP")
	}()

	cfg := Load()

	if cfg.MaxWorkers != 20 {
		t.Errorf("expected MaxWorkers=20, got %d", cfg.MaxWorkers)
	}
	if cfg.HTTPHandlerTimeout != 7*time.Minute {
		t.Errorf("expected HTTPHandlerTimeout=7m, got %v", cfg.HTTPHandlerTimeout)
	}
	if cfg.Environment != "production" {
		t.Errorf("expected Environment=production, got %s", cfg.Environment)
	}
	if !cfg.RetrievalDiagnosticsEnabled {
		t.Error("expected RetrievalDiagnosticsEnabled=true")
	}
	if cfg.EmbedAPIKey != "sk-test" {
		t.Errorf("expected EmbedAPIKey=sk-test, got %s", cfg.EmbedAPIKey)
	}
	if cfg.ESAddress != "http://es:9200" {
		t.Errorf("expected ESAddress override, got %s", cfg.ESAddress)
	}
	if cfg.ESIndex != "docs_v2" {
		t.Errorf("expected ESIndex override, got %s", cfg.ESIndex)
	}
	if cfg.ESQueueKey != "es:retry:v2" {
		t.Errorf("expected ESQueueKey override, got %s", cfg.ESQueueKey)
	}
	if cfg.ESDeadLetterKey != "es:dead:v2" {
		t.Errorf("expected ESDeadLetterKey override, got %s", cfg.ESDeadLetterKey)
	}
	if cfg.ESReplayPeriod != 5*time.Second {
		t.Errorf("expected ESReplayPeriod=5s, got %v", cfg.ESReplayPeriod)
	}
	if cfg.ESMaxRetries != 20 {
		t.Errorf("expected ESMaxRetries=20, got %d", cfg.ESMaxRetries)
	}
	if cfg.ESRetryBaseBackoff != 3*time.Second {
		t.Errorf("expected ESRetryBaseBackoff=3s, got %v", cfg.ESRetryBaseBackoff)
	}
	if cfg.ESRetryMaxBackoff != 30*time.Second {
		t.Errorf("expected ESRetryMaxBackoff=30s, got %v", cfg.ESRetryMaxBackoff)
	}
	if cfg.ESRetryJitter != 0.4 {
		t.Errorf("expected ESRetryJitter=0.4, got %v", cfg.ESRetryJitter)
	}
	if cfg.MultipartMaxMemoryBytes != 8*1024*1024 {
		t.Errorf("expected MultipartMaxMemoryBytes=8MB, got %d", cfg.MultipartMaxMemoryBytes)
	}
	if cfg.RetrievalTimeout != 450*time.Millisecond {
		t.Errorf("expected RetrievalTimeout=450ms, got %v", cfg.RetrievalTimeout)
	}
	if cfg.RetrievalCandidateK != 80 {
		t.Errorf("expected RetrievalCandidateK=80, got %d", cfg.RetrievalCandidateK)
	}
	if cfg.RetrievalFinalTopK != 7 {
		t.Errorf("expected RetrievalFinalTopK=7, got %d", cfg.RetrievalFinalTopK)
	}
	if cfg.RetrievalEnableES {
		t.Error("expected RetrievalEnableES=false")
	}
	if !cfg.RetrievalEnableRerank {
		t.Error("expected RetrievalEnableRerank=true")
	}
	if cfg.RetrievalRerankPolicy != RerankPolicyAlways {
		t.Errorf("expected RetrievalRerankPolicy=always, got %s", cfg.RetrievalRerankPolicy)
	}
	if len(cfg.RetrievalExactSchemaFields) != 2 || cfg.RetrievalExactSchemaFields[0] != "contract_no" || cfg.RetrievalExactSchemaFields[1] != "trace_id" {
		t.Errorf("unexpected RetrievalExactSchemaFields override: %v", cfg.RetrievalExactSchemaFields)
	}
	if cfg.RerankEndpoint != "http://reranker:8080/rerank" {
		t.Errorf("expected RerankEndpoint override, got %s", cfg.RerankEndpoint)
	}
	if cfg.RerankAPIKey != "rk-test" {
		t.Errorf("expected RerankAPIKey override, got %s", cfg.RerankAPIKey)
	}
	if cfg.RerankModel != "test-reranker" {
		t.Errorf("expected RerankModel override, got %s", cfg.RerankModel)
	}
	if cfg.SemanticCacheEnabled {
		t.Error("expected SemanticCacheEnabled=false")
	}
	if cfg.SemanticCacheTTL != 2*time.Minute {
		t.Errorf("expected SemanticCacheTTL=2m, got %v", cfg.SemanticCacheTTL)
	}
	if cfg.SemanticCacheThreshold != 0.88 {
		t.Errorf("expected SemanticCacheThreshold=0.88, got %v", cfg.SemanticCacheThreshold)
	}
	if cfg.SemanticCacheMaxEntries != 64 {
		t.Errorf("expected SemanticCacheMaxEntries=64, got %d", cfg.SemanticCacheMaxEntries)
	}
	if cfg.AgentNodeID != "agent-api-test" {
		t.Errorf("expected AgentNodeID override, got %s", cfg.AgentNodeID)
	}
	if cfg.AgentMaxSteps != 12 {
		t.Errorf("expected AgentMaxSteps=12, got %d", cfg.AgentMaxSteps)
	}
	if cfg.AgentLockTTL != 15*time.Second {
		t.Errorf("expected AgentLockTTL=15s, got %v", cfg.AgentLockTTL)
	}
	if cfg.AgentRunTTL != 2*time.Hour {
		t.Errorf("expected AgentRunTTL=2h, got %v", cfg.AgentRunTTL)
	}
	if cfg.AgentRunTimeout != 20*time.Minute {
		t.Errorf("expected AgentRunTimeout=20m, got %v", cfg.AgentRunTimeout)
	}
	if cfg.AgentApprovalTimeout != 5*time.Minute {
		t.Errorf("expected AgentApprovalTimeout=5m, got %v", cfg.AgentApprovalTimeout)
	}
	if cfg.AgentPlannerType != AgentPlannerLLM {
		t.Errorf("expected AgentPlannerType=llm, got %s", cfg.AgentPlannerType)
	}
	if cfg.ResolvedAgentPlannerType() != AgentPlannerLLM {
		t.Errorf("expected resolved planner llm, got %s", cfg.ResolvedAgentPlannerType())
	}
	if cfg.AgentPlannerEndpoint != "http://planner:8080/v1/chat/completions" {
		t.Errorf("expected AgentPlannerEndpoint override, got %s", cfg.AgentPlannerEndpoint)
	}
	if cfg.AgentPlannerAPIKey != "planner-key" {
		t.Errorf("expected AgentPlannerAPIKey override, got %s", cfg.AgentPlannerAPIKey)
	}
	if cfg.AgentPlannerModel != "planner-model" {
		t.Errorf("expected AgentPlannerModel override, got %s", cfg.AgentPlannerModel)
	}
	if cfg.AgentPlannerTimeout != 12*time.Second {
		t.Errorf("expected AgentPlannerTimeout=12s, got %v", cfg.AgentPlannerTimeout)
	}
	if cfg.AgentPlannerMaxTokens != 768 {
		t.Errorf("expected AgentPlannerMaxTokens=768, got %d", cfg.AgentPlannerMaxTokens)
	}
	if cfg.TaskStatusTTL != 48*time.Hour {
		t.Errorf("expected TaskStatusTTL=48h, got %v", cfg.TaskStatusTTL)
	}
	if cfg.TaskStatusStore != TaskStatusStoreRedis {
		t.Errorf("expected TaskStatusStore=redis, got %s", cfg.TaskStatusStore)
	}
	if cfg.ResolvedTaskStatusStore() != TaskStatusStoreRedis {
		t.Errorf("expected resolved task status store redis, got %s", cfg.ResolvedTaskStatusStore())
	}
	if cfg.PGDSN != "postgres://app:secret@pg:5432/ai_etl" {
		t.Errorf("expected PGDSN override, got %s", cfg.PGDSN)
	}
	if cfg.BootstrapAdminUsername != "root" {
		t.Errorf("expected BootstrapAdminUsername=root, got %s", cfg.BootstrapAdminUsername)
	}
	if cfg.BootstrapAdminPassword != "s3cr3t-password" {
		t.Errorf("expected BootstrapAdminPassword override, got %s", cfg.BootstrapAdminPassword)
	}
	if cfg.BootstrapAdminTenant != "acme" {
		t.Errorf("expected BootstrapAdminTenant=acme, got %s", cfg.BootstrapAdminTenant)
	}
	if !cfg.ReconcileDocsOnStartup {
		t.Error("expected ReconcileDocsOnStartup=true")
	}
}

func TestValidate_DevMode(t *testing.T) {
	cfg := Load()
	if err := cfg.Validate(); err != nil {
		t.Errorf("dev mode should pass validation, got: %v", err)
	}
}

func TestValidate_IngestionJobLeaseCoversRetryWindow(t *testing.T) {
	cfg := Load()
	cfg.PipelineTimeout = 5 * time.Minute
	cfg.MaxRetries = 3
	cfg.RetryBackoff = time.Second
	cfg.IngestionJobLease = 20*time.Minute + 7*time.Second
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected lease equal to worst-case retry window to be rejected")
	}
	cfg.IngestionJobLease += time.Second
	if err := cfg.Validate(); err != nil {
		t.Fatalf("lease exceeding retry window should pass: %v", err)
	}
}

func TestLoad_IngestionOperationsOverrides(t *testing.T) {
	t.Setenv("INGESTION_METRICS_INTERVAL", "5s")
	t.Setenv("ORPHAN_CLEANUP_INTERVAL", "30m")
	t.Setenv("ORPHAN_CLEANUP_GRACE_PERIOD", "48h")
	t.Setenv("ORPHAN_CLEANUP_BATCH_SIZE", "250")

	cfg := Load()
	if cfg.IngestionMetricsInterval != 5*time.Second || cfg.OrphanCleanupInterval != 30*time.Minute ||
		cfg.OrphanCleanupGracePeriod != 48*time.Hour || cfg.OrphanCleanupBatchSize != 250 {
		t.Fatalf("unexpected ingestion operations overrides: metrics=%v interval=%v grace=%v batch=%d",
			cfg.IngestionMetricsInterval, cfg.OrphanCleanupInterval, cfg.OrphanCleanupGracePeriod, cfg.OrphanCleanupBatchSize)
	}
}

func TestLoad_IndexReconciliationOverrides(t *testing.T) {
	t.Setenv("INDEX_RECONCILE_ENABLED", "false")
	t.Setenv("INDEX_RECONCILE_INTERVAL", "10m")
	t.Setenv("INDEX_RECONCILE_LEASE", "45m")
	t.Setenv("INDEX_RECONCILE_BATCH_SIZE", "50")
	t.Setenv("INDEX_RECONCILE_MAX_REPAIRS", "5")
	cfg := Load()
	if cfg.IndexReconcileEnabled || cfg.IndexReconcileInterval != 10*time.Minute || cfg.IndexReconcileLease != 45*time.Minute || cfg.IndexReconcileBatchSize != 50 || cfg.IndexReconcileMaxRepairs != 5 {
		t.Fatalf("unexpected reconciliation overrides: %+v", cfg)
	}
}

func TestValidate_RejectsUnsafeIndexReconciliationSettings(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"interval":     func(c *Config) { c.IndexReconcileInterval = 0 },
		"lease":        func(c *Config) { c.IndexReconcileLease = 0 },
		"short lease":  func(c *Config) { c.IndexReconcileLease = c.IndexReconcileInterval },
		"batch low":    func(c *Config) { c.IndexReconcileBatchSize = 0 },
		"batch high":   func(c *Config) { c.IndexReconcileBatchSize = 1001 },
		"repairs low":  func(c *Config) { c.IndexReconcileMaxRepairs = 0 },
		"repairs high": func(c *Config) { c.IndexReconcileMaxRepairs = 21 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Load()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected invalid reconciliation settings to fail")
			}
		})
	}
}

func TestLoad_IndexRetentionOverrides(t *testing.T) {
	t.Setenv("INDEX_RETENTION_ENABLED", "true")
	t.Setenv("INDEX_RETENTION_WINDOW", "720h")
	t.Setenv("INDEX_RETENTION_INTERVAL", "2h")
	t.Setenv("INDEX_RETENTION_LEASE", "20m")
	t.Setenv("INDEX_RETENTION_BATCH_SIZE", "50")
	cfg := Load()
	if !cfg.IndexRetentionEnabled || cfg.IndexRetentionWindow != 30*24*time.Hour || cfg.IndexRetentionInterval != 2*time.Hour || cfg.IndexRetentionLease != 20*time.Minute || cfg.IndexRetentionBatchSize != 50 {
		t.Fatalf("unexpected index retention overrides: %+v", cfg)
	}
}

func TestValidate_RequiresExplicitRetentionWindowBeforeCleanup(t *testing.T) {
	cfg := Load()
	cfg.IndexRetentionEnabled = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("enabled cleanup without retention window must fail")
	}
	cfg.IndexRetentionWindow = 30 * 24 * time.Hour
	if err := cfg.Validate(); err != nil {
		t.Fatalf("configured retention rejected: %v", err)
	}
}

func TestValidate_RequiresRetentionLeaseToCoverClaimedBatch(t *testing.T) {
	cfg := Load()
	cfg.IndexRetentionBatchSize = 20
	cfg.IndexRetentionLease = 15 * time.Minute
	if err := cfg.Validate(); err == nil {
		t.Fatal("lease equal to worst-case batch duration must fail")
	}
}

func TestValidate_RejectsUnsafeIngestionOperationsSettings(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"metrics interval": func(c *Config) { c.IngestionMetricsInterval = 0 },
		"cleanup interval": func(c *Config) { c.OrphanCleanupInterval = 0 },
		"cleanup grace":    func(c *Config) { c.OrphanCleanupGracePeriod = 0 },
		"batch below min":  func(c *Config) { c.OrphanCleanupBatchSize = 0 },
		"batch above max":  func(c *Config) { c.OrphanCleanupBatchSize = 1001 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Load()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected unsafe ingestion operations settings to fail")
			}
		})
	}
}

func TestValidate_RejectsUnsafePipelineRetrySettings(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.MaxRetries = 11 },
		func(c *Config) { c.MaxRetries = -1 },
		func(c *Config) { c.PipelineTimeout = 0 },
		func(c *Config) { c.RetryBackoff = 0 },
	} {
		cfg := Load()
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected unsafe retry settings to fail: %+v", cfg)
		}
	}
}

func TestValidate_ProductionMissingKey(t *testing.T) {
	cfg := Load()
	cfg.Environment = "production"
	cfg.EmbedAPIKey = ""

	err := cfg.Validate()
	if err == nil {
		t.Error("expected validation error for missing EMBED_API_KEY in production")
	}
}

func TestValidate_WorkerRange(t *testing.T) {
	cfg := Load()
	cfg.MaxWorkers = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for MaxWorkers=0")
	}

	cfg.MaxWorkers = 101
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for MaxWorkers=101")
	}
}

func TestValidate_ESReplayPeriodAndRetries(t *testing.T) {
	cfg := Load()
	cfg.ESReplayPeriod = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for ESReplayPeriod <= 0")
	}

	cfg = Load()
	cfg.ESMaxRetries = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for ESMaxRetries < 1")
	}

	cfg = Load()
	cfg.ESRetryBaseBackoff = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for ESRetryBaseBackoff <= 0")
	}

	cfg = Load()
	cfg.ESRetryMaxBackoff = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for ESRetryMaxBackoff <= 0")
	}

	cfg = Load()
	cfg.ESRetryMaxBackoff = 1 * time.Second
	cfg.ESRetryBaseBackoff = 2 * time.Second
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for ESRetryMaxBackoff < ESRetryBaseBackoff")
	}

	cfg = Load()
	cfg.ESRetryJitter = -0.1
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for ESRetryJitter < 0")
	}

	cfg = Load()
	cfg.ESRetryJitter = 1.1
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for ESRetryJitter > 1")
	}
}

func TestValidate_RerankPolicy(t *testing.T) {
	cfg := Load()
	cfg.RetrievalRerankPolicy = "semantic-only"

	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid rerank policy")
	}
}

func TestValidate_RetrievalExactSchemaFields(t *testing.T) {
	cfg := Load()
	cfg.RetrievalExactSchemaFields = []string{"contract_no", "bad.field"}

	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid exact schema field")
	}
}

func TestValidate_RetrievalConfig(t *testing.T) {
	cfg := Load()
	cfg.RetrievalTimeout = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for RetrievalTimeout <= 0")
	}

	cfg = Load()
	cfg.RetrievalCandidateK = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for RetrievalCandidateK < 1")
	}

	cfg = Load()
	cfg.RetrievalFinalTopK = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for RetrievalFinalTopK < 1")
	}

	cfg = Load()
	cfg.SemanticCacheTTL = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for SemanticCacheTTL <= 0")
	}

	cfg = Load()
	cfg.SemanticCacheThreshold = 1.1
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for SemanticCacheThreshold > 1")
	}

	cfg = Load()
	cfg.SemanticCacheMaxEntries = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for SemanticCacheMaxEntries < 1")
	}

	cfg = Load()
	cfg.TaskStatusTTL = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for TaskStatusTTL <= 0")
	}

	cfg = Load()
	cfg.TaskStatusStore = "postgres"
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid TaskStatusStore")
	}
}

func TestValidate_DoesNotRequireAgentPlannerForWorker(t *testing.T) {
	cfg := Load()
	cfg.Environment = "staging"
	cfg.AgentPlannerType = AgentPlannerAuto
	cfg.AgentPlannerEndpoint = "https://api.openai.com/v1/chat/completions"
	cfg.AgentPlannerAPIKey = ""

	if err := cfg.Validate(); err != nil {
		t.Fatalf("worker/base validation should not require Agent planner config, got: %v", err)
	}
}

func TestValidateAPI_AgentConfig(t *testing.T) {
	cfg := Load()
	cfg.AgentNodeID = ""
	if err := cfg.ValidateAPI(); err == nil {
		t.Error("expected error for empty AgentNodeID")
	}

	cfg = Load()
	cfg.AgentMaxSteps = 0
	if err := cfg.ValidateAPI(); err == nil {
		t.Error("expected error for AgentMaxSteps < 1")
	}

	cfg = Load()
	cfg.AgentLockTTL = 0
	if err := cfg.ValidateAPI(); err == nil {
		t.Error("expected error for AgentLockTTL <= 0")
	}

	cfg = Load()
	cfg.AgentRunTTL = 0
	if err := cfg.ValidateAPI(); err == nil {
		t.Error("expected error for AgentRunTTL <= 0")
	}

	cfg = Load()
	cfg.AgentRunTimeout = 0
	if err := cfg.ValidateAPI(); err == nil {
		t.Error("expected error for AgentRunTimeout <= 0")
	}

	cfg = Load()
	cfg.AgentApprovalTimeout = 0
	if err := cfg.ValidateAPI(); err == nil {
		t.Error("expected error for AgentApprovalTimeout <= 0")
	}

	cfg = Load()
	cfg.AgentPlannerType = "random"
	if err := cfg.ValidateAPI(); err == nil {
		t.Error("expected error for invalid AgentPlannerType")
	}

	cfg = Load()
	cfg.AgentPlannerType = AgentPlannerLLM
	cfg.AgentPlannerEndpoint = ""
	if err := cfg.ValidateAPI(); err == nil {
		t.Error("expected error for empty AgentPlannerEndpoint with llm planner")
	}

	cfg = Load()
	cfg.AgentPlannerType = AgentPlannerLLM
	cfg.AgentPlannerModel = ""
	if err := cfg.ValidateAPI(); err == nil {
		t.Error("expected error for empty AgentPlannerModel with llm planner")
	}

	cfg = Load()
	cfg.AgentPlannerTimeout = 0
	if err := cfg.ValidateAPI(); err == nil {
		t.Error("expected error for AgentPlannerTimeout <= 0")
	}

	cfg = Load()
	cfg.AgentPlannerMaxTokens = 0
	if err := cfg.ValidateAPI(); err == nil {
		t.Error("expected error for AgentPlannerMaxTokens < 1")
	}

	cfg = Load()
	cfg.Environment = "staging"
	cfg.AgentPlannerType = AgentPlannerAuto
	cfg.AgentPlannerEndpoint = "https://api.openai.com/v1/chat/completions"
	cfg.AgentPlannerAPIKey = ""
	if err := cfg.ValidateAPI(); err == nil {
		t.Error("expected API validation to require key for OpenAI planner endpoint")
	}
}

func TestValidate_ProductionAgentPlanner(t *testing.T) {
	cfg := Load()
	cfg.Environment = "production"
	cfg.EmbedAPIKey = "embed-key"
	cfg.KafkaBrokers = "kafka:9092"
	cfg.StoreEndpoint = "http://qdrant:6333"
	cfg.RedisAddr = "redis-cache:6379"
	cfg.RedisCacheAddr = "redis-cache:6379"
	cfg.RedisStateAddr = "redis-state:6379"
	cfg.JWTSecret = "12345678901234567890123456789012"
	cfg.S3AccessKey = "prod-access"
	cfg.S3SecretKey = "prod-secret"
	cfg.CORSAllowedOrigins = []string{"https://console.example.com"}
	cfg.AgentPlannerType = AgentPlannerAuto
	cfg.AgentPlannerEndpoint = "http://planner:8080/v1/chat/completions"
	cfg.PGDSN = "postgres://app:prod@pg.internal:5432/ai_etl"
	cfg.BootstrapAdminPassword = "initial-admin-password"

	if err := cfg.ValidateAPI(); err != nil {
		t.Fatalf("expected production auto planner to resolve to llm, got %v", err)
	}
	if cfg.ResolvedAgentPlannerType() != AgentPlannerLLM {
		t.Fatalf("expected production auto planner to resolve to llm, got %s", cfg.ResolvedAgentPlannerType())
	}

	cfg.AgentPlannerType = AgentPlannerRule
	if err := cfg.ValidateAPI(); err == nil {
		t.Error("expected production rule planner to be rejected")
	}

	cfg.AgentPlannerType = AgentPlannerLLM
	cfg.AgentPlannerEndpoint = "https://api.openai.com/v1/chat/completions"
	cfg.AgentPlannerAPIKey = ""
	if err := cfg.ValidateAPI(); err == nil {
		t.Error("expected OpenAI planner endpoint to require API key in production")
	}
}

func TestIsDev(t *testing.T) {
	cfg := Load()
	if !cfg.IsDev() {
		t.Error("default config should be dev mode")
	}
	cfg.Environment = "production"
	if cfg.IsDev() {
		t.Error("production should not be dev mode")
	}
}

func TestValidateAllowsSessionCoreOnlyForPersonalDemoDevProfile(t *testing.T) {
	t.Setenv("SESSION_CORE_ENABLED", "false")
	t.Setenv("IDENTITY_POLICY_PROFILE", "")
	cfg := Load()
	if cfg.SessionCoreEnabled {
		t.Fatal("session core must default off")
	}

	cfg.SessionCoreEnabled = true
	cfg.IdentityPolicyProfile = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("enabled session core must require an explicit profile")
	}
	cfg.IdentityPolicyProfile = "personal-demo-v1"
	cfg.Environment = "dev"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("personal demo dev profile should be accepted: %v", err)
	}
	for _, environment := range []string{"staging", "production"} {
		cfg.Environment = environment
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%s must reject the personal demo session profile", environment)
		}
		cfg.SessionCoreEnabled = false
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%s must reject the demo profile even when the session core is disabled", environment)
		}
		cfg.SessionCoreEnabled = true
	}
}

func TestEnvDuration(t *testing.T) {
	os.Setenv("TEST_DURATION", "5s")
	defer os.Unsetenv("TEST_DURATION")

	d := EnvDuration("TEST_DURATION", time.Second)
	if d != 5*time.Second {
		t.Errorf("expected 5s, got %v", d)
	}

	// Invalid value should return default
	os.Setenv("TEST_DURATION", "invalid")
	d = EnvDuration("TEST_DURATION", 2*time.Second)
	if d != 2*time.Second {
		t.Errorf("expected 2s for invalid input, got %v", d)
	}
}

func TestValidateAPI_ProductionWeakJWTRejected(t *testing.T) {
	cfg := Load()
	cfg.Environment = "production"
	cfg.EmbedAPIKey = "sk-test"
	cfg.KafkaBrokers = "kafka.internal:9092"
	cfg.StoreEndpoint = "http://qdrant.internal:6333"
	cfg.RedisAddr = "redis-cache.internal:6379"
	cfg.RedisCacheAddr = "redis-cache.internal:6379"
	cfg.RedisStateAddr = "redis-state.internal:6379"
	cfg.AgentPlannerEndpoint = "http://planner.internal/v1/chat/completions"
	cfg.JWTSecret = "change-me-in-production"
	cfg.S3AccessKey = "prod-access"
	cfg.S3SecretKey = "prod-secret"
	cfg.PGDSN = "postgres://app:prod@pg.internal:5432/ai_etl"
	cfg.BootstrapAdminPassword = "initial-admin-password"

	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected weak JWT secret to be rejected in production")
	}
}

func TestValidateAPI_ProductionDefaultS3Rejected(t *testing.T) {
	cfg := Load()
	cfg.Environment = "production"
	cfg.EmbedAPIKey = "sk-test"
	cfg.KafkaBrokers = "kafka.internal:9092"
	cfg.StoreEndpoint = "http://qdrant.internal:6333"
	cfg.RedisAddr = "redis-cache.internal:6379"
	cfg.RedisCacheAddr = "redis-cache.internal:6379"
	cfg.RedisStateAddr = "redis-state.internal:6379"
	cfg.AgentPlannerEndpoint = "http://planner.internal/v1/chat/completions"
	cfg.JWTSecret = "12345678901234567890123456789012"
	cfg.S3AccessKey = "minioadmin"
	cfg.S3SecretKey = "minioadmin"
	cfg.PGDSN = "postgres://app:prod@pg.internal:5432/ai_etl"
	cfg.BootstrapAdminPassword = "initial-admin-password"

	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected default S3 credentials to be rejected in production")
	}
}

func TestValidateAPI_ProductionStrongSecretsPass(t *testing.T) {
	cfg := Load()
	cfg.Environment = "production"
	cfg.EmbedAPIKey = "sk-test"
	cfg.KafkaBrokers = "kafka.internal:9092"
	cfg.StoreEndpoint = "http://qdrant.internal:6333"
	cfg.RedisAddr = "redis-cache.internal:6379"
	cfg.RedisCacheAddr = "redis-cache.internal:6379"
	cfg.RedisStateAddr = "redis-state.internal:6379"
	cfg.AgentPlannerEndpoint = "http://planner.internal/v1/chat/completions"
	cfg.JWTSecret = "12345678901234567890123456789012"
	cfg.S3AccessKey = "prod-access"
	cfg.S3SecretKey = "prod-secret"
	cfg.CORSAllowedOrigins = []string{"https://console.example.com"}
	cfg.PGDSN = "postgres://app:prod@pg.internal:5432/ai_etl"
	cfg.BootstrapAdminPassword = "initial-admin-password"

	if err := cfg.ValidateAPI(); err != nil {
		t.Fatalf("expected strong production config to pass, got: %v", err)
	}
}

func TestValidateAPI_OIDCRejectsUnsafeIssuerAndRedirect(t *testing.T) {
	cfg := Load()
	cfg.OIDCEnabled = true
	cfg.OIDCClientID = "rag-web"
	cfg.OIDCTransactionTTL = 5 * time.Minute
	cfg.OIDCIssuer = "http://idp.example.com/realms/acme"
	cfg.OIDCRedirectURI = "https://rag.example.com/api/auth/oidc/callback"
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected non-HTTPS issuer to be rejected")
	}

	cfg.OIDCIssuer = "https://idp.example.com/realms/acme"
	cfg.OIDCRedirectURI = "https://rag.example.com/api/auth/oidc/callback?next=evil"
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected redirect URI with query to be rejected")
	}

	cfg.OIDCRedirectURI = "https://rag.example.com/api/auth/oidc/callback"
	cfg.OIDCLogoutRedirectURI = "https://evil.example.com/api/auth/logout/callback"
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected cross-origin logout redirect URI to be rejected")
	}
}

func TestValidateAPI_SCIMRequiresExplicitSafeConnectorPolicy(t *testing.T) {
	cfg := Load()
	cfg.SCIMEnabled = true
	cfg.SCIMConnectorID = "workforce"
	cfg.SCIMTenantID = "acme"
	cfg.SCIMIssuer = "http://idp.example.com"
	cfg.SCIMSubjectAttribute = "externalId"
	cfg.SCIMDefaultRole = "readonly"
	cfg.SCIMBearerTokens = []string{"secret"}
	cfg.SCIMMaxBodyBytes = 64 << 10
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected unsafe SCIM issuer to be rejected")
	}
	cfg.SCIMIssuer = "https://idp.example.com"
	cfg.SCIMSubjectAttribute = "userName"
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected implicit userName subject mapping to be rejected")
	}
	cfg.SCIMSubjectAttribute = "externalId"
	cfg.SCIMDefaultRole = "admin"
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected privileged SCIM default role to be rejected")
	}
}

func TestValidateAPI_ProductionMissingPGDSN(t *testing.T) {
	cfg := Load()
	cfg.Environment = "production"
	cfg.EmbedAPIKey = "sk-test"
	cfg.KafkaBrokers = "kafka.internal:9092"
	cfg.StoreEndpoint = "http://qdrant.internal:6333"
	cfg.RedisAddr = "redis-cache.internal:6379"
	cfg.RedisCacheAddr = "redis-cache.internal:6379"
	cfg.RedisStateAddr = "redis-state.internal:6379"
	cfg.AgentPlannerEndpoint = "http://planner.internal/v1/chat/completions"
	cfg.JWTSecret = "12345678901234567890123456789012"
	cfg.S3AccessKey = "prod-access"
	cfg.S3SecretKey = "prod-secret"
	cfg.CORSAllowedOrigins = []string{"https://console.example.com"}
	cfg.BootstrapAdminPassword = "initial-admin-password"
	// PGDSN left empty.

	err := cfg.ValidateAPI()
	if err == nil {
		t.Fatal("expected missing PG_DSN to be rejected in production")
	}
	if !strings.Contains(err.Error(), "PG_DSN") {
		t.Errorf("expected error to mention PG_DSN, got: %v", err)
	}
}

func TestValidateAPI_ProductionWeakBootstrapPassword(t *testing.T) {
	cfg := Load()
	cfg.Environment = "production"
	cfg.EmbedAPIKey = "sk-test"
	cfg.KafkaBrokers = "kafka.internal:9092"
	cfg.StoreEndpoint = "http://qdrant.internal:6333"
	cfg.RedisAddr = "redis-cache.internal:6379"
	cfg.RedisCacheAddr = "redis-cache.internal:6379"
	cfg.RedisStateAddr = "redis-state.internal:6379"
	cfg.AgentPlannerEndpoint = "http://planner.internal/v1/chat/completions"
	cfg.JWTSecret = "12345678901234567890123456789012"
	cfg.S3AccessKey = "prod-access"
	cfg.S3SecretKey = "prod-secret"
	cfg.CORSAllowedOrigins = []string{"https://console.example.com"}
	cfg.PGDSN = "postgres://app:prod@pg.internal:5432/ai_etl"
	cfg.BootstrapAdminPassword = "short"

	err := cfg.ValidateAPI()
	if err == nil {
		t.Fatal("expected weak bootstrap password to be rejected in production")
	}
	if !strings.Contains(err.Error(), "BOOTSTRAP_ADMIN_PASSWORD") {
		t.Errorf("expected error to mention BOOTSTRAP_ADMIN_PASSWORD, got: %v", err)
	}
}

func TestValidateAPI_ProductionWildcardCORSRejected(t *testing.T) {
	cfg := Load()
	cfg.Environment = "production"
	cfg.EmbedAPIKey = "sk-test"
	cfg.KafkaBrokers = "kafka.internal:9092"
	cfg.StoreEndpoint = "http://qdrant.internal:6333"
	cfg.RedisAddr = "redis-cache.internal:6379"
	cfg.RedisCacheAddr = "redis-cache.internal:6379"
	cfg.RedisStateAddr = "redis-state.internal:6379"
	cfg.AgentPlannerEndpoint = "http://planner.internal/v1/chat/completions"
	cfg.JWTSecret = "12345678901234567890123456789012"
	cfg.S3AccessKey = "prod-access"
	cfg.S3SecretKey = "prod-secret"
	cfg.CORSAllowedOrigins = []string{"*"}
	cfg.PGDSN = "postgres://app:prod@pg.internal:5432/ai_etl"
	cfg.BootstrapAdminPassword = "initial-admin-password"

	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected wildcard CORS to be rejected in production")
	}
}

func TestLoad_RedisCacheStateFallbackToShared(t *testing.T) {
	t.Setenv("REDIS_ADDR", "shared-redis:6379")
	t.Setenv("REDIS_PASSWORD", "shared-pass")
	t.Setenv("REDIS_DB", "3")

	cfg := Load()

	if cfg.RedisCacheAddr != "shared-redis:6379" {
		t.Errorf("cache addr should fall back to REDIS_ADDR, got %q", cfg.RedisCacheAddr)
	}
	if cfg.RedisStateAddr != "shared-redis:6379" {
		t.Errorf("state addr should fall back to REDIS_ADDR, got %q", cfg.RedisStateAddr)
	}
	if cfg.RedisCachePassword != "shared-pass" || cfg.RedisStatePassword != "shared-pass" {
		t.Errorf("passwords should fall back to REDIS_PASSWORD, got cache=%q state=%q",
			cfg.RedisCachePassword, cfg.RedisStatePassword)
	}
	if cfg.RedisCacheDB != 3 || cfg.RedisStateDB != 3 {
		t.Errorf("DBs should fall back to REDIS_DB, got cache=%d state=%d", cfg.RedisCacheDB, cfg.RedisStateDB)
	}
}

func TestLoad_RedisCacheStateOverrideShared(t *testing.T) {
	t.Setenv("REDIS_ADDR", "shared-redis:6379")
	t.Setenv("REDIS_DB", "0")
	t.Setenv("REDIS_CACHE_ADDR", "cache-redis:6379")
	t.Setenv("REDIS_CACHE_DB", "1")
	t.Setenv("REDIS_STATE_ADDR", "state-redis:6379")
	t.Setenv("REDIS_STATE_DB", "2")

	cfg := Load()

	if cfg.RedisCacheAddr != "cache-redis:6379" || cfg.RedisCacheDB != 1 {
		t.Errorf("cache override failed: addr=%q db=%d", cfg.RedisCacheAddr, cfg.RedisCacheDB)
	}
	if cfg.RedisStateAddr != "state-redis:6379" || cfg.RedisStateDB != 2 {
		t.Errorf("state override failed: addr=%q db=%d", cfg.RedisStateAddr, cfg.RedisStateDB)
	}
}

// Durable Agent state must never share an instance with the evictable cache:
// an allkeys-lru policy can drop fencing tokens and approval audit records.
func TestValidate_ProductionRejectsSharedRedisForStateAndCache(t *testing.T) {
	cfg := Load()
	cfg.Environment = "production"
	cfg.EmbedAPIKey = "sk-test"
	cfg.KafkaBrokers = "kafka-prod:9092"
	cfg.StoreEndpoint = "http://qdrant-prod:6333"
	cfg.RedisAddr = "redis-prod:6379"
	cfg.RedisCacheAddr = "redis-prod:6379"
	cfg.RedisCacheDB = 0
	cfg.RedisStateAddr = "redis-prod:6379"
	cfg.RedisStateDB = 0

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error when state and cache share the same Redis instance and DB")
	}
	if !strings.Contains(err.Error(), "REDIS_STATE_ADDR/DB must not equal REDIS_CACHE_ADDR/DB") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidate_ProductionAcceptsSeparateRedisInstances(t *testing.T) {
	cfg := Load()
	cfg.Environment = "production"
	cfg.EmbedAPIKey = "sk-test"
	cfg.KafkaBrokers = "kafka-prod:9092"
	cfg.StoreEndpoint = "http://qdrant-prod:6333"
	cfg.RedisAddr = "redis-cache-prod:6379"
	cfg.RedisCacheAddr = "redis-cache-prod:6379"
	cfg.RedisStateAddr = "redis-state-prod:6379"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("separate cache/state instances should pass validation, got: %v", err)
	}
}

func TestValidate_ProductionRequiresConfiguredStateAddr(t *testing.T) {
	cfg := Load()
	cfg.Environment = "production"
	cfg.EmbedAPIKey = "sk-test"
	cfg.KafkaBrokers = "kafka-prod:9092"
	cfg.StoreEndpoint = "http://qdrant-prod:6333"
	cfg.RedisAddr = "redis-cache-prod:6379"
	cfg.RedisCacheAddr = "redis-cache-prod:6379"
	cfg.RedisStateAddr = "localhost:6379"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for default REDIS_STATE_ADDR in production")
	}
	if !strings.Contains(err.Error(), "REDIS_STATE_ADDR must be configured in production") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestEnvSecret_FromFileFallback(t *testing.T) {
	tmpDir := t.TempDir()
	secretFile := filepath.Join(tmpDir, "embed_api_key")
	if err := os.WriteFile(secretFile, []byte("sk-from-file\n"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}

	t.Setenv("EMBED_API_KEY", "")
	t.Setenv("EMBED_API_KEY_FILE", secretFile)

	if got := EnvSecret("EMBED_API_KEY", ""); got != "sk-from-file" {
		t.Fatalf("expected secret from file, got %q", got)
	}
}

func TestEnvSecret_EnvOverridesFile(t *testing.T) {
	tmpDir := t.TempDir()
	secretFile := filepath.Join(tmpDir, "jwt_secret")
	if err := os.WriteFile(secretFile, []byte("file-secret"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}

	t.Setenv("JWT_SECRET", "env-secret")
	t.Setenv("JWT_SECRET_FILE", secretFile)

	if got := EnvSecret("JWT_SECRET", "default-secret"); got != "env-secret" {
		t.Fatalf("expected env secret to win, got %q", got)
	}
}

func TestLoad_ReadsSecretsFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	jwtFile := filepath.Join(tmpDir, "jwt_secret")
	if err := os.WriteFile(jwtFile, []byte("file-jwt-secret"), 0o600); err != nil {
		t.Fatalf("write jwt secret file: %v", err)
	}
	s3File := filepath.Join(tmpDir, "s3_secret_key")
	if err := os.WriteFile(s3File, []byte("file-s3-secret"), 0o600); err != nil {
		t.Fatalf("write s3 secret file: %v", err)
	}

	t.Setenv("JWT_SECRET", "")
	t.Setenv("JWT_SECRET_FILE", jwtFile)
	t.Setenv("S3_SECRET_KEY", "")
	t.Setenv("S3_SECRET_KEY_FILE", s3File)

	cfg := Load()
	if cfg.JWTSecret != "file-jwt-secret" {
		t.Fatalf("expected JWTSecret from file, got %q", cfg.JWTSecret)
	}
	if cfg.S3SecretKey != "file-s3-secret" {
		t.Fatalf("expected S3SecretKey from file, got %q", cfg.S3SecretKey)
	}
}

func TestEnvCSV(t *testing.T) {
	t.Setenv("TEST_CSV", " a, b , ,c ")
	got := EnvCSV("TEST_CSV", "")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("unexpected csv parse result: %#v", got)
	}
}

func TestLoad_CORSDefaultsByEnvironment(t *testing.T) {
	t.Setenv("ENVIRONMENT", "dev")
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	cfgDev := Load()
	if len(cfgDev.CORSAllowedOrigins) != 1 || cfgDev.CORSAllowedOrigins[0] != "*" {
		t.Fatalf("expected dev default cors '*', got %#v", cfgDev.CORSAllowedOrigins)
	}

	t.Setenv("ENVIRONMENT", "staging")
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	cfgStaging := Load()
	if len(cfgStaging.CORSAllowedOrigins) != 0 {
		t.Fatalf("expected non-dev default cors empty, got %#v", cfgStaging.CORSAllowedOrigins)
	}
}
