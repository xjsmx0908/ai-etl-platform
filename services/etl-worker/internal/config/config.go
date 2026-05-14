// Package config handles application configuration loading from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
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
	ParserEndpoint string

	// Store (Qdrant)
	StoreEndpoint   string
	StoreAPIKey     string
	StoreCollection string

	// Sparse Vector (Hybrid Search / BM25)
	SparseK1    float64 // BM25 k1 parameter
	SparseB     float64 // BM25 b parameter
	SparseAvgDL float64 // Average document length in tokens

	// Kafka
	KafkaBrokers  string
	KafkaTopic    string
	KafkaGroupID  string
	KafkaDLQTopic string

	// Redis (Checkpoint)
	RedisAddr     string
	RedisPassword string
	RedisDB       int

	// Server
	HealthPort int

	// Gateway (file upload)
	UploadDir     string
	MaxUploadSize int64 // bytes

	// Runtime
	Environment string // "dev" | "staging" | "production"
}

// Load reads configuration from environment variables with sensible defaults.
func Load() Config {
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
		EmbedAPIKey:     EnvStr("EMBED_API_KEY", ""),
		EmbedModel:      EnvStr("EMBED_MODEL", "text-embedding-ada-002"),
		EmbedDimension:  EnvInt("EMBED_DIMENSION", 1536),
		EmbedMaxRetries: EnvInt("EMBED_MAX_RETRIES", 5),
		EmbedBackoff:    EnvDuration("EMBED_BACKOFF", 500*time.Millisecond),
		EmbedMaxBackoff: EnvDuration("EMBED_MAX_BACKOFF", 30*time.Second),
		EmbedRateLimit:  EnvFloat("EMBED_RATE_LIMIT", 50.0),

		// Parser Service
		ParserEndpoint: EnvStr("PARSER_ENDPOINT", "http://parser-service:8000"),

		// Store (Qdrant)
		StoreEndpoint:   EnvStr("STORE_ENDPOINT", "http://localhost:6333"),
		StoreAPIKey:     EnvStr("STORE_API_KEY", ""),
		StoreCollection: EnvStr("STORE_COLLECTION", "documents"),

		// Sparse Vector (BM25)
		SparseK1:    EnvFloat("SPARSE_K1", 1.2),
		SparseB:     EnvFloat("SPARSE_B", 0.75),
		SparseAvgDL: EnvFloat("SPARSE_AVG_DL", 256),

		// Kafka
		KafkaBrokers:  EnvStr("KAFKA_BROKERS", "localhost:9092"),
		KafkaTopic:    EnvStr("KAFKA_TOPIC", "doc-processing"),
		KafkaGroupID:  EnvStr("KAFKA_GROUP_ID", "etl-pipeline"),
		KafkaDLQTopic: EnvStr("KAFKA_DLQ_TOPIC", "doc-processing-dlq"),

		// Redis
		RedisAddr:     EnvStr("REDIS_ADDR", "localhost:6379"),
		RedisPassword: EnvStr("REDIS_PASSWORD", ""),
		RedisDB:       EnvInt("REDIS_DB", 0),

		// Server
		HealthPort: EnvInt("HEALTH_PORT", 8080),

		// Gateway
		UploadDir:     EnvStr("UPLOAD_DIR", "/data/uploads"),
		MaxUploadSize: int64(EnvInt("MAX_UPLOAD_SIZE_MB", 512)) * 1024 * 1024,

		// Runtime
		Environment: EnvStr("ENVIRONMENT", "dev"),
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
	}
	if c.MaxWorkers < 1 || c.MaxWorkers > 100 {
		return fmt.Errorf("PIPELINE_MAX_WORKERS must be between 1 and 100, got %d", c.MaxWorkers)
	}
	if c.BatchSize < 1 || c.BatchSize > 100 {
		return fmt.Errorf("PIPELINE_BATCH_SIZE must be between 1 and 100, got %d", c.BatchSize)
	}
	return nil
}

// IsDev returns true if running in development environment.
func (c Config) IsDev() bool {
	return c.Environment == "dev"
}

// --- Environment variable helpers (exported for reuse) ---

// EnvStr reads a string environment variable with a default value.
func EnvStr(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
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

// EnvDuration reads a time.Duration environment variable with a default value.
func EnvDuration(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
}
