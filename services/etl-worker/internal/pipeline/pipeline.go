// Package pipeline implements the worker pool orchestrator with panic recovery and graceful drain.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/metrics"
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/parser"
	"ai-etl-pipeline/internal/sparse"
)

// Pipeline orchestrates concurrent document processing with panic recovery.
type Pipeline struct {
	cfg           config.Config
	parser        *parser.Parser
	embedder      model.Embedder
	storer        model.Storer
	fullTextSink  model.FullTextSink
	metrics       *metrics.Collector
	checkpoint    model.CheckpointStore
	dlq           model.DLQStore
	sparseEncoder *sparse.Encoder

	taskCh  chan model.TaskWithAck
	wg      sync.WaitGroup
	running atomic.Bool
}

// New creates a Pipeline with all dependencies injected.
func New(cfg config.Config, emb model.Embedder, st model.Storer, m *metrics.Collector, ckpt model.CheckpointStore, dlq model.DLQStore) *Pipeline {
	return NewWithSinks(cfg, emb, st, nil, m, ckpt, dlq)
}

// NewWithSinks creates Pipeline with optional eventual-consistency sinks.
func NewWithSinks(cfg config.Config, emb model.Embedder, st model.Storer, ft model.FullTextSink, m *metrics.Collector, ckpt model.CheckpointStore, dlq model.DLQStore) *Pipeline {
	sparseEnc := sparse.NewEncoder(sparse.Params{
		K1:     cfg.SparseK1,
		B:      cfg.SparseB,
		AvgDL:  cfg.SparseAvgDL,
		MinTF:  1,
		MaxDim: 30000,
	})
	slog.Info("sparse encoder initialized", "k1", cfg.SparseK1, "b", cfg.SparseB)

	return &Pipeline{
		cfg:           cfg,
		parser:        parser.New(cfg, m),
		embedder:      emb,
		storer:        st,
		fullTextSink:  ft,
		metrics:       m,
		checkpoint:    ckpt,
		dlq:           dlq,
		sparseEncoder: sparseEnc,
		taskCh:        make(chan model.TaskWithAck, cfg.TaskBufferSize),
	}
}

// Run starts consuming from the source and launches the worker pool.
func (p *Pipeline) Run(ctx context.Context, source model.TaskSource) {
	p.running.Store(true)
	slog.Info("pipeline starting", "workers", p.cfg.MaxWorkers, "batch_size", p.cfg.BatchSize)

	for i := 0; i < p.cfg.MaxWorkers; i++ {
		p.wg.Add(1)
		go p.worker(ctx, i)
	}

	// Consumer goroutine
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer close(p.taskCh)
		for twa := range source.Consume(ctx) {
			select {
			case p.taskCh <- twa:
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Wait blocks until all workers complete (drain finished).
func (p *Pipeline) Wait() {
	p.wg.Wait()
	p.running.Store(false)
	slog.Info("pipeline stopped")
}

// worker: auto-restarts after panic without reducing pool size.
func (p *Pipeline) worker(ctx context.Context, id int) {
	defer p.wg.Done()

	for {
		exited := p.workerLoop(ctx, id)
		if exited {
			return
		}
		slog.Warn("worker restarting after panic", "worker_id", id)
	}
}

func (p *Pipeline) workerLoop(ctx context.Context, id int) (normalExit bool) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("worker panic recovered", "worker_id", id, "panic", r)
			normalExit = false
		}
	}()

	for twa := range p.taskCh {
		select {
		case <-ctx.Done():
			return true
		default:
		}
		p.handleTask(ctx, id, twa)
	}
	return true
}

// handleTask processes a task with exponential backoff retry.
func (p *Pipeline) handleTask(ctx context.Context, workerID int, twa model.TaskWithAck) {
	var lastErr error

	for attempt := 0; attempt <= p.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := p.cfg.RetryBackoff * time.Duration(1<<(attempt-1))
			slog.Info("retrying task", "worker", workerID, "doc_id", twa.Task.DocID,
				"attempt", attempt, "backoff", backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
		}

		if err := p.processTask(ctx, twa.Task); err != nil {
			lastErr = err
			slog.Warn("task attempt failed", "worker", workerID,
				"doc_id", twa.Task.DocID, "attempt", attempt+1, "error", err)
			continue
		}

		// Success → Ack + clear checkpoint
		twa.Ack()
		if err := p.checkpoint.Delete(ctx, twa.Task.DocID); err != nil {
			slog.Warn("checkpoint delete failed", "doc_id", twa.Task.DocID, "error", err)
		}
		slog.Info("task completed", "worker", workerID, "doc_id", twa.Task.DocID)
		return
	}

	// Retries exhausted → push to DLQ first, then commit offset only on DLQ success.
	if err := p.dlq.Push(ctx, model.DLQMessage{Task: twa.Task, Error: lastErr.Error(), Time: time.Now()}); err != nil {
		slog.Error("DLQ push failed, message will be retried", "doc_id", twa.Task.DocID, "error", err)
		twa.Nack(err)
		return
	}

	twa.Ack()
	slog.Error("task exhausted retries and moved to DLQ", "doc_id", twa.Task.DocID, "error", lastErr)
}

// processTask: streaming Parse → batch Embed → Store
func (p *Pipeline) processTask(ctx context.Context, task model.Task) error {
	taskCtx, cancel := context.WithTimeout(ctx, p.cfg.PipelineTimeout)
	defer cancel()

	resumeCheckpoint, hasCheckpoint, err := p.checkpoint.Load(taskCtx, task.DocID)
	if err != nil {
		return fmt.Errorf("load checkpoint: %w", err)
	}
	if hasCheckpoint {
		slog.Info("resuming task from checkpoint",
			"doc_id", task.DocID,
			"chunks_done", resumeCheckpoint.ChunksDone,
			"last_chunk_id", resumeCheckpoint.LastChunkID)
	}

	// Streaming parse with safe error propagation via channel
	chunkCh := make(chan model.Chunk, 20)
	parseErrCh := make(chan error, 1)
	go func() {
		parseErrCh <- p.parser.ParseStream(taskCtx, task, chunkCh)
	}()

	// Batch consumption (independent allocation per batch to avoid slice reuse)
	total := 0
	batch := make([]model.Chunk, 0, p.cfg.BatchSize)

	for chunk := range chunkCh {
		select {
		case <-taskCtx.Done():
			return taskCtx.Err()
		default:
		}

		if hasCheckpoint {
			exists, err := p.storer.Exists(taskCtx, chunk.ChunkID)
			if err != nil {
				return fmt.Errorf("resume exists check for chunk %s: %w", chunk.ChunkID, err)
			}
			if exists {
				total++
				continue
			}
		}

		batch = append(batch, chunk)
		if len(batch) >= p.cfg.BatchSize {
			if err := p.processBatch(taskCtx, batch, task.DocID, &total); err != nil {
				return err
			}
			batch = make([]model.Chunk, 0, p.cfg.BatchSize)
		}
	}

	if len(batch) > 0 {
		if err := p.processBatch(taskCtx, batch, task.DocID, &total); err != nil {
			return err
		}
	}

	// Read parse error safely
	if err := <-parseErrCh; err != nil {
		return fmt.Errorf("parse: %w", err)
	}

	slog.Info("document processed", "doc_id", task.DocID, "chunks", total)
	return nil
}

// processBatch: concurrent Embed + sequential Store
func (p *Pipeline) processBatch(ctx context.Context, batch []model.Chunk, docID string, total *int) error {
	stageCtx, cancel := context.WithTimeout(ctx, p.cfg.StageTimeout)
	defer cancel()

	// Concurrent embed (semaphore limits to 5 goroutines)
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	embedErrs := make([]error, len(batch))

	for i := range batch {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-stageCtx.Done():
				embedErrs[idx] = stageCtx.Err()
				return
			}
			embedErrs[idx] = p.embedder.Embed(stageCtx, &batch[idx])
		}(i)
	}
	wg.Wait()

	// Generate sparse vectors (synchronous, CPU-bound and fast)
	for i := range batch {
		if embedErrs[i] == nil {
			batch[i].SparseVector = p.sparseEncoder.Encode(batch[i].Content)
		}
	}

	// Collect successful chunks and aggregate errors
	var successful []model.Chunk
	var errs []error
	for i, chunk := range batch {
		if embedErrs[i] != nil {
			errs = append(errs, embedErrs[i])
			continue
		}
		if chunk.Vector != nil {
			successful = append(successful, chunk)
		}
	}

	if len(errs) > 0 {
		slog.Warn("batch embed partial failure",
			"doc_id", docID, "success", len(successful), "failed", len(errs))
	}
	if len(successful) == 0 {
		return fmt.Errorf("all %d chunks failed embedding: %w", len(batch), errors.Join(errs...))
	}

	// Sequential store with failure threshold
	storeFailed := 0
	lastStoredChunkID := ""
	for _, chunk := range successful {
		select {
		case <-stageCtx.Done():
			return stageCtx.Err()
		default:
		}
		if err := p.storer.Upsert(stageCtx, chunk); err != nil {
			storeFailed++
			slog.Warn("store upsert failed", "chunk_id", chunk.ChunkID, "error", err)
			if storeFailed > len(successful)/2 {
				return fmt.Errorf("store failure rate too high: %d/%d", storeFailed, len(successful))
			}
			continue
		}
		*total++
		lastStoredChunkID = chunk.ChunkID

		// Eventual-consistency full-text path:
		// Qdrant success is primary; ES errors never fail main pipeline.
		if p.fullTextSink != nil {
			if err := p.fullTextSink.Enqueue(stageCtx, chunk); err != nil {
				slog.Warn("full-text enqueue failed (ignored for eventual consistency)",
					"chunk_id", chunk.ChunkID, "doc_id", chunk.DocID, "error", err)
			}
		}
	}

	// Update checkpoint
	if *total > 0 && lastStoredChunkID != "" {
		_ = p.checkpoint.Save(ctx, model.Checkpoint{
			DocID:       docID,
			ChunksDone:  *total,
			LastChunkID: lastStoredChunkID,
		})
	}

	return nil
}
