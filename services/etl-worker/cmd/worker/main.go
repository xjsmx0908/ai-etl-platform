// Package main is the ETL Worker service: consumes tasks from Kafka → parses → embeds → stores to Qdrant.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-etl-pipeline/internal/checkpoint"
	"ai-etl-pipeline/internal/circuit"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/embedder"
	"ai-etl-pipeline/internal/es"
	"ai-etl-pipeline/internal/indexmanifest"
	"ai-etl-pipeline/internal/ingestion"
	"ai-etl-pipeline/internal/kafka"
	"ai-etl-pipeline/internal/metrics"
	"ai-etl-pipeline/internal/migrations"
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/pipeline"
	"ai-etl-pipeline/internal/prometheus"
	"ai-etl-pipeline/internal/store"
	"ai-etl-pipeline/internal/taskstatus"
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
	prom := prometheus.New("ai_etl")
	circuit.SetStateObserver(prom.SetCircuitState)
	prom.DLQMessages.WithLabelValues("task_exhausted_retries").Add(0)
	metricsPort := config.EnvInt("WORKER_METRICS_PORT", 8081)
	metricsSrv := startMetricsServer(metricsPort, prom)

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

	taskStatusStore, err := newTaskStatusStore(cfg)
	if err != nil {
		slog.Error("failed to create task status store", "error", err)
		os.Exit(1)
	}
	defer taskStatusStore.Close()

	dlq, err := newDLQ(cfg)
	if err != nil {
		slog.Error("failed to create DLQ", "error", err)
		os.Exit(1)
	}
	dlq = &instrumentedDLQ{delegate: dlq, prom: prom}
	defer dlq.Close()

	fullTextSink, err := newFullTextSink(cfg)
	if err != nil {
		slog.Error("failed to create full-text sink", "error", err)
		os.Exit(1)
	}
	// Surface Qdrant/ES divergence: a chunk reaching ES dead-letter means the
	// vector store has it but the full-text index never will.
	fullTextSink.SetDeadLetterHook(func() {
		prom.ESDeadLetter.WithLabelValues("es").Inc()
	})
	defer func() {
		if fullTextSink != nil {
			_ = fullTextSink.Close()
		}
	}()

	source, err := newTaskSource(cfg, dlq)
	if err != nil {
		slog.Error("failed to create source", "error", err)
		os.Exit(1)
	}
	defer source.Close()

	// Legacy messages can run without PostgreSQL, but durable outbox messages
	// fail closed until their authoritative job can be claimed. Migrations are
	// still applied here so worker and API replicas agree on lifecycle state.
	var docStore docstore.Store
	var ingestionJobs ingestion.JobStore
	var generationBuilder indexmanifest.BuildStarter
	var generationReconciler *indexmanifest.Reconciler
	var generationRetention *indexmanifest.RetentionCollector
	var generationOperations generationOperationsReader
	pgPool, err := migrations.Open(context.Background(), cfg.PGDSN)
	if err != nil {
		slog.Warn("postgres unavailable; document registry status write-through disabled", "error", err)
	} else {
		defer pgPool.Close()
		docStore = docstore.New(pgPool)
		ingestionJobs = ingestion.NewPostgresStore(pgPool)
		qdrantProjection, ok := storer.(indexmanifest.Projection)
		if !ok {
			slog.Warn("generation indexing disabled; vector store lacks generation projection")
		} else {
			manifestStore := indexmanifest.NewPostgresStore(pgPool)
			generationOperations = manifestStore
			elasticsearchProjection := fullTextSink.GenerationProjection()
			generationBuilder = indexmanifest.NewBuilder(manifestStore, qdrantProjection, elasticsearchProjection)
			if cfg.IndexReconcileEnabled {
				generationReconciler = indexmanifest.NewReconciler(manifestStore, qdrantProjection, elasticsearchProjection, indexmanifest.ReconcilerOptions{
					BatchSize: cfg.IndexReconcileBatchSize, Interval: cfg.IndexReconcileInterval,
					Lease: cfg.IndexReconcileLease, MaxRepairs: cfg.IndexReconcileMaxRepairs,
				}).WithObserver(prom)
			}
			if cfg.IndexRetentionEnabled {
				qdrantDeleter, qdrantOK := storer.(indexmanifest.GenerationDeleter)
				elasticsearchDeleter, elasticsearchOK := elasticsearchProjection.(indexmanifest.GenerationDeleter)
				if !qdrantOK || !elasticsearchOK {
					slog.Warn("generation retention disabled; projection lacks exact generation deletion")
				} else {
					generationRetention = indexmanifest.NewRetentionCollector(manifestStore, qdrantDeleter, elasticsearchDeleter, indexmanifest.RetentionOptions{
						Window: cfg.IndexRetentionWindow, Interval: cfg.IndexRetentionInterval,
						Lease: cfg.IndexRetentionLease, BatchSize: cfg.IndexRetentionBatchSize,
					}).WithObserver(prom)
				}
			}
		}
	}

	// Build Pipeline
	p := pipeline.NewWithSinks(cfg, emb, storer, fullTextSink, mc, ckpt, dlq).
		WithTaskStatusStore(taskStatusStore).
		WithDocStore(docStore).
		WithIngestionJobs(ingestionJobs).
		WithGenerationBuilder(generationBuilder)

	// Start Pipeline
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if generationReconciler != nil {
		go generationReconciler.Run(ctx)
	}
	if generationRetention != nil {
		go generationRetention.Run(ctx)
	}
	if generationOperations != nil {
		go runGenerationOperationsMonitor(ctx, generationOperations, prom,
			cfg.IngestionMetricsInterval, cfg.IndexReconcileMaxRepairs)
	}

	p.Run(ctx, source)
	slog.Info("worker running, consuming tasks from Kafka")

	// Graceful shutdown
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	received := <-sig
	slog.Info("shutdown signal received", "signal", received.String())

	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("metrics server shutdown failed", "error", err)
	}

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

type generationOperationsReader interface {
	OperationsSnapshot(context.Context, int) (indexmanifest.OperationsSnapshot, error)
}

type generationOperationsObserver interface {
	SetGenerationOperations(indexmanifest.OperationsSnapshot)
}

func runGenerationOperationsMonitor(ctx context.Context, reader generationOperationsReader, observer generationOperationsObserver, interval time.Duration, maxRepairs int) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		snapshot, err := reader.OperationsSnapshot(ctx, maxRepairs)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("generation operations snapshot failed", "error", err)
			}
		} else {
			observer.SetGenerationOperations(snapshot)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func startMetricsServer(port int, prom *prometheus.Metrics) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/metrics", prom.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           mux,
		ReadTimeout:       5 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}

	go func() {
		slog.Info("worker metrics server started", "port", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("worker metrics server error", "error", err)
		}
	}()
	return srv
}

type instrumentedDLQ struct {
	delegate model.DLQStore
	prom     *prometheus.Metrics
}

func (d *instrumentedDLQ) Push(ctx context.Context, msg model.DLQMessage) error {
	if err := d.delegate.Push(ctx, msg); err != nil {
		return err
	}
	d.prom.DLQMessages.WithLabelValues("task_exhausted_retries").Inc()
	return nil
}

func (d *instrumentedDLQ) List(ctx context.Context) ([]model.DLQMessage, error) {
	return d.delegate.List(ctx)
}

func (d *instrumentedDLQ) Close() error {
	return d.delegate.Close()
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
	return checkpoint.NewRedisStore(cfg.RedisStateAddr, cfg.RedisStatePassword, cfg.RedisStateDB)
}

func newTaskStatusStore(cfg config.Config) (model.TaskStatusStore, error) {
	if cfg.ResolvedTaskStatusStore() == config.TaskStatusStoreMemory {
		return taskstatus.NewMemoryStore(), nil
	}
	return taskstatus.NewRedisStore(cfg.RedisStateAddr, cfg.RedisStatePassword, cfg.RedisStateDB, cfg.TaskStatusTTL)
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

func newFullTextSink(cfg config.Config) (*es.AsyncSink, error) {
	indexer, err := es.NewHTTPIndexer(cfg.ESAddress, cfg.ESAPIKey, cfg.ESIndex)
	if err != nil {
		return nil, err
	}

	queue, err := es.NewRedisRetryQueue(
		cfg.RedisStateAddr,
		cfg.RedisStatePassword,
		cfg.RedisStateDB,
		cfg.ESQueueKey,
		cfg.ESDeadLetterKey,
		cfg.ESReplayPeriod,
	)
	if err != nil {
		_ = indexer.Close()
		return nil, err
	}

	return es.NewAsyncSink(
		indexer,
		queue,
		cfg.ESReplayPeriod,
		cfg.ESMaxRetries,
		cfg.ESRetryBaseBackoff,
		cfg.ESRetryMaxBackoff,
		cfg.ESRetryJitter,
	), nil
}
