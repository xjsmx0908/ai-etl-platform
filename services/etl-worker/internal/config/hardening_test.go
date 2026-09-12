package config

import "testing"

func TestValidateAPI_NonDevRequiresMetricsToken(t *testing.T) {
	cfg := Load()
	cfg.Environment = "staging"
	cfg.StoreAPIKey = "staging-qdrant-key"
	cfg.S3AccessKey = "not-minioadmin"
	cfg.S3SecretKey = "not-minioadmin"
	cfg.AgentPlannerEndpoint = "http://planner.internal/v1/chat/completions"
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected missing metrics token to be rejected outside development")
	}
	cfg.MetricsToken = "staging-metrics-token"
	if err := cfg.ValidateAPI(); err != nil {
		t.Fatalf("staging with metrics token and store key should pass: %v", err)
	}
}

func TestValidateAPI_ProductionRejectsEmptyRedisPassword(t *testing.T) {
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
	cfg.PGDSN = "postgres://app:prod@pg.internal:5432/ai_etl"
	cfg.BootstrapAdminPassword = "initial-admin-password"
	cfg.StoreAPIKey = "prod-qdrant-api-key"
	cfg.MetricsToken = "prod-metrics-token-value"
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected empty redis password to be rejected in production")
	}
}

func TestValidateAPI_ProductionRejectsDisabledLoginLimit(t *testing.T) {
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
	applyHardeningSecrets(&cfg)
	cfg.LoginRateLimitPerIP = 0
	cfg.LoginRateLimitPerUser = 0
	if err := cfg.ValidateAPI(); err == nil {
		t.Fatal("expected disabled login rate limit to be rejected in production")
	}
}

func TestLoad_LoginRateLimitZeroDisables(t *testing.T) {
	t.Setenv("LOGIN_RATE_LIMIT", "0")
	cfg := Load()
	if cfg.LoginRateLimitPerIP != 0 || cfg.LoginRateLimitPerUser != 0 {
		t.Fatalf("LOGIN_RATE_LIMIT=0 should disable limits, ip=%d user=%d", cfg.LoginRateLimitPerIP, cfg.LoginRateLimitPerUser)
	}
}
