package es

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"ai-etl-pipeline/internal/model"
)

func TestAsyncSink_Enqueue_ImmediateFailThenReplaySuccess(t *testing.T) {
	var indexCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			if r.URL.Path == "/documents_text/_doc/doc-1_0001" {
				call := indexCalls.Add(1)
				if call == 1 {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":"temporary"}`))
					return
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"result":"created"}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	idx, err := NewHTTPIndexer(srv.URL, "", "documents_text")
	if err != nil {
		t.Fatalf("new indexer: %v", err)
	}
	queue := NewMemoryRetryQueue(10)
	sink := NewAsyncSink(idx, queue, 100*time.Millisecond, 3, 10*time.Millisecond, 100*time.Millisecond, 0)
	defer sink.Close()

	err = sink.Enqueue(context.Background(), model.Chunk{
		ChunkID:   "doc-1_0001",
		DocID:     "doc-1",
		TenantID:  "tenant-a",
		Content:   "x",
		Index:     1,
		CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if indexCalls.Load() >= 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected replay index call, got %d", indexCalls.Load())
}

func TestAsyncSink_Enqueue_WhenQueueFails_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	idx, err := NewHTTPIndexer(srv.URL, "", "documents_text")
	if err != nil {
		t.Fatalf("new indexer: %v", err)
	}
	defer idx.Close()

	badQueue := &queueStub{enqueueErr: context.DeadlineExceeded}
	sink := NewAsyncSink(idx, badQueue, time.Second, 3, time.Second, 5*time.Second, 0)
	defer sink.Close()

	err = sink.Enqueue(context.Background(), model.Chunk{
		ChunkID: "doc-1_0001",
		DocID:   "doc-1",
		Content: "x",
	})
	if err == nil {
		t.Fatal("expected enqueue error when queue write fails")
	}
}

type queueStub struct {
	enqueueErr error
}

func (q *queueStub) Enqueue(_ context.Context, _ RetryMessage) error {
	return q.enqueueErr
}
func (q *queueStub) Pop(context.Context, time.Duration) (RetryMessage, bool, error) {
	return RetryMessage{}, false, nil
}
func (q *queueStub) EnqueueDeadLetter(context.Context, RetryMessage) error {
	return nil
}
func (q *queueStub) Close() error {
	return nil
}

func TestAsyncSink_RetryBackoffAndDeadLetter(t *testing.T) {
	// always fail with retryable 500
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"temporary"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	idx, err := NewHTTPIndexer(srv.URL, "", "documents_text")
	if err != nil {
		t.Fatalf("new indexer: %v", err)
	}
	queue := NewMemoryRetryQueue(100)
	sink := NewAsyncSink(idx, queue, 20*time.Millisecond, 3, 10*time.Millisecond, 40*time.Millisecond, 0)
	defer sink.Close()

	err = sink.Enqueue(context.Background(), model.Chunk{
		ChunkID: "doc-1_0001",
		DocID:   "doc-1",
		Content: "x",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(queue.DeadLetters()) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected message moved to dead-letter after max retries")
}

func TestIsRetryableError(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("es index failed: status=500 body=x"), true},
		{fmt.Errorf("es index failed: status=429 body=x"), true},
		{fmt.Errorf("es index failed: status=408 body=x"), true},
		{fmt.Errorf("es index failed: status=400 body=x"), false},
		{fmt.Errorf("es index failed: status=401 body=x"), false},
		{fmt.Errorf("dial tcp 127.0.0.1:9200: connect: connection refused"), true},
	}
	for _, tc := range cases {
		got := isRetryableError(tc.err)
		if got != tc.want {
			t.Fatalf("isRetryableError(%q)=%v want %v", tc.err, got, tc.want)
		}
	}
}
