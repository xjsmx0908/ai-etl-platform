// Package circuit provides a circuit breaker for external API calls.
package circuit

import (
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/sony/gobreaker"
)

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
		},
	})
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
