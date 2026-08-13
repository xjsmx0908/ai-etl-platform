// Package config handles application configuration loading from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	// RerankPolicyAuto routes only fuzzy/semantic searches through reranking.
	RerankPolicyAuto = "auto"
	// RerankPolicyAlways preserves the previous behavior for offline experiments.
	RerankPolicyAlways = "always"

	// AgentPlannerAuto uses rule in development and llm outside development.
	AgentPlannerAuto = "auto"
	// AgentPlannerLLM uses an OpenAI-compatible model endpoint for planning.
	AgentPlannerLLM = "llm"
	// AgentPlannerRule uses a deterministic fixed RAG planning path.
	AgentPlannerRule = "rule"

	// TaskStatusStoreAuto uses memory in dev and redis outside dev.
	TaskStatusStoreAuto = "auto"
	// TaskStatusStoreMemory keeps task status in process memory.
	TaskStatusStoreMemory = "memory"
	// TaskStatusStoreRedis shares task status through Redis.
	TaskStatusStoreRedis = "redis"

	DefaultRetrievalExactSchemaFields = "doc_id,chunk_id,order_id,order_no,contract_id,contract_no,ticket_id,invoice_no,trace_id,request_id,customer_ref,email,phone,sku,user_id"
)

// Config holds all application configuration with environment variable overrides.
type Config struct {
	// Pipeline
	MaxWorkers      int
	TaskBufferSize  int
	StageTimeout    time.Duration
	PipelineTimeout time.Duration
	MaxRetries      int
	RetryBackoff    time.Duration
	BatchSize       int

	// Parser
	MaxChunkSize   int
	ChunkOverlap   int
	ReadBufferSize int

	// Embedder
	EmbedEndpoint   string
	EmbedAPIKey     string
	EmbedModel      string
	EmbedDimension  int
	EmbedMaxRetries int
	EmbedBackoff    time.Duration
	EmbedMaxBackoff time.Duration
	EmbedRateLimit  float64 // requests per second

	// Parser Service
	ParserEndpoint      string
	ParserInternalToken string
	// WorkerHealthURL is the ETL worker's health endpoint, used by the system
	// health aggregation endpoint.
	WorkerHealthURL string

	// Store (Qdrant)
	StoreEndpoint   string
	StoreAPIKey     string
	StoreCollection string

	// Elasticsearch (eventual consistency full-text index)
	ESAddress          string
	ESAPIKey           string
	ESIndex            string
	ESQueueKey         string
	ESDeadLetterKey    string
	ESReplayPeriod     time.Duration
	ESMaxRetries       int
	ESRetryBaseBackoff time.Duration
	ESRetryMaxBackoff  time.Duration
	ESRetryJitter      float64

	// Sparse Vector (Hybrid Search / BM25)
	SparseK1    float64 // BM25 k1 parameter
	SparseB     float64 // BM25 b parameter
	SparseAvgDL float64 // Average document length in tokens

	// Retrieval Gateway (Module 2)
	RetrievalTimeout           time.Duration
	RetrievalCandidateK        int
	RetrievalFinalTopK         int
	RetrievalEnableES          bool
	RetrievalEnableRerank      bool
	RetrievalRerankPolicy      string
	RetrievalExactSchemaFields []string
	// RetrievalMinRelevance gates answer generation on raw backend similarity.
	// Permission filtering can remove the target document while still returning
	// unrelated same-tenant chunks; without this gate the LLM answers from them.
	// 0 disables the gate. Only Qdrant cosine scores are compared against it —
	// BM25 is unbounded and corpus-dependent, so it shares no threshold.
	RetrievalMinRelevance   float64
	RerankEndpoint          string
	RerankAPIKey            string
	RerankModel             string
	SemanticCacheEnabled    bool
	SemanticCacheTTL        time.Duration
	SemanticCacheThreshold  float64
	SemanticCacheMaxEntries int

	// Agent Orchestrator (Module 3)
	AgentNodeID           string
	AgentMaxSteps         int
	AgentLockTTL          time.Duration
	AgentRunTTL           time.Duration
	AgentRunTimeout       time.Duration
	AgentApprovalTimeout  time.Duration
	AgentPlannerType      string
	AgentPlannerEndpoint  string
	AgentPlannerAPIKey    string
	AgentPlannerModel     string
	AgentPlannerTimeout   time.Duration
	AgentPlannerMaxTokens int

	// Kafka
	KafkaBrokers  string
	KafkaTopic    string
	KafkaGroupID  string
	KafkaDLQTopic string

	// Redis (shared default; used as fallback for cache/state below)
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// Redis Cache instance holds only evictable data (semantic retrieval cache).
	// Safe to run with allkeys-lru.
	RedisCacheAddr     string
	RedisCachePassword string
	RedisCacheDB       int

	// Redis State instance holds durable data: Agent runs, approval audit,
	// fencing tokens, idempotency keys, checkpoints, task status, and the ES
	// retry queue. Must run with noeviction; eviction here is a correctness bug,
	// not a capacity issue (a dropped fencing token defeats stale-write rejection).
	RedisStateAddr     string
	RedisStatePassword string
	RedisStateDB       int

	// Server
	HealthPort            int
	HTTPReadTimeout       time.Duration
	HTTPReadHeaderTimeout time.Duration
	HTTPWriteTimeout      time.Duration
	HTTPIdleTimeout       time.Duration
	HTTPMaxHeaderBytes    int
	CORSAllowedOrigins    []string
	IdempotencyTTL        time.Duration
	TaskStatusStore       string
	TaskStatusTTL         time.Duration

	// Gateway (file upload)
	UploadDir               string
	MaxUploadSize           int64 // bytes
	MultipartMaxMemoryBytes int64 // bytes kept in memory before multipart spills to disk
	JWTSecret               string
	S3Endpoint              string
	S3AccessKey             string
	S3SecretKey             string
	S3Bucket                string
	S3UseSSL                bool

	// Runtime
	Environment string // "dev" | "staging" | "production"

	// Prompt versioning: system prompt is loaded from {PromptDir}/rag_answer/{PromptVersion}.md.
	// Empty PromptDir keeps the built-in v1 prompt. Bumping PromptVersion lets prompt changes
	// be tracked and A/B'd through the eval.
	PromptDir     string
	PromptVersion string
}

// Load reads configuration from environment variables with sensible defaults.
func Load() Config {
	environment := EnvStr("ENVIRONMENT", "dev")
	corsDefault := "*"
	if !strings.EqualFold(environment, "dev") {
		corsDefault = ""
	}

	return Config{
		// Pipeline defaults
		MaxWorkers:      EnvInt("PIPELINE_MAX_WORKERS", 10),
		TaskBufferSize:  EnvInt("PIPELINE_TASK_BUFFER", 100),
		StageTimeout:    EnvDuration("PIPELINE_STAGE_TIMEOUT", 30*time.Second),
		PipelineTimeout: EnvDuration("PIPELINE_TIMEOUT", 5*time.Minute),
		MaxRetries:      EnvInt("PIPELINE_MAX_RETRIES", 3),
		RetryBackoff:    EnvDuration("PIPELINE_RETRY_BACKOFF", 500*time.Millisecond),
		BatchSize:       EnvInt("PIPELINE_BATCH_SIZE", 10),

		// Parser
		MaxChunkSize:   EnvInt("PARSER_MAX_CHUNK_SIZE", 4096),
		ChunkOverlap:   EnvInt("PARSER_CHUNK_OVERLAP", 200),
		ReadBufferSize: EnvInt("PARSER_READ_BUFFER", 65536),

		// Embedder
		EmbedEndpoint:   EnvStr("EMBED_ENDPOINT", "https://api.openai.com/v1/embeddings"),
		EmbedAPIKey:     EnvSecret("EMBED_API_KEY", ""),
		EmbedModel:      EnvStr("EMBED_MODEL", "text-embedding-ada-002"),
		EmbedDimension:  EnvInt("EMBED_DIMENSION", 1536),
		EmbedMaxRetries: EnvInt("EMBED_MAX_RETRIES", 5),
		EmbedBackoff:    EnvDuration("EMBED_BACKOFF", 500*time.Millisecond),
		EmbedMaxBackoff: EnvDuration("EMBED_MAX_BACKOFF", 30*time.Second),
		EmbedRateLimit:  EnvFloat("EMBED_RATE_LIMIT", 50.0),

		// Parser Service
		ParserEndpoint:      EnvStr("PARSER_ENDPOINT", "http://parser-service:8000"),
		ParserInternalToken: EnvSecret("PARSER_INTERNAL_TOKEN", ""),
		WorkerHealthURL:     EnvStr("WORKER_HEALTH_URL", "http://etl-worker:8081"),

		// Store (Qdrant)
		StoreEndpoint:   EnvStr("STORE_ENDPOINT", "http://localhost:6333"),
		StoreAPIKey:     EnvSecret("STORE_API_KEY", ""),
		StoreCollection: EnvStr("STORE_COLLECTION", "documents"),

		// Elasticsearch (eventual consistency)
		ESAddress:          EnvStr("ES_ADDRESS", "http://elasticsearch:9200"),
		ESAPIKey:           EnvSecret("ES_API_KEY", ""),
		ESIndex:            EnvStr("ES_INDEX", "documents_text"),
		ESQueueKey:         EnvStr("ES_QUEUE_KEY", "es:index:retry"),
		ESDeadLetterKey:    EnvStr("ES_DEADLETTER_KEY", "es:index:deadletter"),
		ESReplayPeriod:     EnvDuration("ES_REPLAY_PERIOD", 2*time.Second),
		ESMaxRetries:       EnvInt("ES_MAX_RETRIES", 12),
		ESRetryBaseBackoff: EnvDuration("ES_RETRY_BASE_BACKOFF", 2*time.Second),
		ESRetryMaxBackoff:  EnvDuration("ES_RETRY_MAX_BACKOFF", 5*time.Minute),
		ESRetryJitter:      EnvFloat("ES_RETRY_JITTER", 0.2),

		// Sparse Vector (BM25)
		SparseK1:    EnvFloat("SPARSE_K1", 1.2),
		SparseB:     EnvFloat("SPARSE_B", 0.75),
		SparseAvgDL: EnvFloat("SPARSE_AVG_DL", 256),

		// Retrieval Gateway
		RetrievalTimeout:           EnvDuration("RETRIEVAL_TIMEOUT", 300*time.Millisecond),
		RetrievalCandidateK:        EnvInt("RETRIEVAL_CANDIDATE_K", 50),
		RetrievalFinalTopK:         EnvInt("RETRIEVAL_FINAL_TOP_K", 5),
		RetrievalEnableES:          EnvBool("RETRIEVAL_ENABLE_ES", true),
		RetrievalEnableRerank:      EnvBool("RETRIEVAL_ENABLE_RERANK", false),
		RetrievalRerankPolicy:      strings.ToLower(strings.TrimSpace(EnvStr("RETRIEVAL_RERANK_POLICY", RerankPolicyAuto))),
		RetrievalExactSchemaFields: EnvCSV("RETRIEVAL_EXACT_SCHEMA_FIELDS", DefaultRetrievalExactSchemaFields),
		RetrievalMinRelevance:      EnvFloat("RETRIEVAL_MIN_RELEVANCE", 0),
		RerankEndpoint:             EnvStr("RERANK_ENDPOINT", ""),
		RerankAPIKey:               EnvSecret("RERANK_API_KEY", ""),
		RerankModel:                EnvStr("RERANK_MODEL", "bge-reranker-base"),
		SemanticCacheEnabled:       EnvBool("SEMANTIC_CACHE_ENABLED", true),
		SemanticCacheTTL:           EnvDuration("SEMANTIC_CACHE_TTL", 10*time.Minute),
		SemanticCacheThreshold:     EnvFloat("SEMANTIC_CACHE_THRESHOLD", 0.92),
		SemanticCacheMaxEntries:    EnvInt("SEMANTIC_CACHE_MAX_ENTRIES", 128),

		// Agent Orchestrator
		AgentNodeID:           EnvStr("AGENT_NODE_ID", "agent-api-1"),
		AgentMaxSteps:         EnvInt("AGENT_MAX_STEPS", 8),
		AgentLockTTL:          EnvDuration("AGENT_LOCK_TTL", 30*time.Second),
		AgentRunTTL:           EnvDuration("AGENT_RUN_TTL", 24*time.Hour),
		AgentRunTimeout:       EnvDuration("AGENT_RUN_TIMEOUT", 30*time.Minute),
		AgentApprovalTimeout:  EnvDuration("AGENT_APPROVAL_TIMEOUT", 15*time.Minute),
		AgentPlannerType:      strings.ToLower(strings.TrimSpace(EnvStr("AGENT_PLANNER_TYPE", AgentPlannerAuto))),
		AgentPlannerEndpoint:  EnvStr("AGENT_PLANNER_ENDPOINT", EnvStr("LLM_ENDPOINT", "https://api.openai.com/v1/chat/completions")),
		AgentPlannerAPIKey:    EnvSecret("AGENT_PLANNER_API_KEY", EnvSecret("LLM_API_KEY", "")),
		AgentPlannerModel:     EnvStr("AGENT_PLANNER_MODEL", EnvStr("LLM_MODEL", "deepseek-v4-flash")),
		AgentPlannerTimeout:   EnvDuration("AGENT_PLANNER_TIMEOUT", 30*time.Second),
		AgentPlannerMaxTokens: EnvInt("AGENT_PLANNER_MAX_TOKENS", 512),

		// Kafka
		KafkaBrokers:  EnvStr("KAFKA_BROKERS", "localhost:9092"),
		KafkaTopic:    EnvStr("KAFKA_TOPIC", "doc-processing"),
		KafkaGroupID:  EnvStr("KAFKA_GROUP_ID", "etl-pipeline"),
		KafkaDLQTopic: EnvStr("KAFKA_DLQ_TOPIC", "doc-processing-dlq"),

		// Redis: REDIS_* is the shared default; REDIS_CACHE_*/REDIS_STATE_*
		// override it so evictable cache and durable state can be separated.
		RedisAddr:     EnvStr("REDIS_ADDR", "localhost:6379"),
		RedisPassword: EnvSecret("REDIS_PASSWORD", ""),
		RedisDB:       EnvInt("REDIS_DB", 0),

		RedisCacheAddr:     EnvStr("REDIS_CACHE_ADDR", EnvStr("REDIS_ADDR", "localhost:6379")),
		RedisCachePassword: EnvSecret("REDIS_CACHE_PASSWORD", EnvSecret("REDIS_PASSWORD", "")),
		RedisCacheDB:       EnvInt("REDIS_CACHE_DB", EnvInt("REDIS_DB", 0)),

		RedisStateAddr:     EnvStr("REDIS_STATE_ADDR", EnvStr("REDIS_ADDR", "localhost:6379")),
		RedisStatePassword: EnvSecret("REDIS_STATE_PASSWORD", EnvSecret("REDIS_PASSWORD", "")),
		RedisStateDB:       EnvInt("REDIS_STATE_DB", EnvInt("REDIS_DB", 0)),

		// Server
		HealthPort:            EnvInt("HEALTH_PORT", 8080),
		HTTPReadTimeout:       EnvDuration("HTTP_READ_TIMEOUT", 15*time.Second),
		HTTPReadHeaderTimeout: EnvDuration("HTTP_READ_HEADER_TIMEOUT", 10*time.Second),
		HTTPWriteTimeout:      EnvDuration("HTTP_WRITE_TIMEOUT", 60*time.Second),
		HTTPIdleTimeout:       EnvDuration("HTTP_IDLE_TIMEOUT", 120*time.Second),
		HTTPMaxHeaderBytes:    EnvInt("HTTP_MAX_HEADER_BYTES", 1<<20),
		CORSAllowedOrigins:    EnvCSV("CORS_ALLOWED_ORIGINS", corsDefault),
		IdempotencyTTL:        EnvDuration("IDEMPOTENCY_TTL", 24*time.Hour),
		TaskStatusStore:       strings.ToLower(strings.TrimSpace(EnvStr("TASK_STATUS_STORE", TaskStatusStoreAuto))),
		TaskStatusTTL:         EnvDuration("TASK_STATUS_TTL", 7*24*time.Hour),

		// Gateway
		UploadDir:               EnvStr("UPLOAD_DIR", "/data/uploads"),
		MaxUploadSize:           int64(EnvInt("MAX_UPLOAD_SIZE_MB", 512)) * 1024 * 1024,
		MultipartMaxMemoryBytes: int64(EnvInt("MULTIPART_MAX_MEMORY_MB", 4)) * 1024 * 1024,
		JWTSecret:               EnvSecret("JWT_SECRET", "change-me-in-production"),
		S3Endpoint:              EnvStr("S3_ENDPOINT", "localhost:9000"),
		S3AccessKey:             EnvSecret("S3_ACCESS_KEY", "minioadmin"),
		S3SecretKey:             EnvSecret("S3_SECRET_KEY", "minioadmin"),
		S3Bucket:                EnvStr("S3_BUCKET", "documents"),
		S3UseSSL:                strings.EqualFold(EnvStr("S3_USE_SSL", "false"), "true"),

		// Runtime
		Environment: environment,

		// Prompt versioning (optional; empty PromptDir keeps built-in v1)
		PromptDir:     EnvStr("PROMPT_DIR", ""),
		PromptVersion: EnvStr("PROMPT_VERSION", "v1"),
	}
}

// Validate checks required configuration for production environments.
func (c Config) Validate() error {
	if c.Environment == "production" {
		if c.EmbedAPIKey == "" {
			return fmt.Errorf("EMBED_API_KEY is required in production")
		}
		if c.KafkaBrokers == "localhost:9092" {
			return fmt.Errorf("KAFKA_BROKERS must be configured in production")
		}
		if c.StoreEndpoint == "http://localhost:6333" {
			return fmt.Errorf("STORE_ENDPOINT must be configured in production")
		}
		if c.RedisAddr == "localhost:6379" {
			return fmt.Errorf("REDIS_ADDR must be configured in production")
		}
		if c.RedisStateAddr == "localhost:6379" {
			return fmt.Errorf("REDIS_STATE_ADDR must be configured in production")
		}
		if c.RedisCacheAddr == "localhost:6379" {
			return fmt.Errorf("REDIS_CACHE_ADDR must be configured in production")
		}
		// Durable state must not share an instance with the evictable cache:
		// an LRU eviction policy can drop Agent runs, approval audit records,
		// and fencing tokens.
		if c.RedisStateAddr == c.RedisCacheAddr && c.RedisStateDB == c.RedisCacheDB {
			return fmt.Errorf("REDIS_STATE_ADDR/DB must not equal REDIS_CACHE_ADDR/DB in production: durable Agent state cannot share an evictable cache instance")
		}
	}
	if c.MaxWorkers < 1 || c.MaxWorkers > 100 {
		return fmt.Errorf("PIPELINE_MAX_WORKERS must be between 1 and 100, got %d", c.MaxWorkers)
	}
	if c.BatchSize < 1 || c.BatchSize > 100 {
		return fmt.Errorf("PIPELINE_BATCH_SIZE must be between 1 and 100, got %d", c.BatchSize)
	}
	if c.MultipartMaxMemoryBytes < 1<<20 || c.MultipartMaxMemoryBytes > 64<<20 {
		return fmt.Errorf("MULTIPART_MAX_MEMORY_MB must be between 1 and 64, got %d bytes", c.MultipartMaxMemoryBytes)
	}
	if c.ESReplayPeriod <= 0 {
		return fmt.Errorf("ES_REPLAY_PERIOD must be > 0, got %s", c.ESReplayPeriod)
	}
	if c.ESMaxRetries < 1 {
		return fmt.Errorf("ES_MAX_RETRIES must be >= 1, got %d", c.ESMaxRetries)
	}
	if strings.TrimSpace(c.ESQueueKey) == "" {
		return fmt.Errorf("ES_QUEUE_KEY is required")
	}
	if strings.TrimSpace(c.ESDeadLetterKey) == "" {
		return fmt.Errorf("ES_DEADLETTER_KEY is required")
	}
	if c.ESRetryBaseBackoff <= 0 {
		return fmt.Errorf("ES_RETRY_BASE_BACKOFF must be > 0, got %s", c.ESRetryBaseBackoff)
	}
	if c.ESRetryMaxBackoff <= 0 {
		return fmt.Errorf("ES_RETRY_MAX_BACKOFF must be > 0, got %s", c.ESRetryMaxBackoff)
	}
	if c.ESRetryMaxBackoff < c.ESRetryBaseBackoff {
		return fmt.Errorf("ES_RETRY_MAX_BACKOFF must be >= ES_RETRY_BASE_BACKOFF, got max=%s base=%s", c.ESRetryMaxBackoff, c.ESRetryBaseBackoff)
	}
	if c.ESRetryJitter < 0 || c.ESRetryJitter > 1 {
		return fmt.Errorf("ES_RETRY_JITTER must be between 0 and 1, got %v", c.ESRetryJitter)
	}
	if c.RetrievalTimeout <= 0 {
		return fmt.Errorf("RETRIEVAL_TIMEOUT must be > 0, got %s", c.RetrievalTimeout)
	}
	if c.RetrievalCandidateK < 1 || c.RetrievalCandidateK > 500 {
		return fmt.Errorf("RETRIEVAL_CANDIDATE_K must be between 1 and 500, got %d", c.RetrievalCandidateK)
	}
	if c.RetrievalMinRelevance < 0 || c.RetrievalMinRelevance > 1 {
		return fmt.Errorf("RETRIEVAL_MIN_RELEVANCE must be between 0 and 1, got %v", c.RetrievalMinRelevance)
	}
	if c.RetrievalFinalTopK < 1 || c.RetrievalFinalTopK > 100 {
		return fmt.Errorf("RETRIEVAL_FINAL_TOP_K must be between 1 and 100, got %d", c.RetrievalFinalTopK)
	}
	switch strings.ToLower(strings.TrimSpace(c.RetrievalRerankPolicy)) {
	case RerankPolicyAuto, RerankPolicyAlways:
	default:
		return fmt.Errorf("RETRIEVAL_RERANK_POLICY must be %q or %q, got %q", RerankPolicyAuto, RerankPolicyAlways, c.RetrievalRerankPolicy)
	}
	for _, field := range c.RetrievalExactSchemaFields {
		if !validSchemaFieldName(field) {
			return fmt.Errorf("RETRIEVAL_EXACT_SCHEMA_FIELDS contains invalid field %q", field)
		}
	}
	if c.SemanticCacheTTL <= 0 {
		return fmt.Errorf("SEMANTIC_CACHE_TTL must be > 0, got %s", c.SemanticCacheTTL)
	}
	if c.SemanticCacheThreshold <= 0 || c.SemanticCacheThreshold > 1 {
		return fmt.Errorf("SEMANTIC_CACHE_THRESHOLD must be in (0, 1], got %v", c.SemanticCacheThreshold)
	}
	if c.SemanticCacheMaxEntries < 1 || c.SemanticCacheMaxEntries > 10000 {
		return fmt.Errorf("SEMANTIC_CACHE_MAX_ENTRIES must be between 1 and 10000, got %d", c.SemanticCacheMaxEntries)
	}
	if c.TaskStatusTTL <= 0 {
		return fmt.Errorf("TASK_STATUS_TTL must be > 0, got %s", c.TaskStatusTTL)
	}
	switch strings.ToLower(strings.TrimSpace(c.TaskStatusStore)) {
	case TaskStatusStoreAuto, TaskStatusStoreMemory, TaskStatusStoreRedis:
	default:
		return fmt.Errorf("TASK_STATUS_STORE must be %q, %q, or %q, got %q", TaskStatusStoreAuto, TaskStatusStoreMemory, TaskStatusStoreRedis, c.TaskStatusStore)
	}
	return nil
}

func (c Config) validateAgentConfig() error {
	if strings.TrimSpace(c.AgentNodeID) == "" {
		return fmt.Errorf("AGENT_NODE_ID is required")
	}
	if c.AgentMaxSteps < 1 || c.AgentMaxSteps > 64 {
		return fmt.Errorf("AGENT_MAX_STEPS must be between 1 and 64, got %d", c.AgentMaxSteps)
	}
	if c.AgentLockTTL <= 0 {
		return fmt.Errorf("AGENT_LOCK_TTL must be > 0, got %s", c.AgentLockTTL)
	}
	if c.AgentRunTTL <= 0 {
		return fmt.Errorf("AGENT_RUN_TTL must be > 0, got %s", c.AgentRunTTL)
	}
	if c.AgentRunTimeout <= 0 {
		return fmt.Errorf("AGENT_RUN_TIMEOUT must be > 0, got %s", c.AgentRunTimeout)
	}
	if c.AgentApprovalTimeout <= 0 {
		return fmt.Errorf("AGENT_APPROVAL_TIMEOUT must be > 0, got %s", c.AgentApprovalTimeout)
	}
	switch strings.ToLower(strings.TrimSpace(c.AgentPlannerType)) {
	case AgentPlannerAuto, AgentPlannerLLM, AgentPlannerRule:
	default:
		return fmt.Errorf("AGENT_PLANNER_TYPE must be %q, %q, or %q, got %q", AgentPlannerAuto, AgentPlannerLLM, AgentPlannerRule, c.AgentPlannerType)
	}
	if c.AgentPlannerTimeout <= 0 {
		return fmt.Errorf("AGENT_PLANNER_TIMEOUT must be > 0, got %s", c.AgentPlannerTimeout)
	}
	if c.AgentPlannerMaxTokens < 1 || c.AgentPlannerMaxTokens > 8192 {
		return fmt.Errorf("AGENT_PLANNER_MAX_TOKENS must be between 1 and 8192, got %d", c.AgentPlannerMaxTokens)
	}
	if c.ResolvedAgentPlannerType() == AgentPlannerLLM {
		if strings.TrimSpace(c.AgentPlannerEndpoint) == "" {
			return fmt.Errorf("AGENT_PLANNER_ENDPOINT is required for llm planner")
		}
		if strings.TrimSpace(c.AgentPlannerModel) == "" {
			return fmt.Errorf("AGENT_PLANNER_MODEL is required for llm planner")
		}
		if endpointRequiresAPIKey(c.AgentPlannerEndpoint) && strings.TrimSpace(c.AgentPlannerAPIKey) == "" {
			return fmt.Errorf("AGENT_PLANNER_API_KEY is required for OpenAI planner endpoint")
		}
	}
	if c.Environment == "production" {
		if c.ResolvedAgentPlannerType() == AgentPlannerRule {
			return fmt.Errorf("AGENT_PLANNER_TYPE=rule is not allowed in production")
		}
	}
	return nil
}

// IsDev returns true if running in development environment.
func (c Config) IsDev() bool {
	return c.Environment == "dev"
}

// ResolvedAgentPlannerType returns the concrete planner selected by config.
func (c Config) ResolvedAgentPlannerType() string {
	switch strings.ToLower(strings.TrimSpace(c.AgentPlannerType)) {
	case AgentPlannerLLM:
		return AgentPlannerLLM
	case AgentPlannerRule:
		return AgentPlannerRule
	default:
		if c.IsDev() {
			return AgentPlannerRule
		}
		return AgentPlannerLLM
	}
}

// ResolvedTaskStatusStore returns the concrete task status store selected by config.
func (c Config) ResolvedTaskStatusStore() string {
	switch strings.ToLower(strings.TrimSpace(c.TaskStatusStore)) {
	case TaskStatusStoreMemory:
		return TaskStatusStoreMemory
	case TaskStatusStoreRedis:
		return TaskStatusStoreRedis
	default:
		if c.IsDev() {
			return TaskStatusStoreMemory
		}
		return TaskStatusStoreRedis
	}
}

// ValidateAPI extends base validation for Query API specific security requirements.
func (c Config) ValidateAPI() error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := c.validateAgentConfig(); err != nil {
		return err
	}
	if c.Environment != "production" {
		return nil
	}
	if weakSecret(c.JWTSecret) || len(c.JWTSecret) < 32 {
		return fmt.Errorf("JWT_SECRET must be strong in production (>=32 chars and not default value)")
	}
	if c.S3AccessKey == "minioadmin" || c.S3SecretKey == "minioadmin" {
		return fmt.Errorf("S3 credentials must not use default values in production")
	}
	for _, origin := range c.CORSAllowedOrigins {
		if origin == "*" {
			return fmt.Errorf("CORS_ALLOWED_ORIGINS must not contain wildcard '*' in production")
		}
	}
	return nil
}

func weakSecret(secret string) bool {
	v := strings.TrimSpace(secret)
	switch v {
	case "", "change-me-in-production", "your-jwt-secret-change-in-production", "dev-secret", "secret":
		return true
	default:
		return false
	}
}

func validSchemaFieldName(raw string) bool {
	field := strings.TrimSpace(raw)
	if field == "" || len(field) > 64 {
		return false
	}
	for _, r := range field {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func endpointRequiresAPIKey(endpoint string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(endpoint)), "api.openai.com")
}

// --- Environment variable helpers (exported for reuse) ---

// EnvStr reads a string environment variable with a default value.
func EnvStr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// EnvSecret reads sensitive values from KEY or KEY_FILE.
// Priority: KEY > KEY_FILE > default.
func EnvSecret(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	path := os.Getenv(key + "_FILE")
	if path == "" {
		return defaultVal
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return defaultVal
	}

	secret := strings.TrimSpace(string(content))
	if secret == "" {
		return defaultVal
	}
	return secret
}

// EnvInt reads an integer environment variable with a default value.
func EnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return defaultVal
}

// EnvFloat reads a float64 environment variable with a default value.
func EnvFloat(key string, defaultVal float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return defaultVal
}

// EnvBool reads a boolean environment variable with a default value.
func EnvBool(key string, defaultVal bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return defaultVal
}

// EnvDuration reads a time.Duration environment variable with a default value.
func EnvDuration(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
}

// EnvCSV reads a comma-separated environment variable and returns trimmed values.
func EnvCSV(key, defaultVal string) []string {
	raw := EnvStr(key, defaultVal)
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}

	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		v := strings.TrimSpace(part)
		if v != "" {
			values = append(values, v)
		}
	}
	return values
}
