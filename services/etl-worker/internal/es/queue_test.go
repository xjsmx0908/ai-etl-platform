package es

import (
	"context"
	"testing"
	"time"

	"ai-etl-pipeline/internal/model"
)

func TestMemoryRetryQueue_DelayedPopOrder(t *testing.T) {
	q := NewMemoryRetryQueue(10)
	defer q.Close()

	now := time.Now().UTC()
	msgLate := RetryMessage{Op: queueOpIndex, Chunk: model.Chunk{ChunkID: "c-late"}, NextRetryAt: now.Add(120 * time.Millisecond)}
	msgSoon := RetryMessage{Op: queueOpIndex, Chunk: model.Chunk{ChunkID: "c-soon"}, NextRetryAt: now.Add(20 * time.Millisecond)}

	if err := q.Enqueue(context.Background(), msgLate); err != nil {
		t.Fatalf("enqueue late: %v", err)
	}
	if err := q.Enqueue(context.Background(), msgSoon); err != nil {
		t.Fatalf("enqueue soon: %v", err)
	}

	started := time.Now()
	first, ok, err := q.Pop(context.Background(), 300*time.Millisecond)
	if err != nil {
		t.Fatalf("pop first: %v", err)
	}
	if !ok {
		t.Fatal("expected first pop ok=true")
	}
	if first.Chunk.ChunkID != "c-soon" {
		t.Fatalf("expected c-soon first, got %s", first.Chunk.ChunkID)
	}
	if time.Since(started) < 15*time.Millisecond {
		t.Fatalf("expected delayed pop wait, got too fast: %v", time.Since(started))
	}

	second, ok, err := q.Pop(context.Background(), 300*time.Millisecond)
	if err != nil {
		t.Fatalf("pop second: %v", err)
	}
	if !ok {
		t.Fatal("expected second pop ok=true")
	}
	if second.Chunk.ChunkID != "c-late" {
		t.Fatalf("expected c-late second, got %s", second.Chunk.ChunkID)
	}
}

func TestMemoryRetryQueue_DeadLetter(t *testing.T) {
	q := NewMemoryRetryQueue(10)
	defer q.Close()

	msg := RetryMessage{Op: queueOpIndex, Chunk: model.Chunk{ChunkID: "c1"}, Retry: 3}
	if err := q.EnqueueDeadLetter(context.Background(), msg); err != nil {
		t.Fatalf("enqueue dead-letter: %v", err)
	}
	items := q.DeadLetters()
	if len(items) != 1 {
		t.Fatalf("expected 1 dead-letter item, got %d", len(items))
	}
	if items[0].Chunk.ChunkID != "c1" || items[0].Retry != 3 {
		t.Fatalf("unexpected dead-letter item: %+v", items[0])
	}
}
