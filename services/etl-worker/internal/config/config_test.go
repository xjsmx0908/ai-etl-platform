package config

import (
	"os"
	"path/filepath"
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
	if cfg.IdempotencyTTL != 24*time.Hour {
		t.Errorf("expected IdempotencyTTL=24h, got %v", cfg.IdempotencyTTL)
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
	if cfg.AgentPlannerType != AgentPlannerAuto {
		t.Errorf("expected AgentPlannerType=auto, got %s", cfg.AgentPlannerType)
	}
	if cfg.ResolvedAgentPlannerType() != AgentPlannerRule {
		t.Errorf("expected dev auto planner to resolve to rule, got %s", cfg.ResolvedAgentPlannerType())
	}
	if cfg.AgentPlannerEndpoint != "https://api.openai.com/v1/chat/completions" {
		t.Errorf("expected default AgentPlannerEndpoint, got %s", cfg.AgentPlannerEndpoint)
	}
	if cfg.AgentPlannerModel != "gpt-4o-mini" {
		t.Errorf("expected default AgentPlannerModel, got %s", cfg.AgentPlannerModel)
	}
	if cfg.AgentPlannerTimeout != 30*time.Second {
		t.Errorf("expected AgentPlannerTimeout=30s, got %v", cfg.AgentPlannerTimeout)
	}
	if cfg.AgentPlannerMaxTokens != 512 {
		t.Errorf("expected AgentPlannerMaxTokens=512, got %d", cfg.AgentPlannerMaxTokens)
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	os.Setenv("PIPELINE_MAX_WORKERS", "20")
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
	os.Setenv("AGENT_PLANNER_TYPE", "llm")
	os.Setenv("AGENT_PLANNER_ENDPOINT", "http://planner:8080/v1/chat/completions")
	os.Setenv("AGENT_PLANNER_API_KEY", "planner-key")
	os.Setenv("AGENT_PLANNER_MODEL", "planner-model")
	os.Setenv("AGENT_PLANNER_TIMEOUT", "12s")
	os.Setenv("AGENT_PLANNER_MAX_TOKENS", "768")
	defer func() {
		os.Unsetenv("PIPELINE_MAX_WORKERS")
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
		os.Unsetenv("AGENT_PLANNER_TYPE")
		os.Unsetenv("AGENT_PLANNER_ENDPOINT")
		os.Unsetenv("AGENT_PLANNER_API_KEY")
		os.Unsetenv("AGENT_PLANNER_MODEL")
		os.Unsetenv("AGENT_PLANNER_TIMEOUT")
		os.Unsetenv("AGENT_PLANNER_MAX_TOKENS")
	}()

	cfg := Load()

	if cfg.MaxWorkers != 20 {
		t.Errorf("expected MaxWorkers=20, got %d", cfg.MaxWorkers)
	}
	if cfg.Environment != "production" {
		t.Errorf("expected Environment=production, got %s", cfg.Environment)
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
}

func TestValidate_DevMode(t *testing.T) {
	cfg := Load()
	if err := cfg.Validate(); err != nil {
		t.Errorf("dev mode should pass validation, got: %v", err)
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
	cfg.AgentNodeID = ""
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for empty AgentNodeID")
	}

	cfg = Load()
	cfg.AgentMaxSteps = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for AgentMaxSteps < 1")
	}

	cfg = Load()
	cfg.AgentLockTTL = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for AgentLockTTL <= 0")
	}

	cfg = Load()
	cfg.AgentRunTTL = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for AgentRunTTL <= 0")
	}

	cfg = Load()
	cfg.AgentPlannerType = "random"
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid AgentPlannerType")
	}

	cfg = Load()
	cfg.AgentPlannerType = AgentPlannerLLM
	cfg.AgentPlannerEndpoint = ""
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for empty AgentPlannerEndpoint with llm planner")
	}

	cfg = Load()
	cfg.AgentPlannerType = AgentPlannerLLM
	cfg.AgentPlannerModel = ""
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for empty AgentPlannerModel with llm planner")
	}

	cfg = Load()
	cfg.AgentPlannerTimeout = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for AgentPlannerTimeout <= 0")
	}

	cfg = Load()
	cfg.AgentPlannerMaxTokens = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for AgentPlannerMaxTokens < 1")
	}
}

func TestValidate_ProductionAgentPlanner(t *testing.T) {
	cfg := Load()
	cfg.Environment = "production"
	cfg.EmbedAPIKey = "embed-key"
	cfg.KafkaBrokers = "kafka:9092"
	cfg.StoreEndpoint = "http://qdrant:6333"
	cfg.RedisAddr = "redis:6379"
	cfg.AgentPlannerType = AgentPlannerAuto
	cfg.AgentPlannerEndpoint = "http://planner:8080/v1/chat/completions"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected production auto planner to resolve to llm, got %v", err)
	}
	if cfg.ResolvedAgentPlannerType() != AgentPlannerLLM {
		t.Fatalf("expected production auto planner to resolve to llm, got %s", cfg.ResolvedAgentPlannerType())
	}

	cfg.AgentPlannerType = AgentPlannerRule
	if err := cfg.Validate(); err == nil {
		t.Error("expected production rule planner to be rejected")
	}

	cfg.AgentPlannerType = AgentPlannerLLM
	cfg.AgentPlannerEndpoint = "https://api.openai.com/v1/chat/completions"
	cfg.AgentPlannerAPIKey = ""
	if err := cfg.Validate(); err == nil {
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
	cfg.RedisAddr = "redis.internal:6379"
	cfg.AgentPlannerEndpoint = "http://planner.internal/v1/chat/completions"
	cfg.JWTSecret = "change-me-in-production"
	cfg.S3AccessKey = "prod-access"
	cfg.S3SecretKey = "prod-secret"

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
	cfg.RedisAddr = "redis.internal:6379"
	cfg.AgentPlannerEndpoint = "http://planner.internal/v1/chat/completions"
	cfg.JWTSecret = "12345678901234567890123456789012"
	cfg.S3AccessKey = "minioadmin"
	cfg.S3SecretKey = "minioadmin"

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
	cfg.RedisAddr = "redis.internal:6379"
	cfg.AgentPlannerEndpoint = "http://planner.internal/v1/chat/completions"
	cfg.JWTSecret = "12345678901234567890123456789012"
	cfg.S3AccessKey = "prod-access"
	cfg.S3SecretKey = "prod-secret"
	cfg.CORSAllowedOrigins = []string{"https://console.example.com"}

	if err := cfg.ValidateAPI(); err != nil {
		t.Fatalf("expected strong production config to pass, got: %v", err)
	}
}

func TestValidateAPI_ProductionWildcardCORSRejected(t *testing.T) {
	cfg := Load()
	cfg.Environment = "production"
	cfg.EmbedAPIKey = "sk-test"
	cfg.KafkaBrokers = "kafka.internal:9092"
	cfg.StoreEndpoint = "http://qdrant.internal:6333"
	cfg.RedisAddr = "redis.internal:6379"
	cfg.AgentPlannerEndpoint = "http://planner.internal/v1/chat/completions"
	cfg.JWTSecret = "12345678901234567890123456789012"
	cfg.S3AccessKey = "prod-access"
	cfg.S3SecretKey = "prod-secret"
	cfg.CORSAllowedOrigins = []string{"*"}

	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected wildcard CORS to be rejected in production")
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
