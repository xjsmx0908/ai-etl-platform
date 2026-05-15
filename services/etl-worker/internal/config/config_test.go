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
}

func TestLoad_EnvOverride(t *testing.T) {
	os.Setenv("PIPELINE_MAX_WORKERS", "20")
	os.Setenv("ENVIRONMENT", "production")
	os.Setenv("EMBED_API_KEY", "sk-test")
	defer func() {
		os.Unsetenv("PIPELINE_MAX_WORKERS")
		os.Unsetenv("ENVIRONMENT")
		os.Unsetenv("EMBED_API_KEY")
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
