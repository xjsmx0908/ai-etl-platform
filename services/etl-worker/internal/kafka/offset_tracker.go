package kafka

import (
	"sync"

	kafkago "github.com/segmentio/kafka-go"
)

type partitionKey struct {
	topic     string
	partition int
}

type partitionOffsets struct {
	order   []int64
	entries map[int64]bool
}

// offsetTracker prevents a later-completing worker from committing past an
// earlier unfinished message in the same partition. Kafka commits are partition
// high-water marks, so committing offset 12 also commits 10 and 11.
type offsetTracker struct {
	mu         sync.Mutex
	partitions map[partitionKey]*partitionOffsets
}

func newOffsetTracker() *offsetTracker {
	return &offsetTracker{partitions: make(map[partitionKey]*partitionOffsets)}
}

func (t *offsetTracker) Register(msg kafkago.Message) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := partitionKey{topic: msg.Topic, partition: msg.Partition}
	p, ok := t.partitions[key]
	if !ok {
		p = &partitionOffsets{entries: make(map[int64]bool)}
		t.partitions[key] = p
	}
	if _, exists := p.entries[msg.Offset]; !exists {
		p.entries[msg.Offset] = false
		p.order = append(p.order, msg.Offset)
	}
}

func (t *offsetTracker) Complete(msg kafkago.Message) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := partitionKey{topic: msg.Topic, partition: msg.Partition}
	if p := t.partitions[key]; p != nil {
		if _, ok := p.entries[msg.Offset]; ok {
			p.entries[msg.Offset] = true
		}
	}
}

func (t *offsetTracker) Candidate(msg kafkago.Message) (kafkago.Message, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := partitionKey{topic: msg.Topic, partition: msg.Partition}
	p := t.partitions[key]
	if p == nil {
		return kafkago.Message{}, false
	}
	var highest int64
	found := false
	for _, offset := range p.order {
		if !p.entries[offset] {
			break
		}
		highest = offset
		found = true
	}
	if !found {
		return kafkago.Message{}, false
	}
	candidate := msg
	candidate.Topic, candidate.Partition, candidate.Offset = key.topic, key.partition, highest
	return candidate, true
}

func (t *offsetTracker) Confirm(msg kafkago.Message) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := partitionKey{topic: msg.Topic, partition: msg.Partition}
	p := t.partitions[key]
	if p == nil {
		return 0
	}
	consumed := 0
	for _, offset := range p.order {
		delete(p.entries, offset)
		consumed++
		if offset == msg.Offset {
			break
		}
	}
	p.order = p.order[consumed:]
	return consumed
}
