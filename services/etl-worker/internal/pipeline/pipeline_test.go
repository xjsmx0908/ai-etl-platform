package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/metrics"
	"ai-etl-pipeline/internal/model"
)

// mockParserServer returns a fake parser-service URL that yields the given
// chunks, so pipeline tests no longer depend on the local text scanner.
func mockParserServer(t *testing.T, docID string, chunks []map[string]interface{}) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"doc_id":       docID,
			"chunks":       chunks,
			"total_chunks": len(chunks),
			"status":       "success",
		})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

type noopEmbedder struct{}

func (noopEmbedder) Embed(context.Context, *model.Chunk) error { return nil }
func (noopEmbedder) Close() error                              { return nil }

type vectorEmbedder struct{}

func (vectorEmbedder) Embed(_ context.Context, c *model.Chunk) error {
	c.Vector = []float64{0.1, 0.2, 0.3}
	return nil
}
func (vectorEmbedder) Close() error { return nil }

type noopStorer struct{}

func (noopStorer) Upsert(context.Context, model.Chunk) error    { return nil }
func (noopStorer) Exists(context.Context, string) (bool, error) { return false, nil }
func (noopStorer) Close() error                                 { return nil }

type captureStorer struct {
	chunks []model.Chunk
}

func (s *captureStorer) Upsert(_ context.Context, c model.Chunk) error {
	s.chunks = append(s.chunks, c)
	return nil
}
func (s *captureStorer) Exists(context.Context, string) (bool, error) { return false, nil }
func (s *captureStorer) Close() error                                 { return nil }

type noopCheckpoint struct{}

func (noopCheckpoint) Save(context.Context, model.Checkpoint) error { return nil }
func (noopCheckpoint) Load(context.Context, string) (model.Checkpoint, bool, error) {
	return model.Checkpoint{}, false, nil
}
func (noopCheckpoint) Delete(context.Context, string) error { return nil }
func (noopCheckpoint) Close() error                         { return nil }

type checkpointStub struct {
	cp        model.Checkpoint
	found     bool
	loadErr   error
	saveCalls []model.Checkpoint
}

func (c *checkpointStub) Save(_ context.Context, cp model.Checkpoint) error {
	c.saveCalls = append(c.saveCalls, cp)
	return nil
}

func (c *checkpointStub) Load(context.Context, string) (model.Checkpoint, bool, error) {
	if c.loadErr != nil {
		return model.Checkpoint{}, false, c.loadErr
	}
	return c.cp, c.found, nil
}

func (c *checkpointStub) Delete(context.Context, string) error { return nil }
func (c *checkpointStub) Close() error                         { return nil }

type dlqStub struct {
	pushes int
	err    error
}

func (d *dlqStub) Push(_ context.Context, _ model.DLQMessage) error {
	d.pushes++
	return d.err
}
func (d *dlqStub) List(context.Context) ([]model.DLQMessage, error) { return nil, nil }
func (d *dlqStub) Close() error                                     { return nil }

type taskStatusStub struct {
	statuses []model.TaskStatus
	err      error
}

func (s *taskStatusStub) Save(_ context.Context, status model.TaskStatus) error {
	if s.err != nil {
		return s.err
	}
	s.statuses = append(s.statuses, status)
	return nil
}

func (s *taskStatusStub) Load(context.Context, string, string) (model.TaskStatus, bool, error) {
	return model.TaskStatus{}, false, nil
}

func (s *taskStatusStub) Close() error { return nil }

type fullTextSinkStub struct {
	calls int
	err   error
}

func (s *fullTextSinkStub) Enqueue(_ context.Context, _ model.Chunk) error {
	s.calls++
	return s.err
}

func (s *fullTextSinkStub) Close() error { return nil }

type resumableStorer struct {
	existing    map[string]bool
	upserted    []model.Chunk
	existsCalls []string
	failIDs     map[string]error
}

func (s *resumableStorer) Upsert(_ context.Context, c model.Chunk) error {
	if err := s.failIDs[c.ChunkID]; err != nil {
		return err
	}
	s.upserted = append(s.upserted, c)
	if s.existing == nil {
		s.existing = make(map[string]bool)
	}
	s.existing[c.ChunkID] = true
	return nil
}

func (s *resumableStorer) Exists(_ context.Context, chunkID string) (bool, error) {
	s.existsCalls = append(s.existsCalls, chunkID)
	return s.existing[chunkID], nil
}

func (s *resumableStorer) Close() error { return nil }

func baseTestConfig() config.Config {
	return config.Config{
		MaxWorkers:      1,
		TaskBufferSize:  1,
		StageTimeout:    50 * time.Millisecond,
		PipelineTimeout: 50 * time.Millisecond,
		MaxRetries:      0,
		RetryBackoff:    time.Millisecond,
		BatchSize:       1,
		MaxChunkSize:    128,
		ChunkOverlap:    16,
		ReadBufferSize:  4096,
		SparseK1:        1.2,
		SparseB:         0.75,
		SparseAvgDL:     256,
		Environment:     "staging",
	}
}

func TestHandleTask_CommitsOffsetOnlyAfterDLQSuccess(t *testing.T) {
	dlq := &dlqStub{}
	statuses := &taskStatusStub{}
	p := New(baseTestConfig(), noopEmbedder{}, noopStorer{}, metrics.NewCollector(10), noopCheckpoint{}, dlq).WithTaskStatusStore(statuses)

	acked := 0
	nacked := 0
	twa := model.TaskWithAck{
		Task: model.Task{
			DocID:    "doc-1",
			TenantID: "tenant-a",
			FilePath: "missing-object-key",
		},
		Ack:  func() { acked++ },
		Nack: func(error) { nacked++ },
	}

	p.handleTask(context.Background(), 0, twa)

	if dlq.pushes != 1 {
		t.Fatalf("expected 1 DLQ push, got %d", dlq.pushes)
	}
	if acked != 1 {
		t.Fatalf("expected ack to be called after DLQ success, got %d", acked)
	}
	if nacked != 0 {
		t.Fatalf("expected nack not called on DLQ success, got %d", nacked)
	}
	if len(statuses.statuses) < 2 {
		t.Fatalf("expected processing and failed statuses, got %+v", statuses.statuses)
	}
	if statuses.statuses[0].Status != model.TaskStatusProcessing || statuses.statuses[len(statuses.statuses)-1].Status != model.TaskStatusFailed {
		t.Fatalf("unexpected status sequence: %+v", statuses.statuses)
	}
}

func TestHandleTask_DoesNotCommitWhenDLQFails(t *testing.T) {
	dlq := &dlqStub{err: errors.New("dlq unavailable")}
	statuses := &taskStatusStub{}
	p := New(baseTestConfig(), noopEmbedder{}, noopStorer{}, metrics.NewCollector(10), noopCheckpoint{}, dlq).WithTaskStatusStore(statuses)

	acked := 0
	nacked := 0
	twa := model.TaskWithAck{
		Task: model.Task{
			DocID:    "doc-2",
			TenantID: "tenant-a",
			FilePath: "missing-object-key",
		},
		Ack:  func() { acked++ },
		Nack: func(error) { nacked++ },
	}

	p.handleTask(context.Background(), 0, twa)

	if dlq.pushes != 1 {
		t.Fatalf("expected 1 DLQ push attempt, got %d", dlq.pushes)
	}
	if acked != 0 {
		t.Fatalf("expected no ack when DLQ push fails, got %d", acked)
	}
	if nacked != 1 {
		t.Fatalf("expected nack when DLQ push fails, got %d", nacked)
	}
	if len(statuses.statuses) < 1 || statuses.statuses[0].Status != model.TaskStatusProcessing {
		t.Fatalf("expected processing status when DLQ push fails, got %+v", statuses.statuses)
	}
	// DLQ failure means no terminal "failed" status is written (message nacked).
	for _, s := range statuses.statuses {
		if s.Status == model.TaskStatusFailed {
			t.Fatalf("unexpected failed status when DLQ push fails: %+v", statuses.statuses)
		}
	}
}

func TestHandleTask_RecordsCompletedStatus(t *testing.T) {
	cfg := baseTestConfig()
	cfg.Environment = "dev"
	cfg.MaxChunkSize = 512
	cfg.ReadBufferSize = 4096
	cfg.ParserEndpoint = mockParserServer(t, "doc-ok", []map[string]interface{}{
		{"chunk_id": "doc-ok_0000", "doc_id": "doc-ok", "tenant_id": "tenant-a", "content": "# Done\n" + strings.Repeat("a", 140), "index": 0},
	})

	tmp, err := os.CreateTemp(t.TempDir(), "complete-*.md")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := tmp.WriteString("# Done\n" + strings.Repeat("a", 140)); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	statuses := &taskStatusStub{}
	p := New(cfg, vectorEmbedder{}, noopStorer{}, metrics.NewCollector(10), noopCheckpoint{}, &dlqStub{}).WithTaskStatusStore(statuses)
	acked := 0
	p.handleTask(context.Background(), 0, model.TaskWithAck{
		Task: model.Task{
			DocID:     "doc-ok",
			TenantID:  "tenant-a",
			FilePath:  tmp.Name(),
			CreatedAt: time.Now().UTC(),
		},
		Ack:  func() { acked++ },
		Nack: func(error) {},
	})
	if acked != 1 {
		t.Fatalf("expected ack, got %d", acked)
	}
	if len(statuses.statuses) < 2 {
		t.Fatalf("expected processing and completed statuses, got %+v", statuses.statuses)
	}
	if statuses.statuses[0].Status != model.TaskStatusProcessing || statuses.statuses[len(statuses.statuses)-1].Status != model.TaskStatusCompleted {
		t.Fatalf("unexpected status sequence: %+v", statuses.statuses)
	}
	// The pipeline emits a granular embedding stage carrying chunk progress.
	foundProgress := false
	for _, s := range statuses.statuses {
		if s.Stage == "embedding" && s.ChunksDone == 1 {
			foundProgress = true
		}
	}
	if !foundProgress {
		t.Fatalf("expected an embedding stage with ChunksDone=1, got %+v", statuses.statuses)
	}
}

func TestProcessBatch_FullTextSinkFailure_DoesNotFailMainPath(t *testing.T) {
	cfg := baseTestConfig()
	cfg.MaxWorkers = 1
	cfg.TaskBufferSize = 1
	cfg.BatchSize = 1
	cfg.Environment = "dev"

	storer := &captureStorer{}
	fullText := &fullTextSinkStub{err: errors.New("es unavailable")}

	p := NewWithSinks(cfg, vectorEmbedder{}, storer, fullText, metrics.NewCollector(10), noopCheckpoint{}, &dlqStub{})

	total := 0
	batch := []model.Chunk{
		{
			ChunkID:  "doc-x_0001",
			DocID:    "doc-x",
			TenantID: "tenant-a",
			Content:  "hello world",
			Index:    1,
		},
	}

	if err := p.processBatch(context.Background(), batch, "doc-x", &total); err != nil {
		t.Fatalf("processBatch should succeed even if fullText sink fails, got: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected total=1, got %d", total)
	}
	if fullText.calls != 1 {
		t.Fatalf("expected fullText sink call=1, got %d", fullText.calls)
	}
	if len(storer.chunks) != 1 {
		t.Fatalf("expected stored chunks=1, got %d", len(storer.chunks))
	}
}

func TestProcessTask_ResumesFromCheckpointAndSkipsStoredChunks(t *testing.T) {
	cfg := baseTestConfig()
	cfg.Environment = "dev"
	cfg.MaxChunkSize = 512
	cfg.ReadBufferSize = 4096
	cfg.ParserEndpoint = mockParserServer(t, "doc-resume", []map[string]interface{}{
		{"chunk_id": "doc-resume_0000", "doc_id": "doc-resume", "tenant_id": "tenant-a", "content": "first", "index": 0},
		{"chunk_id": "doc-resume_0001", "doc_id": "doc-resume", "tenant_id": "tenant-a", "content": "second", "index": 1},
		{"chunk_id": "doc-resume_0002", "doc_id": "doc-resume", "tenant_id": "tenant-a", "content": "third", "index": 2},
	})

	tmp, err := os.CreateTemp(t.TempDir(), "resume-*.md")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	content := strings.Join([]string{
		"# First",
		strings.Repeat("a", 140),
		"# Second",
		strings.Repeat("b", 140),
		"# Third",
		strings.Repeat("c", 140),
	}, "\n")
	if _, err := tmp.WriteString(content); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	storer := &resumableStorer{
		existing: map[string]bool{
			"doc-resume_0000": true,
			"doc-resume_0001": true,
		},
	}
	ckpt := &checkpointStub{
		cp: model.Checkpoint{
			DocID:       "doc-resume",
			ChunksDone:  2,
			LastChunkID: "doc-resume_0001",
		},
		found: true,
	}
	p := New(cfg, vectorEmbedder{}, storer, metrics.NewCollector(10), ckpt, &dlqStub{})

	err = p.processTask(context.Background(), model.Task{
		DocID:      "doc-resume",
		TenantID:   "tenant-a",
		FilePath:   tmp.Name(),
		Permission: "internal",
	})
	if err != nil {
		t.Fatalf("processTask returned error: %v", err)
	}
	if len(storer.upserted) != 1 {
		t.Fatalf("expected exactly one resumed upsert, got %d", len(storer.upserted))
	}
	if storer.upserted[0].ChunkID != "doc-resume_0002" {
		t.Fatalf("expected resumed chunk doc-resume_0002, got %s", storer.upserted[0].ChunkID)
	}
	if len(storer.existsCalls) != 3 {
		t.Fatalf("expected 3 exists checks during resume, got %d", len(storer.existsCalls))
	}
}

// The parser-service (binary) path must complete: the producer owns chunkCh
// and must close it after pushing all chunks, or the consumer's `range chunkCh`
// never terminates and processTask hangs in "processing" forever (regression:
// a PDF/DOCX task previously deadlocked here). Uses context.Background() so a
// timeout cannot mask the hang — go test -timeout would report it as FAIL.
func TestProcessTask_BinaryParserServicePathCompletes(t *testing.T) {
	cfg := baseTestConfig()
	cfg.PipelineTimeout = 5 * time.Second
	cfg.StageTimeout = 5 * time.Second
	cfg.ParserEndpoint = mockParserServer(t, "doc-bin", []map[string]interface{}{
		{"chunk_id": "doc-bin_0000", "doc_id": "doc-bin", "tenant_id": "tenant-a", "content": "first chunk", "index": 0},
		{"chunk_id": "doc-bin_0001", "doc_id": "doc-bin", "tenant_id": "tenant-a", "content": "second chunk", "index": 1},
	})

	// A real temp file with a binary extension so pathExists() short-circuits
	// the S3 materialization step and we exercise the parser-service path.
	tmp, err := os.CreateTemp(t.TempDir(), "doc-*.pdf")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("close temp file: %v", err)
	}

	storer := &captureStorer{}
	statuses := &taskStatusStub{}
	p := New(cfg, vectorEmbedder{}, storer, metrics.NewCollector(10), &noopCheckpoint{}, &dlqStub{}).WithTaskStatusStore(statuses)

	err = p.processTask(context.Background(), model.Task{
		DocID:      "doc-bin",
		TenantID:   "tenant-a",
		FilePath:   tmp.Name(),
		Permission: "internal",
	})
	if err != nil {
		t.Fatalf("processTask returned error: %v", err)
	}
	if len(storer.chunks) != 2 {
		t.Fatalf("expected 2 chunks stored, got %d", len(storer.chunks))
	}
	// The parser-service path must report a known total so the frontend can
	// render "embedding done/total".
	foundTotal := false
	for _, s := range statuses.statuses {
		if s.Stage == "embedding" && s.TotalChunks == 2 {
			foundTotal = true
		}
	}
	if !foundTotal {
		t.Fatalf("expected an embedding stage with TotalChunks=2, got %+v", statuses.statuses)
	}
}

func TestProcessBatch_CheckpointTracksLastStoredChunk(t *testing.T) {
	cfg := baseTestConfig()
	storer := &resumableStorer{
		failIDs: map[string]error{
			"doc-cp_0001": errors.New("store failed"),
		},
	}
	ckpt := &checkpointStub{}
	p := New(cfg, vectorEmbedder{}, storer, metrics.NewCollector(10), ckpt, &dlqStub{})

	total := 0
	batch := []model.Chunk{
		{ChunkID: "doc-cp_0000", DocID: "doc-cp", TenantID: "tenant-a", Content: "first"},
		{ChunkID: "doc-cp_0001", DocID: "doc-cp", TenantID: "tenant-a", Content: "second"},
	}

	if err := p.processBatch(context.Background(), batch, "doc-cp", &total); err != nil {
		t.Fatalf("processBatch should tolerate a minority store failure, got: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected total=1 after single successful store, got %d", total)
	}
	if len(ckpt.saveCalls) != 1 {
		t.Fatalf("expected one checkpoint save, got %d", len(ckpt.saveCalls))
	}
	if ckpt.saveCalls[0].LastChunkID != "doc-cp_0000" {
		t.Fatalf("expected checkpoint last chunk doc-cp_0000, got %s", ckpt.saveCalls[0].LastChunkID)
	}
}
