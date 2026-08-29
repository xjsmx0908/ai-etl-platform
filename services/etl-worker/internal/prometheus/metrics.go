// Package prometheus provides enterprise metrics collection.
package prometheus

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-etl-pipeline/internal/agent"
	"ai-etl-pipeline/internal/indexmanifest"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Per-1k-token USD prices used to estimate LLM cost. Zero when unset, which
// keeps the cost metric at zero rather than misreporting.
var (
	llmPromptPricePer1K     = envFloat("LLM_PRICE_PROMPT_PER_1K", 0)
	llmCompletionPricePer1K = envFloat("LLM_PRICE_COMPLETION_PER_1K", 0)
)

func envFloat(key string, defaultVal float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return defaultVal
}

// Metrics holds all Prometheus metrics for the ETL pipeline.
type Metrics struct {
	mu                     sync.Mutex
	llmConsecutiveFailures map[string]int

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
	// ESDeadLetter counts chunks that permanently failed ES indexing. Non-zero
	// means Qdrant and ES are silently diverging — alert on it.
	ESDeadLetter             *prometheus.CounterVec
	IngestionOutboxPending   prometheus.Gauge
	IngestionOutboxRetried   prometheus.Gauge
	IngestionOutboxOldestAge prometheus.Gauge
	IngestionJobs            *prometheus.GaugeVec
	IngestionExpiredLeases   prometheus.Gauge

	// Generation metrics intentionally use only fixed lifecycle/outcome labels.
	GenerationManifests       *prometheus.GaugeVec
	GenerationOldestAge       *prometheus.GaugeVec
	GenerationDiagnostics     *prometheus.GaugeVec
	GenerationReconciliations *prometheus.CounterVec
	GenerationRetentions      *prometheus.CounterVec
	GenerationRollbacks       *prometheus.CounterVec

	// Query metrics
	QueryDuration          *prometheus.HistogramVec
	QueryFailures          *prometheus.CounterVec
	RetrievalCount         *prometheus.HistogramVec
	LLMRequests            *prometheus.CounterVec
	LLMRequestDuration     *prometheus.HistogramVec
	LLMConsecutiveFailures *prometheus.GaugeVec
	// LLMTokens counts consumed prompt/completion tokens by model.
	LLMTokens *prometheus.CounterVec
	// LLMCostUSD accumulates estimated spend by model, from configured per-1k
	// prices. Zero when prices are not configured.
	LLMCostUSD *prometheus.CounterVec

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
		llmConsecutiveFailures: make(map[string]int),
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
		ESDeadLetter: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "es",
				Name:      "deadletter_total",
				Help:      "Chunks that permanently failed ES indexing (Qdrant/ES divergence)",
			},
			[]string{"reason"},
		),
		IngestionOutboxPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "ingestion", Name: "outbox_pending",
			Help: "Committed ingestion outbox events not yet published to Kafka",
		}),
		IngestionOutboxRetried: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "ingestion", Name: "outbox_retried",
			Help: "Pending ingestion outbox events with at least one publication attempt",
		}),
		IngestionOutboxOldestAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "ingestion", Name: "outbox_oldest_age_seconds",
			Help: "Age in seconds of the oldest pending ingestion outbox event",
		}),
		IngestionJobs: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "ingestion", Name: "jobs",
			Help: "Durable ingestion jobs by lifecycle state",
		}, []string{"status"}),
		IngestionExpiredLeases: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "ingestion", Name: "expired_processing_leases",
			Help: "Processing ingestion jobs whose recovery lease has expired",
		}),
		GenerationManifests: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "generation", Name: "manifests",
			Help: "Durable index generation manifests by lifecycle state",
		}, []string{"state"}),
		GenerationOldestAge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "generation", Name: "oldest_age_seconds",
			Help: "Age of the oldest durable index generation by lifecycle state",
		}, []string{"state"}),
		GenerationDiagnostics: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Subsystem: "generation", Name: "diagnostics",
			Help: "Current durable index generation health diagnostics",
		}, []string{"condition"}),
		GenerationReconciliations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "generation", Name: "reconciliations_total",
			Help: "Completed index generation reconciliation outcomes",
		}, []string{"outcome"}),
		GenerationRetentions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "generation", Name: "retentions_total",
			Help: "Completed retired index generation cleanup outcomes",
		}, []string{"outcome"}),
		GenerationRollbacks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Subsystem: "generation", Name: "rollbacks_total",
			Help: "Completed index generation rollback outcomes",
		}, []string{"outcome"}),
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
		LLMRequests: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "llm",
				Name:      "requests_total",
				Help:      "Total LLM requests by model and outcome",
			},
			[]string{"model", "outcome"},
		),
		LLMRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Subsystem: "llm",
				Name:      "request_duration_seconds",
				Help:      "LLM request duration by model and outcome",
				Buckets:   []float64{0.1, 0.5, 1, 2, 5, 10, 20, 30, 60},
			},
			[]string{"model", "outcome"},
		),
		LLMConsecutiveFailures: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Subsystem: "llm",
				Name:      "consecutive_failures",
				Help:      "Consecutive completed LLM request failures per process",
			},
			[]string{"model"},
		),
		LLMTokens: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "llm",
				Name:      "tokens_total",
				Help:      "LLM tokens consumed by model and kind (prompt/completion)",
			},
			[]string{"model", "kind"},
		),
		LLMCostUSD: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Subsystem: "llm",
				Name:      "cost_usd_total",
				Help:      "Estimated LLM spend in USD by model; zero when prices unset",
			},
			[]string{"model"},
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
		m.ESDeadLetter,
		m.IngestionOutboxPending,
		m.IngestionOutboxRetried,
		m.IngestionOutboxOldestAge,
		m.IngestionJobs,
		m.IngestionExpiredLeases,
		m.GenerationManifests,
		m.GenerationOldestAge,
		m.GenerationDiagnostics,
		m.GenerationReconciliations,
		m.GenerationRetentions,
		m.GenerationRollbacks,
		m.QueryDuration,
		m.QueryFailures,
		m.RetrievalCount,
		m.LLMRequests,
		m.LLMRequestDuration,
		m.LLMConsecutiveFailures,
		m.LLMTokens,
		m.LLMCostUSD,
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

// SetIngestionOperations publishes one bounded-cardinality snapshot of durable
// admission and worker state. Callers should refresh it from PostgreSQL.
func (m *Metrics) SetIngestionOperations(pending, retried int, oldestAge time.Duration, jobs map[string]int, expiredLeases int) {
	m.IngestionOutboxPending.Set(float64(pending))
	m.IngestionOutboxRetried.Set(float64(retried))
	m.IngestionOutboxOldestAge.Set(nonNegativeDuration(oldestAge).Seconds())
	for _, status := range []string{"queued", "published", "processing", "completed", "failed"} {
		m.IngestionJobs.WithLabelValues(status).Set(float64(jobs[status]))
	}
	m.IngestionExpiredLeases.Set(float64(expiredLeases))
}

// SetGenerationOperations replaces the current durable generation health
// snapshot. Only fixed state and condition labels are emitted.
func (m *Metrics) SetGenerationOperations(snapshot indexmanifest.OperationsSnapshot) {
	for _, state := range []indexmanifest.ManifestState{
		indexmanifest.StateBuilding, indexmanifest.StateReady, indexmanifest.StateFailed,
		indexmanifest.StateActive, indexmanifest.StateRetired,
	} {
		m.GenerationManifests.WithLabelValues(string(state)).Set(float64(snapshot.Manifests[state]))
		m.GenerationOldestAge.WithLabelValues(string(state)).Set(nonNegativeDuration(snapshot.OldestAge[state]).Seconds())
	}
	m.GenerationDiagnostics.WithLabelValues("backend_diverged").Set(float64(snapshot.BackendDiverged))
	m.GenerationDiagnostics.WithLabelValues("repair_exhausted").Set(float64(snapshot.RepairExhausted))
	m.GenerationDiagnostics.WithLabelValues("retention_failed").Set(float64(snapshot.RetentionFailed))
}

func (m *Metrics) ObserveReconciliation(report indexmanifest.ReconciliationReport, err error) {
	if err != nil {
		m.GenerationReconciliations.WithLabelValues("pass_failed").Inc()
		return
	}
	for outcome, count := range map[string]int{
		"healthy": report.Healthy, "diverged": report.Diverged,
		"repair_scheduled": report.RepairScheduled, "repair_pending": report.RepairPending,
		"repair_exhausted": report.RepairExhausted, "conflicted": report.Conflicted,
	} {
		m.GenerationReconciliations.WithLabelValues(outcome).Add(float64(count))
	}
}

func (m *Metrics) ObserveRetention(report indexmanifest.RetentionReport, err error) {
	if err != nil {
		m.GenerationRetentions.WithLabelValues("pass_failed").Inc()
		return
	}
	for outcome, count := range map[string]int{
		"deleted": report.Deleted, "failed": report.Failed, "conflicted": report.Conflicted,
	} {
		m.GenerationRetentions.WithLabelValues(outcome).Add(float64(count))
	}
}

func (m *Metrics) ObserveRollback(outcome indexmanifest.RollbackOutcome) {
	switch outcome {
	case indexmanifest.RollbackSucceeded, indexmanifest.RollbackConflict,
		indexmanifest.RollbackNotReady, indexmanifest.RollbackFailed:
		m.GenerationRollbacks.WithLabelValues(string(outcome)).Inc()
	default:
		m.GenerationRollbacks.WithLabelValues("failed").Inc()
	}
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

// InitializeLLMModel exposes a zero-value consecutive failure gauge for a configured model.
func (m *Metrics) InitializeLLMModel(model string) {
	model = normalizedLabel(model, "unknown")

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.llmConsecutiveFailures[model]; !exists {
		m.llmConsecutiveFailures[model] = 0
	}
	m.LLMConsecutiveFailures.WithLabelValues(model).Set(float64(m.llmConsecutiveFailures[model]))
}

// RecordLLMRequest records one completed LLM request and its process-local failure streak.
func (m *Metrics) RecordLLMRequest(model, outcome string, duration time.Duration) {
	model = normalizedLabel(model, "unknown")
	outcome = normalizedLabel(outcome, "unknown")
	duration = nonNegativeDuration(duration)

	m.LLMRequests.WithLabelValues(model, outcome).Inc()
	m.LLMRequestDuration.WithLabelValues(model, outcome).Observe(duration.Seconds())

	m.mu.Lock()
	defer m.mu.Unlock()
	if outcome == "success" {
		m.llmConsecutiveFailures[model] = 0
	} else {
		m.llmConsecutiveFailures[model]++
	}
	m.LLMConsecutiveFailures.WithLabelValues(model).Set(float64(m.llmConsecutiveFailures[model]))
}

// RecordLLMTokens records prompt/completion token consumption and estimated cost.
// Cost uses configured per-1k prices (LLM_PRICE_PROMPT_PER_1K /
// LLM_PRICE_COMPLETION_PER_1K, USD); both zero means cost stays zero.
func (m *Metrics) RecordLLMTokens(model string, promptTokens, completionTokens int64) {
	model = normalizedLabel(model, "unknown")
	if promptTokens > 0 {
		m.LLMTokens.WithLabelValues(model, "prompt").Add(float64(promptTokens))
	}
	if completionTokens > 0 {
		m.LLMTokens.WithLabelValues(model, "completion").Add(float64(completionTokens))
	}
	if promptTokens > 0 || completionTokens > 0 {
		cost := float64(promptTokens)/1000*llmPromptPricePer1K +
			float64(completionTokens)/1000*llmCompletionPricePer1K
		if cost > 0 {
			m.LLMCostUSD.WithLabelValues(model).Add(cost)
		}
	}
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

// Unwrap lets http.ResponseController reach the underlying ResponseWriter, so
// SSE handlers can still obtain an http.Flusher through this middleware.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
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
