// Package pipeline implements the worker pool orchestrator with panic recovery and graceful drain.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/indexmanifest"
	"ai-etl-pipeline/internal/ingestion"
	"ai-etl-pipeline/internal/metrics"
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/parser"
	"ai-etl-pipeline/internal/sparse"
)

// Pipeline orchestrates concurrent document processing with panic recovery.
// docStatusWriter is the document-registry status write surface. It is satisfied
// by *docstore.PgStore. Status writes are best-effort: ingestion never blocks on
// the registry.
type docStatusWriter interface {
	UpsertStatus(ctx context.Context, tenantID, docID string, d docstore.Document) error
}

type Pipeline struct {
	cfg           config.Config
	parser        *parser.Parser
	parserClient  *parser.Client
	embedder      model.Embedder
	storer        model.Storer
	fullTextSink  model.FullTextSink
	metrics       *metrics.Collector
	checkpoint    model.CheckpointStore
	dlq           model.DLQStore
	taskStatus    model.TaskStatusStore
	docStatus     docStatusWriter
	ingestionJobs ingestion.JobStore
	taskCanceller interface {
		Cancel(context.Context, model.Task, string, time.Time) error
	}
	generationBuilds indexmanifest.BuildStarter
	sparseEncoder    *sparse.Encoder

	taskCh  chan model.TaskWithAck
	wg      sync.WaitGroup
	running atomic.Bool
}

var ErrTaskCancelled = errors.New("task cancelled")

// pageBatchRanges returns half-open page ranges for bounded OCR requests. The
// checkpoint stores the next page to process, so restarting after a completed
// batch never re-runs an already durable OCR batch.
func pageBatchRanges(totalPages, batchSize, startPage int) [][2]int {
	if totalPages <= 0 || batchSize <= 0 {
		return nil
	}
	if startPage < 0 {
		startPage = 0
	}
	if startPage >= totalPages {
		return nil
	}
	ranges := make([][2]int, 0, (totalPages-startPage+batchSize-1)/batchSize)
	for start := startPage; start < totalPages; start += batchSize {
		end := start + batchSize
		if end > totalPages {
			end = totalPages
		}
		ranges = append(ranges, [2]int{start, end})
	}
	return ranges
}

func (p *Pipeline) WithIngestionJobs(store ingestion.JobStore) *Pipeline {
	p.ingestionJobs = store
	if canceller, ok := store.(interface {
		Cancel(context.Context, model.Task, string, time.Time) error
	}); ok {
		p.taskCanceller = canceller
	}
	return p
}

func (p *Pipeline) WithGenerationBuilder(builder indexmanifest.BuildStarter) *Pipeline {
	p.generationBuilds = builder
	return p
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
		parserClient:  parser.NewClient(cfg.ParserEndpoint),
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

// WithDocStore enables write-through of ingestion status to the document
// registry (PostgreSQL). Best-effort: a failure is logged, never blocking.
func (p *Pipeline) WithDocStore(store docStatusWriter) *Pipeline {
	p.docStatus = store
	return p
}

// WithTaskStatusStore enables task lifecycle status updates.
func (p *Pipeline) WithTaskStatusStore(store model.TaskStatusStore) *Pipeline {
	p.taskStatus = store
	return p
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
	if twa.Task.EventID != "" {
		if p.ingestionJobs == nil {
			err := errors.New("durable ingestion job store unavailable")
			twa.Nack(err)
			return
		}
		claim, err := p.ingestionJobs.Claim(ctx, twa.Task, p.cfg.IngestionJobLease)
		if err != nil {
			twa.Nack(err)
			return
		}
		switch claim {
		case ingestion.ClaimAcquired:
		case ingestion.ClaimTerminal:
			twa.Ack()
			return
		case ingestion.ClaimBusy:
			twa.Nack(errors.New("ingestion job is already processing"))
			return
		default:
			twa.Nack(fmt.Errorf("unknown ingestion claim result %q", claim))
			return
		}
	}

	p.saveTaskStatus(ctx, twa.Task, model.TaskStatusProcessing, "processing", "")

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
			if errors.Is(err, ErrTaskCancelled) {
				if twa.Task.EventID != "" && p.taskCanceller != nil {
					_ = p.taskCanceller.Cancel(ctx, twa.Task, err.Error(), time.Now().UTC())
				}
				twa.Ack()
				p.saveTaskStatus(ctx, twa.Task, model.TaskStatusCancelled, "cancelled", err.Error())
				return
			}
			lastErr = err
			slog.Warn("task attempt failed", "worker", workerID,
				"doc_id", twa.Task.DocID, "attempt", attempt+1, "error", err)
			continue
		}

		// Durable completion must commit before the Kafka offset. If PostgreSQL is
		// temporarily unavailable the message remains uncommitted and may resume
		// after its processing lease expires.
		if twa.Task.EventID != "" {
			if err := p.ingestionJobs.Complete(ctx, twa.Task, time.Now().UTC()); err != nil {
				twa.Nack(err)
				return
			}
		}
		twa.Ack()
		if err := p.checkpoint.Delete(ctx, twa.Task.DocID); err != nil {
			slog.Warn("checkpoint delete failed", "doc_id", twa.Task.DocID, "error", err)
		}
		p.saveTaskStatus(ctx, twa.Task, model.TaskStatusCompleted, "completed", "")
		slog.Info("task completed", "worker", workerID, "doc_id", twa.Task.DocID)
		return
	}

	// Retries exhausted → push to DLQ first, then commit offset only on DLQ success.
	if err := p.dlq.Push(ctx, model.DLQMessage{Task: twa.Task, Error: lastErr.Error(), Time: time.Now()}); err != nil {
		slog.Error("DLQ push failed, message will be retried", "doc_id", twa.Task.DocID, "error", err)
		twa.Nack(err)
		return
	}

	if twa.Task.EventID != "" {
		if err := p.ingestionJobs.Fail(ctx, twa.Task, lastErr.Error(), time.Now().UTC()); err != nil {
			twa.Nack(err)
			return
		}
	}
	twa.Ack()
	p.saveTaskStatus(ctx, twa.Task, model.TaskStatusFailed, "failed", lastErr.Error())
	slog.Error("task exhausted retries and moved to DLQ", "doc_id", twa.Task.DocID, "error", lastErr)
}

func (p *Pipeline) saveTaskStatus(ctx context.Context, task model.Task, state model.TaskStatusState, stage string, message string) {
	p.saveTaskStatusProgress(ctx, task, state, stage, message, 0, 0)
}

// saveTaskStatusProgress persists task status with chunk progress so the
// frontend can render "embedding 12/37" instead of a static "processing".
func (p *Pipeline) saveTaskStatusProgress(ctx context.Context, task model.Task, state model.TaskStatusState, stage string, message string, done, total int) {
	p.saveTaskStatusProgressPages(ctx, task, state, stage, message, done, total, 0, 0)
}

func (p *Pipeline) saveTaskStatusProgressPages(ctx context.Context, task model.Task, state model.TaskStatusState, stage string, message string, done, total, pagesDone, pagesTotal int) {
	if p.taskStatus == nil {
		return
	}
	now := time.Now().UTC()
	status := model.TaskStatus{
		TaskID:      task.DocID,
		DocID:       task.DocID,
		TenantID:    task.TenantID,
		JobID:       task.JobID,
		EventID:     task.EventID,
		Status:      state,
		Stage:       stage,
		ChunksDone:  done,
		TotalChunks: total,
		PagesDone:   pagesDone,
		PagesTotal:  pagesTotal,
		Error:       message,
		FilePath:    task.FilePath,
		FileHash:    task.FileHash,
		Permission:  task.Permission,
		UploadedBy:  task.UploadedBy,
		Metadata:    task.Metadata,
		CreatedAt:   task.CreatedAt,
		UpdatedAt:   now,
	}
	if state == model.TaskStatusCompleted || state == model.TaskStatusFailed || state == model.TaskStatusCancelled {
		status.CompletedAt = now
	}
	if status.CreatedAt.IsZero() {
		status.CreatedAt = now
	}
	if err := p.taskStatus.Save(ctx, status); err != nil {
		slog.Warn("task status update failed", "doc_id", task.DocID, "status", state, "error", err)
	}

	// Write-through to the document registry so the inventory mirrors durable
	// task status. Best-effort: never blocks or fails ingestion.
	if p.docStatus != nil && task.EventID == "" {
		if err := p.docStatus.UpsertStatus(ctx, task.TenantID, task.DocID, docstore.Document{
			Status:      string(state),
			Stage:       stage,
			ChunksDone:  done,
			ChunksTotal: total,
			Error:       message,
			CompletedAt: status.CompletedAt,
		}); err != nil {
			slog.Warn("document registry status update failed", "doc_id", task.DocID, "status", state, "error", err)
		}
	}
}

func requiresParserService(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf", ".docx", ".doc", ".xls", ".xlsx", ".pptx",
		".png", ".jpg", ".jpeg", ".webp", ".bmp":
		return true
	default:
		return false
	}
}

// processTask: streaming Parse → batch Embed → Store
func (p *Pipeline) processTask(ctx context.Context, task model.Task) (resultErr error) {
	taskCtx, cancel := context.WithTimeout(ctx, p.cfg.PipelineTimeout)
	defer cancel()

	var generationBuild indexmanifest.BuildSession
	if task.EventID != "" {
		if p.generationBuilds == nil {
			return errors.New("generation builder unavailable for durable ingestion")
		}
		generationBuild, resultErr = p.generationBuilds.Begin(taskCtx, indexmanifest.BuildRequest{
			Version: indexmanifest.VersionIdentity{
				TenantID: task.TenantID, DocumentID: task.DocID, DocumentVersionID: task.JobID,
			},
			Definition: indexmanifest.BuildDefinition{
				ChunkerVersion:    fmt.Sprintf("parser-v1:size=%d:overlap=%d", p.cfg.MaxChunkSize, p.cfg.ChunkOverlap),
				EmbeddingModel:    p.cfg.EmbedModel,
				VectorDimension:   p.cfg.EmbedDimension,
				SchemaVersion:     "generation-payload-v1",
				CollectionVersion: p.cfg.StoreCollection,
				IndexVersion:      p.cfg.ESIndex,
			},
		})
		if resultErr != nil {
			return resultErr
		}
		defer func() {
			if resultErr != nil {
				abortCtx, abortCancel := context.WithTimeout(context.WithoutCancel(ctx), p.cfg.StageTimeout)
				defer abortCancel()
				if abortErr := generationBuild.Abort(abortCtx, resultErr); abortErr != nil {
					resultErr = errors.Join(resultErr, abortErr)
				}
			}
		}()
	}

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

	p.saveTaskStatusProgress(taskCtx, task, model.TaskStatusProcessing, "parsing", "", 0, 0)

	// Plain-text files keep the deterministic local scanner (identical chunk
	// semantics to the golden-set eval). Binary documents (PDF/DOCX, including
	// scanned PDFs) go to the parser service, which does real extraction and OCR.
	needsParserService := requiresParserService(task.FilePath)
	if needsParserService && strings.EqualFold(filepath.Ext(task.FilePath), ".pdf") {
		total, err := p.processPDFBatches(taskCtx, task, resumeCheckpoint, hasCheckpoint, generationBuild)
		if err != nil {
			return err
		}
		if total == 0 {
			return errors.New("parser produced no chunks")
		}
		if generationBuild != nil {
			if err := generationBuild.Complete(taskCtx); err != nil {
				return fmt.Errorf("complete generation build: %w", err)
			}
		}
		slog.Info("document processed", "doc_id", task.DocID, "chunks", total)
		return nil
	}

	// Stream parse with safe error propagation via channel. Both sources (the
	// local scanner and the parser-service response) feed the same channel, so
	// the batch consumption below is shared.
	chunkCh := make(chan model.Chunk, 20)
	parseErrCh := make(chan error, 1)
	totalCh := make(chan int, 1) // known chunk total for the parser-service path
	go func() {
		if !needsParserService {
			parseErrCh <- p.parser.ParseStream(taskCtx, task, chunkCh)
			return
		}
		// The parser-service path owns chunkCh and must close it on every exit
		// (success or error); the text path above already closes it via
		// ParseStream's deferred close. Without this the consumer's
		// `range chunkCh` never terminates and the task hangs in "processing".
		defer close(chunkCh)
		// Materialize the object to a local temp file; the parser service needs
		// a filesystem path. ParseFile has a generous timeout for OCR.
		localPath := task.FilePath
		cleanup := func() {}
		if !pathExists(localPath) {
			path, _, c, err := p.parser.MaterializeObject(taskCtx, task.FilePath)
			if err != nil {
				totalCh <- 0
				parseErrCh <- fmt.Errorf("materialize object: %w", err)
				return
			}
			localPath = path
			cleanup = c
		}
		defer cleanup()

		localTask := task
		localTask.FilePath = localPath
		chunks, err := p.parserClient.ParseFile(taskCtx, localTask)
		if err != nil {
			totalCh <- 0
			parseErrCh <- fmt.Errorf("parser service: %w", err)
			return
		}
		totalCh <- len(chunks)
		for _, chunk := range chunks {
			select {
			case chunkCh <- chunk:
			case <-taskCtx.Done():
				parseErrCh <- taskCtx.Err()
				return
			}
		}
		parseErrCh <- nil
	}()

	// The parser-service path reports a known total before chunks flow; the
	// text path streams chunks without a known count.
	var totalChunks int
	if needsParserService {
		select {
		case totalChunks = <-totalCh:
		case <-taskCtx.Done():
			return taskCtx.Err()
		}
	}

	// Batch embed + store (independent allocation per batch to avoid slice reuse)
	total := 0
	batch := make([]model.Chunk, 0, p.cfg.BatchSize)
	for chunk := range chunkCh {
		select {
		case <-taskCtx.Done():
			return taskCtx.Err()
		default:
		}

		if hasCheckpoint && generationBuild == nil {
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
			if err := p.processBatch(taskCtx, batch, task.DocID, &total, generationBuild); err != nil {
				return err
			}
			p.saveTaskStatusProgress(taskCtx, task, model.TaskStatusProcessing, "embedding", "", total, totalChunks)
			batch = make([]model.Chunk, 0, p.cfg.BatchSize)
		}
	}

	if len(batch) > 0 {
		if err := p.processBatch(taskCtx, batch, task.DocID, &total, generationBuild); err != nil {
			return err
		}
		p.saveTaskStatusProgress(taskCtx, task, model.TaskStatusProcessing, "embedding", "", total, totalChunks)
	}

	// The producer closed the channel, so parsing finished; surface its error
	// (e.g. materialize or parser-service failures) instead of swallowing it.
	if err := <-parseErrCh; err != nil {
		return err
	}
	if total == 0 {
		return errors.New("parser produced no chunks")
	}
	if generationBuild != nil {
		if err := generationBuild.Complete(taskCtx); err != nil {
			return fmt.Errorf("complete generation build: %w", err)
		}
	}

	slog.Info("document processed", "doc_id", task.DocID, "chunks", total)
	return nil
}

// processPDFBatches keeps OCR requests bounded and durable. A parser request
// covers one page range; completed ranges are recorded in the checkpoint before
// the next range starts. Generation builds intentionally replay from page zero
// because their digest must include every chunk in the current build session.
func (p *Pipeline) processPDFBatches(taskCtx context.Context, task model.Task, cp model.Checkpoint, hasCheckpoint bool, generationBuild indexmanifest.BuildSession) (int, error) {
	localPath := task.FilePath
	cleanup := func() {}
	if !pathExists(localPath) {
		path, _, c, err := p.parser.MaterializeObject(taskCtx, task.FilePath)
		if err != nil {
			return 0, fmt.Errorf("materialize object: %w", err)
		}
		localPath, cleanup = path, c
	}
	defer cleanup()
	localTask := task
	localTask.FilePath = localPath

	startPage := 0
	if hasCheckpoint && generationBuild == nil {
		startPage = cp.PagesDone
	}
	batchSize := p.cfg.OCRPageBatchSize
	if batchSize <= 0 {
		batchSize = 25
	}
	var totalPages, totalChunks int
	chunkOffset := 0
	if hasCheckpoint && generationBuild == nil {
		totalChunks = cp.ChunksDone
	}
	first := true
	for first || startPage < totalPages {
		if p.taskStatus != nil {
			if status, found, err := p.taskStatus.Load(taskCtx, task.TenantID, task.DocID); err == nil && found && status.Status == model.TaskStatusCancelled {
				return totalChunks, ErrTaskCancelled
			}
		}
		first = false
		end := startPage + batchSize
		if totalPages > 0 && end > totalPages {
			end = totalPages
		}
		// A large deterministic ID segment per page prevents two page batches
		// from colliding even when the number of chunks per page varies.
		chunkOffset = startPage * 10000
		result, err := p.parserClient.ParseFileRangeResult(taskCtx, localTask, startPage, end, chunkOffset)
		if err != nil {
			return totalChunks, fmt.Errorf("parser service OCR pages %d-%d: %w", startPage+1, end, err)
		}
		if result.PageCount > 0 {
			totalPages = result.PageCount
		} else if end > totalPages {
			totalPages = end
		}
		p.saveTaskStatusProgressPages(taskCtx, task, model.TaskStatusProcessing, "ocr", fmt.Sprintf("正在处理第 %d-%d 页", startPage+1, end), totalChunks, 0, startPage, totalPages)

		batch := result.Chunks
		if hasCheckpoint && generationBuild == nil && startPage < cp.PagesDone {
			startPage = end
			continue
		}
		if len(batch) > 0 {
			// OCR output is slower to embed on the local CPU model than ordinary
			// text. Isolate each chunk so one slow/invalid request cannot consume
			// the shared stage timeout or repeat successful siblings on retry.
			embedBatchSize := 1
			for i := 0; i < len(batch); i += embedBatchSize {
				endBatch := i + embedBatchSize
				if endBatch > len(batch) {
					endBatch = len(batch)
				}
				if err := p.processBatch(taskCtx, batch[i:endBatch], task.DocID, &totalChunks, generationBuild); err != nil {
					return totalChunks, err
				}
				p.saveTaskStatusProgressPages(taskCtx, task, model.TaskStatusProcessing, "embedding", "正在生成向量并写入索引", totalChunks, totalChunks, startPage, totalPages)
			}
		}
		startPage = end
		if err := p.checkpoint.Save(taskCtx, model.Checkpoint{DocID: task.DocID, ChunksDone: totalChunks, PagesDone: startPage, PagesTotal: totalPages}); err != nil {
			return totalChunks, fmt.Errorf("save OCR checkpoint: %w", err)
		}
		p.saveTaskStatusProgressPages(taskCtx, task, model.TaskStatusProcessing, "ocr", fmt.Sprintf("已完成第 %d / %d 页", startPage, totalPages), totalChunks, 0, startPage, totalPages)
		if startPage >= totalPages {
			break
		}
	}
	return totalChunks, nil
}

func pathExists(path string) bool {
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return false
}

// processBatch: concurrent Embed + sequential Store
func (p *Pipeline) processBatch(ctx context.Context, batch []model.Chunk, docID string, total *int, generationBuild indexmanifest.BuildSession) error {
	stageCtx, cancel := context.WithTimeout(ctx, p.cfg.StageTimeout)
	defer cancel()

	// Concurrent embed is bounded independently from the worker pool. Local
	// Ollama/bge-m3 instances commonly need a concurrency of 1–2; five parallel
	// OCR chunks can otherwise queue until the per-request deadline expires.
	concurrency := p.cfg.EmbedConcurrency
	if concurrency <= 0 {
		concurrency = 2
	}
	sem := make(chan struct{}, concurrency)
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
	if generationBuild != nil && len(errs) > 0 {
		return fmt.Errorf("strict generation embedding failed for %d/%d chunks: %w", len(errs), len(batch), errors.Join(errs...))
	}
	if generationBuild != nil && len(successful) != len(batch) {
		return fmt.Errorf("strict generation embedding produced vectors for %d/%d chunks", len(successful), len(batch))
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
		var storeErr error
		if generationBuild != nil {
			storeErr = generationBuild.Upsert(stageCtx, chunk)
		} else {
			storeErr = p.storer.Upsert(stageCtx, chunk)
		}
		if storeErr != nil {
			storeFailed++
			slog.Warn("store upsert failed", "chunk_id", chunk.ChunkID, "error", storeErr)
			if generationBuild != nil {
				return storeErr
			}
			if storeFailed > len(successful)/2 {
				return fmt.Errorf("store failure rate too high: %d/%d", storeFailed, len(successful))
			}
			continue
		}
		*total++
		lastStoredChunkID = chunk.ChunkID

		// Eventual-consistency full-text path:
		// Qdrant success is primary; ES errors never fail main pipeline.
		if generationBuild == nil && p.fullTextSink != nil {
			if err := p.fullTextSink.Enqueue(stageCtx, chunk); err != nil {
				slog.Warn("full-text enqueue failed (ignored for eventual consistency)",
					"chunk_id", chunk.ChunkID, "doc_id", chunk.DocID, "error", err)
			}
		}
	}

	// Update checkpoint
	if *total > 0 && lastStoredChunkID != "" {
		cp := model.Checkpoint{
			DocID:       docID,
			ChunksDone:  *total,
			LastChunkID: lastStoredChunkID,
		}
		// Preserve page progress written by the OCR batch coordinator. A chunk
		// checkpoint must never roll a document back to page zero after a partial
		// batch failure.
		if existing, found, err := p.checkpoint.Load(ctx, docID); err == nil && found {
			cp.PagesDone, cp.PagesTotal = existing.PagesDone, existing.PagesTotal
		}
		_ = p.checkpoint.Save(ctx, cp)
	}

	return nil
}
