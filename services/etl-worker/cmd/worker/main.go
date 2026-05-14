// Package main is the ETL Worker service: consumes tasks from Kafka → parses → embeds → stores to Qdrant.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-etl-pipeline/internal/checkpoint"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/embedder"
	"ai-etl-pipeline/internal/kafka"
	"ai-etl-pipeline/internal/metrics"
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/pipeline"
	"ai-etl-pipeline/internal/store"
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
	slog.Info("starting ai-etl-worker",
		"version", version, "build_time", buildTime,
		"env", cfg.Environment, "workers", cfg.MaxWorkers)

	// Initialize tracing
	tracingShutdown, err := tracing.Init(tracing.Config{
		ServiceName:    "etl-worker",
		ServiceVersion: version,
		Endpoint:       config.EnvStr("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		SampleRatio:    1.0,
	})
	if err != nil {
		slog.Warn("tracing init failed, continuing without tracing", "error", err)
	}
	defer tracingShutdown()

	mc := metrics.NewCollector(500)

	emb, err := embedder.NewHTTPEmbedder(cfg)
	if err != nil {
		slog.Error("failed to create embedder", "error", err)
		os.Exit(1)
	}
	defer emb.Close()

	storer, err := newStorer(cfg)
	if err != nil {
		slog.Error("failed to create storer", "error", err)
		os.Exit(1)
	}
	defer storer.Close()

	ckpt, err := newCheckpoint(cfg)
	if err != nil {
		slog.Error("failed to create checkpoint", "error", err)
		os.Exit(1)
	}
	defer ckpt.Close()

	dlq, err := newDLQ(cfg)
	if err != nil {
		slog.Error("failed to create DLQ", "error", err)
		os.Exit(1)
	}
	defer dlq.Close()

	source, err := newTaskSource(cfg, dlq)
	if err != nil {
		slog.Error("failed to create source", "error", err)
		os.Exit(1)
	}
	defer source.Close()

	// Build Pipeline
	p := pipeline.New(cfg, emb, storer, mc, ckpt, dlq)

	// Start Pipeline
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p.Run(ctx, source)
	slog.Info("worker running, consuming tasks from Kafka")

	// Graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	received := <-sig
	slog.Info("shutdown signal received", "signal", received.String())

	cancel()

	drainDone := make(chan struct{})
	go func() {
		p.Wait()
		close(drainDone)
	}()

	drainTimeout := 30 * time.Second
	select {
	case <-drainDone:
		slog.Info("graceful drain complete")
	case <-time.After(drainTimeout):
		slog.Warn("drain timeout, force shutdown", "timeout", drainTimeout)
	}

	mc.Stop()
	mc.Summary()
	slog.Info("worker shutdown complete")
}

func newStorer(cfg config.Config) (model.Storer, error) {
	if cfg.IsDev() {
		return store.NewMemoryStorer(), nil
	}
	return store.NewQdrantStorer(cfg.StoreEndpoint, cfg.StoreAPIKey, cfg.StoreCollection, cfg.EmbedDimension)
}

func newCheckpoint(cfg config.Config) (model.CheckpointStore, error) {
	if cfg.IsDev() {
		return checkpoint.NewMemoryStore(), nil
	}
	return checkpoint.NewRedisStore(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
}

func newDLQ(cfg config.Config) (model.DLQStore, error) {
	if cfg.IsDev() {
		return kafka.NewMemoryDLQ(), nil
	}
	return kafka.NewDLQ(cfg.KafkaBrokers, cfg.KafkaDLQTopic)
}

func newTaskSource(cfg config.Config, dlq model.DLQStore) (model.TaskSource, error) {
	if cfg.IsDev() {
		tasks := []model.Task{
			{FilePath: "/data/ai-whitepaper.pdf", DocID: "doc-001", TenantID: "tenant-a"},
			{FilePath: "/data/ml-fundamentals.docx", DocID: "doc-002", TenantID: "tenant-a"},
			{FilePath: "/data/deep-learning.md", DocID: "doc-003", TenantID: "tenant-b"},
		}
		return kafka.NewMockSource(tasks, dlq), nil
	}
	return kafka.NewSource(cfg.KafkaBrokers, cfg.KafkaTopic, cfg.KafkaGroupID, dlq)
}
