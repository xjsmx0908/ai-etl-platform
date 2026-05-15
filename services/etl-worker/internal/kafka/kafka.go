// Package kafka provides Kafka-based task source and dead letter queue implementations.
package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"ai-etl-pipeline/internal/model"
)

// --- Real Kafka Source ---

// Producer publishes tasks to a Kafka topic (used by the upload gateway).
type Producer struct {
	writer *kafkago.Writer
}

// NewProducer creates a Kafka producer for publishing document processing tasks.
func NewProducer(brokers, topic string) (*Producer, error) {
	writer := &kafkago.Writer{
		Addr:         kafkago.TCP(strings.Split(brokers, ",")...),
		Topic:        topic,
		Balancer:     &kafkago.LeastBytes{},
		BatchTimeout: 50 * time.Millisecond,
		RequiredAcks: kafkago.RequireAll,
	}
	slog.Info("kafka producer connected", "brokers", brokers, "topic", topic)
	return &Producer{writer: writer}, nil
}

// Publish sends a task to Kafka. Returns error if write fails.
func (p *Producer) Publish(ctx context.Context, task model.Task) error {
	value, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}
	err = p.writer.WriteMessages(ctx, kafkago.Message{
		Key:   []byte(task.DocID),
		Value: value,
		Headers: []kafkago.Header{
			{Key: "tenant_id", Value: []byte(task.TenantID)},
			{Key: "created_at", Value: []byte(task.CreatedAt.Format(time.RFC3339))},
		},
	})
	if err != nil {
		return fmt.Errorf("kafka publish: %w", err)
	}
	slog.Info("task published", "doc_id", task.DocID, "tenant_id", task.TenantID)
	return nil
}

// Close shuts down the producer.
func (p *Producer) Close() error {
	return p.writer.Close()
}

// --- Real Kafka Source ---

// Source consumes tasks from Kafka with manual offset commit (At-Least-Once).
type Source struct {
	reader *kafkago.Reader
	dlq    model.DLQStore
}

// NewSource creates a Kafka consumer connected to the given topic and group.
func NewSource(brokers, topic, groupID string, dlq model.DLQStore) (*Source, error) {
	reader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:        strings.Split(brokers, ","),
		Topic:          topic,
		GroupID:        groupID,
		MinBytes:       1, // Allow low-throughput/small messages to be consumed promptly.
		MaxBytes:       10e6,
		MaxWait:        3 * time.Second,
		CommitInterval: 0, // Manual commit (At-Least-Once)
		// For a new consumer group with no committed offset, start from earliest.
		// This avoids missing the first message when topic/partition is created just-in-time.
		StartOffset:            kafkago.FirstOffset,
		WatchPartitionChanges:  true,
		PartitionWatchInterval: 5 * time.Second,
	})

	slog.Info("kafka source connected",
		"brokers", brokers, "topic", topic, "group", groupID)

	return &Source{reader: reader, dlq: dlq}, nil
}

// Consume returns a channel of tasks. Ack commits the offset; Nack sends to DLQ.
func (ks *Source) Consume(ctx context.Context) <-chan model.TaskWithAck {
	ch := make(chan model.TaskWithAck, 10)
	go func() {
		defer close(ch)
		for {
			msg, err := ks.reader.FetchMessage(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Error("kafka fetch error", "error", err)
				time.Sleep(time.Second)
				continue
			}

			var task model.Task
			if err := json.Unmarshal(msg.Value, &task); err != nil {
				slog.Error("kafka message decode error",
					"offset", msg.Offset, "error", err)
				_ = ks.reader.CommitMessages(ctx, msg)
				continue
			}

			twa := model.TaskWithAck{
				Task: task,
				Ack: func() {
					if err := ks.reader.CommitMessages(ctx, msg); err != nil {
						slog.Error("kafka commit failed",
							"offset", msg.Offset, "error", err)
					} else {
						slog.Debug("kafka offset committed",
							"offset", msg.Offset, "doc_id", task.DocID)
					}
				},
				Nack: func(err error) {
					slog.Warn("message not committed due to nack, will be retried",
						"offset", msg.Offset, "doc_id", task.DocID, "error", err)
				},
			}

			select {
			case ch <- twa:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}

// Close shuts down the Kafka reader.
func (ks *Source) Close() error {
	slog.Info("kafka source closing")
	return ks.reader.Close()
}

// --- Real Kafka DLQ Producer ---

// DLQ writes failed tasks to a Kafka dead letter topic.
type DLQ struct {
	writer *kafkago.Writer
}

// NewDLQ creates a DLQ producer for the given topic.
func NewDLQ(brokers, topic string) (*DLQ, error) {
	writer := &kafkago.Writer{
		Addr:         kafkago.TCP(strings.Split(brokers, ",")...),
		Topic:        topic,
		Balancer:     &kafkago.LeastBytes{},
		BatchTimeout: 100 * time.Millisecond,
		RequiredAcks: kafkago.RequireAll,
	}

	slog.Info("kafka DLQ connected", "brokers", brokers, "topic", topic)
	return &DLQ{writer: writer}, nil
}

// Push writes a failed task to the DLQ topic.
func (d *DLQ) Push(ctx context.Context, msg model.DLQMessage) error {
	value, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	err = d.writer.WriteMessages(ctx, kafkago.Message{
		Key:   []byte(msg.Task.DocID),
		Value: value,
		Headers: []kafkago.Header{
			{Key: "error", Value: []byte(msg.Error)},
			{Key: "timestamp", Value: []byte(msg.Time.Format(time.RFC3339))},
		},
	})
	if err != nil {
		slog.Error("kafka DLQ write failed", "doc_id", msg.Task.DocID, "error", err)
		return err
	}

	slog.Warn("message sent to DLQ", "doc_id", msg.Task.DocID, "error", msg.Error)
	return nil
}

// List is not supported for Kafka DLQ (use Kafka UI or dedicated consumer).
func (d *DLQ) List(_ context.Context) ([]model.DLQMessage, error) {
	return nil, nil
}

// Close shuts down the DLQ writer.
func (d *DLQ) Close() error {
	return d.writer.Close()
}

// --- Mock Source (development) ---

// MockSource simulates a Kafka consumer for development.
type MockSource struct {
	messages []model.Task
	offsets  map[int]bool
	dlq      model.DLQStore
	mu       sync.Mutex
	closed   bool
}

// NewMockSource creates a mock source with predefined tasks.
func NewMockSource(messages []model.Task, dlq model.DLQStore) *MockSource {
	return &MockSource{
		messages: messages,
		offsets:  make(map[int]bool),
		dlq:      dlq,
	}
}

// Consume returns tasks with simulated delivery delay.
func (ks *MockSource) Consume(ctx context.Context) <-chan model.TaskWithAck {
	ch := make(chan model.TaskWithAck, 10)
	go func() {
		defer close(ch)
		for idx, msg := range ks.messages {
			select {
			case <-ctx.Done():
				return
			default:
			}

			ks.mu.Lock()
			if ks.closed || ks.offsets[idx] {
				ks.mu.Unlock()
				continue
			}
			ks.mu.Unlock()

			offset := idx
			task := msg
			twa := model.TaskWithAck{
				Task: task,
				Ack: func() {
					ks.mu.Lock()
					ks.offsets[offset] = true
					ks.mu.Unlock()
					slog.Debug("offset committed", "offset", offset, "doc_id", task.DocID)
				},
				Nack: func(err error) {
					slog.Warn("mock source nack, offset not committed",
						"offset", offset, "doc_id", task.DocID, "error", err)
				},
			}

			time.Sleep(time.Duration(10+rand.Intn(20)) * time.Millisecond)

			select {
			case ch <- twa:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}

// Close marks the mock source as closed.
func (ks *MockSource) Close() error {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	ks.closed = true
	committed := 0
	for _, v := range ks.offsets {
		if v {
			committed++
		}
	}
	slog.Info("kafka source closed", "committed", committed, "total", len(ks.messages))
	return nil
}

// --- Mock DLQ (development) ---

// MemoryDLQ provides an in-memory DLQ for development.
type MemoryDLQ struct {
	mu       sync.Mutex
	messages []model.DLQMessage
}

// NewMemoryDLQ creates an in-memory DLQ.
func NewMemoryDLQ() *MemoryDLQ {
	return &MemoryDLQ{}
}

func (d *MemoryDLQ) Push(_ context.Context, msg model.DLQMessage) error {
	d.mu.Lock()
	d.messages = append(d.messages, msg)
	d.mu.Unlock()
	slog.Warn("DLQ message added", "doc_id", msg.Task.DocID, "error", msg.Error)
	return nil
}

func (d *MemoryDLQ) List(_ context.Context) ([]model.DLQMessage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]model.DLQMessage, len(d.messages))
	copy(out, d.messages)
	return out, nil
}

func (d *MemoryDLQ) Close() error { return nil }
