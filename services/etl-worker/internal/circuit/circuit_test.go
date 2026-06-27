package circuit

import (
	"errors"
	"testing"
	"time"
)

func TestStateObserverReceivesInitialClosedState(t *testing.T) {
	defer SetStateObserver(nil)

	var gotName string
	gotState := -1
	SetStateObserver(func(name string, state int) {
		gotName = name
		gotState = state
	})

	New("embedder", 1, time.Millisecond)

	if gotName != "embedder" {
		t.Fatalf("expected observer name embedder, got %q", gotName)
	}
	if gotState != 0 {
		t.Fatalf("expected closed state 0, got %d", gotState)
	}
}

func TestStateObserverReceivesOpenStateAfterConsecutiveFailures(t *testing.T) {
	defer SetStateObserver(nil)

	states := make([]int, 0, 2)
	SetStateObserver(func(_ string, state int) {
		states = append(states, state)
	})

	breaker := New("llm-api", 1, time.Hour)
	for i := 0; i < 6; i++ {
		_, _ = breaker.Execute(func() (any, error) {
			return nil, errors.New("dependency failed")
		})
	}

	if !containsState(states, 1) {
		t.Fatalf("expected observer to receive open state 1, got %v", states)
	}
	failures, successes := breaker.Stats()
	if failures != 6 {
		t.Fatalf("expected 6 failures, got %d", failures)
	}
	if successes != 0 {
		t.Fatalf("expected 0 successes, got %d", successes)
	}
}

func containsState(states []int, want int) bool {
	for _, state := range states {
		if state == want {
			return true
		}
	}
	return false
}
