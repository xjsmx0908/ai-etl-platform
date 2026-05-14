package config

import (
	"os"
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
