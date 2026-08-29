package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"ai-etl-pipeline/internal/indexmanifest"
)

type generationOperationsReaderStub struct {
	snapshot indexmanifest.OperationsSnapshot
	maxSeen  chan int
}

func (s *generationOperationsReaderStub) OperationsSnapshot(_ context.Context, maxRepairs int) (indexmanifest.OperationsSnapshot, error) {
	s.maxSeen <- maxRepairs
	return s.snapshot, nil
}

type generationOperationsObserverStub struct {
	observed chan indexmanifest.OperationsSnapshot
}

func (s *generationOperationsObserverStub) SetGenerationOperations(snapshot indexmanifest.OperationsSnapshot) {
	s.observed <- snapshot
}

func TestGenerationOperationsMonitorPublishesImmediateDurableSnapshot(t *testing.T) {
	want := indexmanifest.OperationsSnapshot{
		Manifests:       map[indexmanifest.ManifestState]int{indexmanifest.StateActive: 2},
		BackendDiverged: 1,
	}
	reader := &generationOperationsReaderStub{snapshot: want, maxSeen: make(chan int, 1)}
	observer := &generationOperationsObserverStub{observed: make(chan indexmanifest.OperationsSnapshot, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runGenerationOperationsMonitor(ctx, reader, observer, time.Hour, 3)
		close(done)
	}()

	if got := <-observer.observed; !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot=%+v, want %+v", got, want)
	}
	if got := <-reader.maxSeen; got != 3 {
		t.Fatalf("max repairs=%d, want 3", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop after cancellation")
	}
}
