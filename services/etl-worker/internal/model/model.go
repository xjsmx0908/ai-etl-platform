// Package model defines core domain types and interfaces for the ETL pipeline.
package model

import (
	"context"
	"time"
)

// Task represents a document processing task from the message queue.
type Task struct {
	// JobID and EventID identify the durable ingestion admission. They are
	// optional for backwards compatibility with pre-outbox messages.
	JobID      string            `json:"job_id,omitempty"`
	EventID    string            `json:"event_id,omitempty"`
	FilePath   string            `json:"file_path"`
	DocID      string            `json:"doc_id"`
	TenantID   string            `json:"tenant_id"`
	Permission string            `json:"permission,omitempty"` // "public" | "internal" | "confidential"
	FileHash   string            `json:"file_hash,omitempty"`  // SHA-256 of source file
	Metadata   map[string]string `json:"metadata,omitempty"`   // Business exact-match fields, e.g. order_id or trace_id
	UploadedBy string            `json:"uploaded_by,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
}

// TaskWithAck wraps a Task with acknowledgment callbacks for At-Least-Once delivery.
type TaskWithAck struct {
	Task Task
	Ack  func()
	Nack func(error)
}

// Chunk represents a semantic segment of a document with its vector representations.
type Chunk struct {
	ChunkID      string            `json:"chunk_id"`
	DocID        string            `json:"doc_id"`
	TenantID     string            `json:"tenant_id"`
	Content      string            `json:"content"`
	Index        int               `json:"index"`
	Vector       []float64         `json:"vector,omitempty"`        // Dense vector (OpenAI Embedding)
	SparseVector SparseVector      `json:"sparse_vector,omitempty"` // Sparse vector (BM25)
	TokenUsed    int               `json:"token_used"`
	CreatedAt    time.Time         `json:"created_at"`           // Chunk creation timestamp
	Permission   string            `json:"permission,omitempty"` // Inherited from Task
	FileHash     string            `json:"file_hash,omitempty"`  // SHA-256 of source file
	Metadata     map[string]string `json:"metadata,omitempty"`   // Business exact-match fields inherited from Task
}

// SparseVector represents a sparse vector in Qdrant format (indices + values).
type SparseVector struct {
	Indices []uint32  `json:"indices"`
	Values  []float32 `json:"values"`
}

// IsEmpty returns true if the sparse vector has no entries.
func (sv SparseVector) IsEmpty() bool {
	return len(sv.Indices) == 0
}

// Metric represents a processing event for observability.
type Metric struct {
	ChunkID   string
	DocID     string
	TenantID  string
	Stage     string // "parse" | "embed" | "store"
	Duration  time.Duration
	TokenUsed int
	Success   bool
	Error     string
}

// Checkpoint tracks processing progress for crash recovery.
type Checkpoint struct {
	DocID       string `json:"doc_id"`
	JobID       string `json:"job_id,omitempty"`
	ChunksDone  int    `json:"chunks_done"`
	LastChunkID string `json:"last_chunk_id"`
	PagesDone   int    `json:"pages_done,omitempty"`
	PagesTotal  int    `json:"pages_total,omitempty"`
	// ChunkIdentities is persisted for generation builds so a retry can resume
	// from the page checkpoint while still sealing a digest over all chunks.
	ChunkIdentities []CheckpointChunkIdentity `json:"chunk_identities,omitempty"`
}

type CheckpointChunkIdentity struct {
	ChunkID     string `json:"chunk_id"`
	Index       int    `json:"index"`
	ContentHash string `json:"content_hash"`
}

// TaskStatusState describes a document processing task lifecycle.
type TaskStatusState string

const (
	TaskStatusQueued     TaskStatusState = "queued"
	TaskStatusProcessing TaskStatusState = "processing"
	TaskStatusCompleted  TaskStatusState = "completed"
	TaskStatusFailed     TaskStatusState = "failed"
	TaskStatusCancelled  TaskStatusState = "cancelled"
)

// TaskStatus is a tenant-scoped read model for upload/worker task progress.
type TaskStatus struct {
	TaskID      string            `json:"task_id"`
	DocID       string            `json:"doc_id"`
	TenantID    string            `json:"tenant_id"`
	JobID       string            `json:"job_id,omitempty"`
	EventID     string            `json:"event_id,omitempty"`
	Status      TaskStatusState   `json:"status"`
	Stage       string            `json:"stage,omitempty"`
	ChunksDone  int               `json:"chunks_done,omitempty"`
	TotalChunks int               `json:"total_chunks,omitempty"`
	PagesDone   int               `json:"pages_done,omitempty"`
	PagesTotal  int               `json:"pages_total,omitempty"`
	Error       string            `json:"error,omitempty"`
	FilePath    string            `json:"file_path,omitempty"`
	FileHash    string            `json:"file_hash,omitempty"`
	Permission  string            `json:"permission,omitempty"`
	UploadedBy  string            `json:"uploaded_by,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	CompletedAt time.Time         `json:"completed_at,omitempty"`
}

// DLQMessage represents a failed task destined for the dead letter queue.
type DLQMessage struct {
	Task  Task      `json:"task"`
	Error string    `json:"error"`
	Time  time.Time `json:"time"`
}

// --- Interfaces ---

// TaskSource provides a stream of tasks from an external message queue.
type TaskSource interface {
	Consume(ctx context.Context) <-chan TaskWithAck
	Close() error
}

// Storer persists vector data with idempotent upsert semantics.
type Storer interface {
	Upsert(ctx context.Context, chunk Chunk) error
	Exists(ctx context.Context, chunkID string) (bool, error)
	Close() error
}

// CheckpointStore provides crash-recovery progress persistence.
type CheckpointStore interface {
	Save(ctx context.Context, cp Checkpoint) error
	Load(ctx context.Context, docID string) (Checkpoint, bool, error)
	Delete(ctx context.Context, docID string) error
	Close() error
}

// TaskStatusStore persists tenant-scoped task status for read-only status queries.
type TaskStatusStore interface {
	Save(ctx context.Context, status TaskStatus) error
	Load(ctx context.Context, tenantID string, taskID string) (TaskStatus, bool, error)
	Close() error
}

// DLQStore handles failed tasks that exhausted all retries.
type DLQStore interface {
	Push(ctx context.Context, msg DLQMessage) error
	List(ctx context.Context) ([]DLQMessage, error)
	Close() error
}

// Embedder converts text chunks into dense vector representations.
type Embedder interface {
	Embed(ctx context.Context, chunk *Chunk) error
	Close() error
}

// FullTextSink accepts chunks that should be indexed to a full-text engine asynchronously.
// Errors from this sink should not block the primary vector write path.
type FullTextSink interface {
	Enqueue(ctx context.Context, chunk Chunk) error
	Close() error
}
