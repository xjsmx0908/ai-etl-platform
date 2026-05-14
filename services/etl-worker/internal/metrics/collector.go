// Package metrics provides asynchronous, non-blocking observability collection.
package metrics

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"

	"ai-etl-pipeline/internal/model"
)

// Collector aggregates processing metrics with lock-free atomic counters.
type Collector struct {
	ch chan model.Metric
	wg sync.WaitGroup

	// Prometheus-compatible counters (atomic, lock-free)
	chunksProcessed atomic.Int64
	chunksFailed    atomic.Int64
	tokensUsed      atomic.Int64
	totalLatencyMs  atomic.Int64

	// Per-stage counters
	parseCount atomic.Int64
	embedCount atomic.Int64
	storeCount atomic.Int64
}

// NewCollector creates a metrics collector with the given buffer size.
func NewCollector(bufSize int) *Collector {
	mc := &Collector{ch: make(chan model.Metric, bufSize)}
	mc.wg.Add(1)
	go mc.loop()
	return mc
}

// Emit sends a metric event. Non-blocking; drops if buffer is full.
func (mc *Collector) Emit(m model.Metric) {
	select {
	case mc.ch <- m:
	default:
		slog.Warn("metrics channel full, dropping",
			"chunk_id", m.ChunkID, "stage", m.Stage)
	}
}

// Stop closes the collector and waits for pending metrics to flush.
func (mc *Collector) Stop() {
	close(mc.ch)
	mc.wg.Wait()
}

// Snapshot returns current metric values for the /metrics endpoint.
func (mc *Collector) Snapshot() map[string]int64 {
	return map[string]int64{
		"chunks_processed": mc.chunksProcessed.Load(),
		"chunks_failed":    mc.chunksFailed.Load(),
		"tokens_used":      mc.tokensUsed.Load(),
		"avg_latency_ms":   mc.avgLatencyMs(),
		"parse_count":      mc.parseCount.Load(),
		"embed_count":      mc.embedCount.Load(),
		"store_count":      mc.storeCount.Load(),
	}
}

// Summary prints a human-readable metrics summary to stdout.
func (mc *Collector) Summary() {
	snap := mc.Snapshot()
	fmt.Printf("\n===== Metrics =====\n")
	fmt.Printf("  processed: %d  failed: %d  tokens: %d  avg_latency: %dms\n",
		snap["chunks_processed"], snap["chunks_failed"],
		snap["tokens_used"], snap["avg_latency_ms"])
	fmt.Printf("  stages → parse: %d  embed: %d  store: %d\n",
		snap["parse_count"], snap["embed_count"], snap["store_count"])
	fmt.Printf("===================\n")
}

func (mc *Collector) loop() {
	defer mc.wg.Done()
	for m := range mc.ch {
		mc.chunksProcessed.Add(1)
		mc.tokensUsed.Add(int64(m.TokenUsed))
		mc.totalLatencyMs.Add(m.Duration.Milliseconds())

		if !m.Success {
			mc.chunksFailed.Add(1)
		}

		switch m.Stage {
		case "parse":
			mc.parseCount.Add(1)
		case "embed":
			mc.embedCount.Add(1)
		case "store":
			mc.storeCount.Add(1)
		}

		if !m.Success {
			slog.Error("chunk processing failed",
				"chunk_id", m.ChunkID, "doc_id", m.DocID,
				"stage", m.Stage, "duration_ms", m.Duration.Milliseconds(),
				"error", m.Error)
		} else {
			slog.Debug("chunk processed",
				"chunk_id", m.ChunkID, "stage", m.Stage,
				"duration_ms", m.Duration.Milliseconds(), "tokens", m.TokenUsed)
		}
	}
}

func (mc *Collector) avgLatencyMs() int64 {
	total := mc.chunksProcessed.Load()
	if total == 0 {
		return 0
	}
	return mc.totalLatencyMs.Load() / total
}

// InitLogger initializes the global structured logger based on environment.
func InitLogger(env string) {
	var handler slog.Handler
	opts := &slog.HandlerOptions{}

	if env == "production" || env == "staging" {
		opts.Level = slog.LevelInfo
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		opts.Level = slog.LevelDebug
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)
}
