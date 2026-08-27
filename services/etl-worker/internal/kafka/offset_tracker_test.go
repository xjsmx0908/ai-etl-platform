package kafka

import (
	"testing"

	kafkago "github.com/segmentio/kafka-go"
)

func TestOffsetTrackerCommitsOnlyContiguousCompletedPrefix(t *testing.T) {
	tracker := newOffsetTracker()
	m1 := kafkago.Message{Topic: "tasks", Partition: 2, Offset: 10}
	m2 := kafkago.Message{Topic: "tasks", Partition: 2, Offset: 11}
	m3 := kafkago.Message{Topic: "tasks", Partition: 2, Offset: 12}
	tracker.Register(m1)
	tracker.Register(m2)
	tracker.Register(m3)

	tracker.Complete(m2)
	if _, ok := tracker.Candidate(m2); ok {
		t.Fatal("offset 11 must not commit while offset 10 is unfinished")
	}
	tracker.Complete(m1)
	candidate, ok := tracker.Candidate(m1)
	if !ok || candidate.Offset != 11 {
		t.Fatalf("candidate = offset %d ok=%v, want contiguous offset 11", candidate.Offset, ok)
	}
	if confirmed := tracker.Confirm(candidate); confirmed != 2 {
		t.Fatalf("confirmed offsets = %d, want 2", confirmed)
	}

	tracker.Complete(m3)
	candidate, ok = tracker.Candidate(m3)
	if !ok || candidate.Offset != 12 {
		t.Fatalf("next candidate = offset %d ok=%v, want 12", candidate.Offset, ok)
	}
}

func TestOffsetTrackerKeepsPartitionsIndependent(t *testing.T) {
	tracker := newOffsetTracker()
	blocked := kafkago.Message{Topic: "tasks", Partition: 1, Offset: 5}
	ready := kafkago.Message{Topic: "tasks", Partition: 2, Offset: 8}
	tracker.Register(blocked)
	tracker.Register(ready)
	tracker.Complete(ready)

	candidate, ok := tracker.Candidate(ready)
	if !ok || candidate.Partition != 2 || candidate.Offset != 8 {
		t.Fatalf("independent partition candidate = %+v ok=%v", candidate, ok)
	}
}

func TestOffsetTrackerAllowsCompactedOffsetGaps(t *testing.T) {
	tracker := newOffsetTracker()
	first := kafkago.Message{Topic: "tasks", Partition: 1, Offset: 10}
	nextVisible := kafkago.Message{Topic: "tasks", Partition: 1, Offset: 12}
	tracker.Register(first)
	tracker.Register(nextVisible)
	tracker.Complete(first)
	tracker.Complete(nextVisible)

	candidate, ok := tracker.Candidate(nextVisible)
	if !ok || candidate.Offset != 12 {
		t.Fatalf("compacted candidate = offset %d ok=%v, want 12", candidate.Offset, ok)
	}
}
