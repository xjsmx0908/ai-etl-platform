// Package prometheus provides enterprise metrics collection.
package prometheus

import (
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds all Prometheus metrics for the ETL pipeline.
type Metrics struct {
	mu sync.Mutex

	// HTTP metrics
	HTTPRequestDuration *prometheus.HistogramVec
	HTTPRequestInFlight *prometheus.GaugeVec

	// Pipeline metrics
	ChunksProcessed *prometheus.CounterVec
	EmbedDuration   *prometheus.HistogramVec
	EmbedFailures   *prometheus.CounterVec
	StoreDuration   *prometheus.HistogramVec
	StoreFailures   *prometheus.CounterVec
	DLQMessages     *prometheus.CounterVec

	// Query metrics
	QueryDuration  *prometheus.HistogramVec
	QueryFailures  *prometheus.CounterVec
	RetrievalCount *prometheus.HistogramVec

	// Circuit breaker
	CircuitState *prometheus.GaugeVec
}

// New creates a new Metrics instance with all counters registered.
func New(namespace string) *Metrics {
	m := &Metrics{
		HTTPRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: "http",
				Name:      "request_duration_seconds",
				Help:      "HTTP request duration in seconds",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"method", "path", "status"},
		),
		HTTPRequestInFlight: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "http",
				Name:      "requests_in_flight",
				Help:      "Number of HTTP requests currently being processed",
			},
			[]string{"handler"},
		),
		ChunksProcessed: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "pipeline",
				Name:      "chunks_processed_total",
				Help:      "Total number of chunks processed",
			},
			[]string{"tenant_id", "status"},
		),
		EmbedDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: "embed",
				Name:      "duration_seconds",
				Help:      "Embedding API call duration",
				Buckets:   []float64{0.1, 0.5, 1, 2, 5, 10},
			},
			[]string{"tenant_id", "status"},
		),
		EmbedFailures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "embed",
				Name:      "failures_total",
				Help:      "Total embedding failures",
			},
			[]string{"tenant_id", "error"},
		),
		StoreDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: "store",
				Name:      "duration_seconds",
				Help:      "Vector store upsert duration",
				Buckets:   []float64{0.01, 0.05, 0.1, 0.5, 1},
			},
			[]string{"tenant_id", "status"},
		),
		StoreFailures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "store",
				Name:      "failures_total",
				Help:      "Total store failures",
			},
			[]string{"tenant_id", "error"},
		),
		DLQMessages: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "dlq",
				Name:      "messages_total",
				Help:      "Total messages sent to dead letter queue",
			},
			[]string{"reason"},
		),
		QueryDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: "query",
				Name:      "duration_seconds",
				Help:      "Query processing duration",
				Buckets:   []float64{0.5, 1, 2, 5, 10, 30},
			},
			[]string{"tenant_id", "status"},
		),
		QueryFailures: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "query",
				Name:      "failures_total",
				Help:      "Total query failures",
			},
			[]string{"tenant_id", "stage"},
		),
		RetrievalCount: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: "query",
				Name:      "retrieval_count",
				Help:      "Number of chunks retrieved per query",
				Buckets:   []float64{1, 3, 5, 10, 20},
			},
			[]string{"tenant_id"},
		),
		CircuitState: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "circuit",
				Name:      "state",
				Help:      "Circuit breaker state (0=closed, 1=open, 2=half-open)",
			},
			[]string{"name"},
		),
	}

	prometheus.MustRegister(
		m.HTTPRequestDuration,
		m.HTTPRequestInFlight,
		m.ChunksProcessed,
		m.EmbedDuration,
		m.EmbedFailures,
		m.StoreDuration,
		m.StoreFailures,
		m.DLQMessages,
		m.QueryDuration,
		m.QueryFailures,
		m.RetrievalCount,
		m.CircuitState,
	)

	return m
}

// Handler returns an HTTP handler for the /metrics endpoint.
func (m *Metrics) Handler() http.Handler {
	return promhttp.Handler()
}
