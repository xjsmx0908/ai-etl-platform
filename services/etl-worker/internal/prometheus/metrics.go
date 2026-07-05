// Package prometheus provides enterprise metrics collection.
package prometheus

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-etl-pipeline/internal/agent"

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

	// Agent orchestration metrics
	AgentRunsStarted       *prometheus.CounterVec
	AgentRunCompletions    *prometheus.CounterVec
	AgentRunDuration       *prometheus.HistogramVec
	AgentToolSteps         *prometheus.CounterVec
	AgentToolStepDuration  *prometheus.HistogramVec
	AgentApprovalDecisions *prometheus.CounterVec
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
		AgentRunsStarted: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "agent",
				Name:      "runs_started_total",
				Help:      "Total Agent runs created",
			},
			[]string{"auto_execute"},
		),
		AgentRunCompletions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "agent",
				Name:      "run_completions_total",
				Help:      "Total Agent runs reaching a terminal state",
			},
			[]string{"state", "error_type"},
		),
		AgentRunDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: "agent",
				Name:      "run_duration_seconds",
				Help:      "Agent run duration from creation to terminal state",
				Buckets:   []float64{0.1, 0.5, 1, 2, 5, 10, 30, 60, 300, 1800},
			},
			[]string{"state", "error_type"},
		),
		AgentToolSteps: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "agent",
				Name:      "tool_steps_total",
				Help:      "Total Agent tool steps by terminal or approval-waiting state",
			},
			[]string{"tool_name", "state"},
		),
		AgentToolStepDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: "agent",
				Name:      "tool_step_duration_seconds",
				Help:      "Agent tool step duration by tool and state",
				Buckets:   []float64{0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10, 30, 60},
			},
			[]string{"tool_name", "state"},
		),
		AgentApprovalDecisions: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "agent",
				Name:      "approval_decisions_total",
				Help:      "Total Agent approval decisions",
			},
			[]string{"decision", "tool_name"},
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
		m.AgentRunsStarted,
		m.AgentRunCompletions,
		m.AgentRunDuration,
		m.AgentToolSteps,
		m.AgentToolStepDuration,
		m.AgentApprovalDecisions,
	)

	return m
}

// Handler returns an HTTP handler for the /metrics endpoint.
func (m *Metrics) Handler() http.Handler {
	return promhttp.Handler()
}

// HandlerFor returns a /metrics handler backed by the provided gatherer.
func (m *Metrics) HandlerFor(gatherer prometheus.Gatherer) http.Handler {
	return promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{})
}

// HTTPMiddleware records request count and latency for application routes.
func (m *Metrics) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}
		path := routeLabel(r.URL.Path)

		m.HTTPRequestInFlight.WithLabelValues(path).Inc()
		defer func() {
			m.HTTPRequestInFlight.WithLabelValues(path).Dec()
			status := strconv.Itoa(rec.statusCode)
			m.HTTPRequestDuration.WithLabelValues(r.Method, path, status).Observe(time.Since(start).Seconds())
		}()

		next.ServeHTTP(rec, r)
	})
}

// SetCircuitState records circuit breaker state (0=closed, 1=open, 2=half-open).
func (m *Metrics) SetCircuitState(name string, state int) {
	m.CircuitState.WithLabelValues(name).Set(float64(state))
}

// RecordAgentRunStarted records an Agent run creation event.
func (m *Metrics) RecordAgentRunStarted(autoExecute bool) {
	m.AgentRunsStarted.WithLabelValues(strconv.FormatBool(autoExecute)).Inc()
}

// RecordAgentRunFinished records an Agent run terminal event.
func (m *Metrics) RecordAgentRunFinished(state agent.RunState, errorType string, duration time.Duration) {
	duration = nonNegativeDuration(duration)
	errorType = normalizedLabel(errorType, "none")
	m.AgentRunCompletions.WithLabelValues(string(state), errorType).Inc()
	m.AgentRunDuration.WithLabelValues(string(state), errorType).Observe(duration.Seconds())
}

// RecordAgentToolStep records a completed, failed, cancelled, or approval-waiting tool step.
func (m *Metrics) RecordAgentToolStep(toolName string, state agent.RunState, duration time.Duration) {
	duration = nonNegativeDuration(duration)
	toolName = normalizedLabel(toolName, "unknown")
	m.AgentToolSteps.WithLabelValues(toolName, string(state)).Inc()
	m.AgentToolStepDuration.WithLabelValues(toolName, string(state)).Observe(duration.Seconds())
}

// RecordAgentApprovalDecision records a durable approval decision.
func (m *Metrics) RecordAgentApprovalDecision(decision agent.ApprovalStatus, toolName string) {
	toolName = normalizedLabel(toolName, "unknown")
	m.AgentApprovalDecisions.WithLabelValues(string(decision), toolName).Inc()
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	if r.statusCode == 0 {
		r.statusCode = http.StatusOK
	}
	return r.ResponseWriter.Write(data)
}

func routeLabel(path string) string {
	switch {
	case path == "/healthz":
		return "/healthz"
	case path == "/readyz":
		return "/readyz"
	case path == "/metrics":
		return "/metrics"
	case path == "/version":
		return "/version"
	case strings.HasPrefix(path, "/v1/upload"):
		return "/v1/upload"
	case strings.HasPrefix(path, "/v1/query"):
		return "/v1/query"
	case strings.HasPrefix(path, "/v1/agent/runs"):
		return "/v1/agent/runs"
	default:
		return "/other"
	}
}

func normalizedLabel(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func nonNegativeDuration(duration time.Duration) time.Duration {
	if duration < 0 {
		return 0
	}
	return duration
}
