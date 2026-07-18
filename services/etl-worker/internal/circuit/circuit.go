// Package circuit provides a circuit breaker for external API calls.
package circuit

import (
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sony/gobreaker"
)

// StateObserver receives circuit state updates as 0=closed, 1=open, 2=half-open.
type StateObserver func(name string, state int)

var (
	observerMu sync.RWMutex
	observer   StateObserver
)

// SetStateObserver registers a process-wide observer for circuit state changes.
func SetStateObserver(fn StateObserver) {
	observerMu.Lock()
	defer observerMu.Unlock()
	observer = fn
}

// Breaker wraps gobreaker with metrics tracking.
type Breaker struct {
	cb        *gobreaker.CircuitBreaker
	failures  atomic.Int64
	successes atomic.Int64
}

// New creates a circuit breaker with enterprise defaults.
func New(name string, maxRequests uint32, timeout time.Duration) *Breaker {
	cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        name,
		MaxRequests: maxRequests,
		Timeout:     timeout,
		ReadyToTrip: func(counts gobreaker.Counts) bool {
			return counts.ConsecutiveFailures > 5
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			slog.Info("circuit breaker state changed",
				"name", name, "from", from.String(), "to", to.String())
			notifyState(name, to)
		},
	})
	notifyState(name, gobreaker.StateClosed)
	return &Breaker{cb: cb}
}

// Execute runs fn through the circuit breaker.
func (b *Breaker) Execute(fn func() (any, error)) (any, error) {
	result, err := b.cb.Execute(fn)
	if err != nil {
		b.failures.Add(1)
	} else {
		b.successes.Add(1)
	}
	return result, err
}

// Stats returns breaker statistics.
func (b *Breaker) Stats() (failures, successes int64) {
	return b.failures.Load(), b.successes.Load()
}

// IsRejected reports whether the circuit breaker rejected execution before calling the dependency.
func IsRejected(err error) bool {
	return errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests)
}

func notifyState(name string, state gobreaker.State) {
	observerMu.RLock()
	fn := observer
	observerMu.RUnlock()
	if fn != nil {
		fn(name, stateCode(state))
	}
}

func stateCode(state gobreaker.State) int {
	switch state {
	case gobreaker.StateOpen:
		return 1
	case gobreaker.StateHalfOpen:
		return 2
	default:
		return 0
	}
}
