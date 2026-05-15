package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/metrics"
	"ai-etl-pipeline/internal/model"
)

type noopEmbedder struct{}

func (noopEmbedder) Embed(context.Context, *model.Chunk) error { return nil }
func (noopEmbedder) Close() error                              { return nil }

type noopStorer struct{}

func (noopStorer) Upsert(context.Context, model.Chunk) error    { return nil }
func (noopStorer) Exists(context.Context, string) (bool, error) { return false, nil }
func (noopStorer) Close() error                                 { return nil }

type noopCheckpoint struct{}

func (noopCheckpoint) Save(context.Context, model.Checkpoint) error { return nil }
func (noopCheckpoint) Load(context.Context, string) (model.Checkpoint, bool, error) {
	return model.Checkpoint{}, false, nil
}
func (noopCheckpoint) Delete(context.Context, string) error { return nil }
func (noopCheckpoint) Close() error                         { return nil }

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
	p := New(baseTestConfig(), noopEmbedder{}, noopStorer{}, metrics.NewCollector(10), noopCheckpoint{}, dlq)

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
}

func TestHandleTask_DoesNotCommitWhenDLQFails(t *testing.T) {
	dlq := &dlqStub{err: errors.New("dlq unavailable")}
	p := New(baseTestConfig(), noopEmbedder{}, noopStorer{}, metrics.NewCollector(10), noopCheckpoint{}, dlq)

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
}
