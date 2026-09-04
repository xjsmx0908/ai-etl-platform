package prometheus

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-etl-pipeline/internal/agent"
	"ai-etl-pipeline/internal/deletionworkflow"
	"ai-etl-pipeline/internal/indexmanifest"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestHTTPMiddlewareRecordsStatusAndNormalizedPath(t *testing.T) {
	m := New("test_ai_etl")
	handler := m.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "teapot", http.StatusTeapot)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/query?debug=true", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTeapot {
		t.Fatalf("expected status %d, got %d", http.StatusTeapot, rr.Code)
	}

	got := histogramCount(t, m.HTTPRequestDuration.WithLabelValues(http.MethodPost, "/v1/query", "418").(prometheus.Metric))
	if got != 1 {
		t.Fatalf("expected request count 1, got %v", got)
	}
	inFlight := gaugeValue(t, m.HTTPRequestInFlight.WithLabelValues("/v1/query"))
	if inFlight != 0 {
		t.Fatalf("expected in-flight gauge to return to 0, got %v", inFlight)
	}
}

func TestHTTPMiddlewareUsesDedicatedReleaseCenterRouteLabels(t *testing.T) {
	for _, test := range []struct {
		path string
		want string
	}{
		{path: "/v1/release-center/overview", want: "/v1/release-center/overview"},
		{path: "/v1/release-center/requests/request-1", want: "/v1/release-center/requests"},
		{path: "/v1/release-center/reviews/doc-1", want: "/v1/release-center"},
	} {
		if got := routeLabel(test.path); got != test.want {
			t.Fatalf("routeLabel(%q)=%q want %q", test.path, got, test.want)
		}
	}
}

func TestSCIMMiddlewareRecordsBoundedOutcomeAndDuration(t *testing.T) {
	m := New("test_ai_etl_scim_duration")
	handler := m.SCIMMiddleware("workforce", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPatch, "/scim/v2/Users/id", nil))

	if got := counterValue(t, m.SCIMRequests.WithLabelValues("workforce", "patch", "success")); got != 1 {
		t.Fatalf("SCIM requests=%v want 1", got)
	}
	if got := histogramCount(t, m.SCIMRequestDuration.WithLabelValues("workforce", "patch", "success").(prometheus.Metric)); got != 1 {
		t.Fatalf("SCIM duration samples=%v want 1", got)
	}
}

func TestSCIMReadDoesNotHideStaleProvisioning(t *testing.T) {
	m := New("test_ai_etl_scim_stale")
	handler := m.SCIMMiddleware("workforce", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/scim/v2/Users?filter=x", nil))
	if got := gaugeValue(t, m.SCIMLastSuccess.WithLabelValues("workforce")); got != 0 {
		t.Fatalf("successful read changed last provisioning success to %v", got)
	}
	if got := gaugeValue(t, m.SCIMEnabledSince.WithLabelValues("workforce")); got <= 0 {
		t.Fatalf("SCIM enabled timestamp=%v want positive", got)
	}
}

func TestSetCircuitStateRecordsGauge(t *testing.T) {
	m := New("test_ai_etl_circuit")

	m.SetCircuitState("llm-api", 1)

	got := gaugeValue(t, m.CircuitState.WithLabelValues("llm-api"))
	if got != 1 {
		t.Fatalf("expected circuit state 1, got %v", got)
	}
}

func TestAgentMetricsRecordLifecycleEvents(t *testing.T) {
	m := New("test_ai_etl_agent")

	m.RecordAgentRunStarted(true)
	m.RecordAgentRunFinished(agent.StateFailed, "run_timeout_exceeded", 2*time.Second)
	m.RecordAgentToolStep("rag_query", agent.StateCompleted, 25*time.Millisecond)
	m.RecordAgentApprovalDecision(agent.ApprovalRejected, "publish_report")

	if got := counterValue(t, m.AgentRunsStarted.WithLabelValues("true")); got != 1 {
		t.Fatalf("expected one started run, got %v", got)
	}
	if got := counterValue(t, m.AgentRunCompletions.WithLabelValues(string(agent.StateFailed), "run_timeout_exceeded")); got != 1 {
		t.Fatalf("expected one failed run completion, got %v", got)
	}
	if got := histogramCount(t, m.AgentRunDuration.WithLabelValues(string(agent.StateFailed), "run_timeout_exceeded").(prometheus.Metric)); got != 1 {
		t.Fatalf("expected one run duration sample, got %v", got)
	}
	if got := counterValue(t, m.AgentToolSteps.WithLabelValues("rag_query", string(agent.StateCompleted))); got != 1 {
		t.Fatalf("expected one completed tool step, got %v", got)
	}
	if got := histogramCount(t, m.AgentToolStepDuration.WithLabelValues("rag_query", string(agent.StateCompleted)).(prometheus.Metric)); got != 1 {
		t.Fatalf("expected one tool step duration sample, got %v", got)
	}
	if got := counterValue(t, m.AgentApprovalDecisions.WithLabelValues(string(agent.ApprovalRejected), "publish_report")); got != 1 {
		t.Fatalf("expected one rejected approval decision, got %v", got)
	}
}

func TestLLMMetricsRecordOutcomeAndConsecutiveFailures(t *testing.T) {
	m := New("test_ai_etl_llm")
	m.InitializeLLMModel("test-model")

	if got := gaugeValue(t, m.LLMConsecutiveFailures.WithLabelValues("test-model")); got != 0 {
		t.Fatalf("expected initialized consecutive failure gauge 0, got %v", got)
	}

	m.RecordLLMRequest("test-model", "server_error", 250*time.Millisecond)
	m.RecordLLMRequest("test-model", "server_error", 500*time.Millisecond)

	if got := counterValue(t, m.LLMRequests.WithLabelValues("test-model", "server_error")); got != 2 {
		t.Fatalf("expected two failed LLM requests, got %v", got)
	}
	if got := histogramCount(t, m.LLMRequestDuration.WithLabelValues("test-model", "server_error").(prometheus.Metric)); got != 2 {
		t.Fatalf("expected two LLM duration samples, got %v", got)
	}
	if got := gaugeValue(t, m.LLMConsecutiveFailures.WithLabelValues("test-model")); got != 2 {
		t.Fatalf("expected consecutive failure gauge 2, got %v", got)
	}

	m.RecordLLMRequest("test-model", "success", 100*time.Millisecond)

	if got := counterValue(t, m.LLMRequests.WithLabelValues("test-model", "success")); got != 1 {
		t.Fatalf("expected one successful LLM request, got %v", got)
	}
	if got := gaugeValue(t, m.LLMConsecutiveFailures.WithLabelValues("test-model")); got != 0 {
		t.Fatalf("expected success to reset consecutive failures, got %v", got)
	}
}

func TestLLMTokenMetricsRecordUsageAndCost(t *testing.T) {
	// Prices are read from env at package init; control them for this test.
	// Zero prices keep cost at zero.
	m := New("test_ai_etl_tokens")

	m.RecordLLMTokens("tok-model", 1000, 2000)

	if got := counterValue(t, m.LLMTokens.WithLabelValues("tok-model", "prompt")); got != 1000 {
		t.Fatalf("expected 1000 prompt tokens, got %v", got)
	}
	if got := counterValue(t, m.LLMTokens.WithLabelValues("tok-model", "completion")); got != 2000 {
		t.Fatalf("expected 2000 completion tokens, got %v", got)
	}
	// Cost stays zero without configured prices.
	if got := counterValue(t, m.LLMCostUSD.WithLabelValues("tok-model")); got != 0 {
		t.Fatalf("expected zero cost without prices, got %v", got)
	}
}

func TestLLMTokenMetricsSkipZeroUsage(t *testing.T) {
	m := New("test_ai_etl_tokens_zero")

	m.RecordLLMTokens("tok-zero", 0, 0)

	if got := counterValue(t, m.LLMTokens.WithLabelValues("tok-zero", "prompt")); got != 0 {
		t.Fatalf("expected no prompt metric for zero usage, got %v", got)
	}
}

func TestHandlerForUsesProvidedGatherer(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New("test_ai_etl_handler")
	reg.MustRegister(m.HTTPRequestDuration)

	handler := m.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))

	rr := httptest.NewRecorder()
	m.HandlerFor(reg).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected metrics status 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `test_ai_etl_handler_http_request_duration_seconds_count{method="GET",path="/healthz",status="204"} 1`) {
		t.Fatalf("expected handler to expose registered request metric, got:\n%s", rr.Body.String())
	}
}

func TestSetIngestionOperationsExposesDurableBacklogSnapshot(t *testing.T) {
	m := New("test_ai_etl_ingestion")

	m.SetIngestionOperations(7, 3, 95*time.Second, map[string]int{
		"queued": 2, "published": 1, "processing": 4, "completed": 9, "failed": 2,
	}, 1)

	if got := gaugeValue(t, m.IngestionOutboxPending); got != 7 {
		t.Fatalf("pending outbox = %v, want 7", got)
	}
	if got := gaugeValue(t, m.IngestionOutboxRetried); got != 3 {
		t.Fatalf("retried outbox = %v, want 3", got)
	}
	if got := gaugeValue(t, m.IngestionOutboxOldestAge); got != 95 {
		t.Fatalf("oldest outbox age = %v, want 95", got)
	}
	if got := gaugeValue(t, m.IngestionJobs.WithLabelValues("processing")); got != 4 {
		t.Fatalf("processing jobs = %v, want 4", got)
	}
	if got := gaugeValue(t, m.IngestionExpiredLeases); got != 1 {
		t.Fatalf("expired leases = %v, want 1", got)
	}
}

func TestGenerationMetricsExposeOnlyBoundedLifecycleLabels(t *testing.T) {
	m := New("test_ai_etl_generation")
	m.SetGenerationOperations(indexmanifest.OperationsSnapshot{
		Manifests: map[indexmanifest.ManifestState]int{
			indexmanifest.StateBuilding: 2,
			indexmanifest.StateActive:   7,
			indexmanifest.StateFailed:   3,
		},
		OldestAge: map[indexmanifest.ManifestState]time.Duration{
			indexmanifest.StateBuilding: 95 * time.Second,
		},
		BackendDiverged: 4,
		RepairExhausted: 1,
		RetentionFailed: 2,
	})
	m.ObserveReconciliation(indexmanifest.ReconciliationReport{
		Healthy: 5, RepairScheduled: 2, RepairPending: 1, RepairExhausted: 1, Conflicted: 3,
	}, nil)
	m.ObserveRetention(indexmanifest.RetentionReport{Deleted: 4, Failed: 2, Conflicted: 1}, nil)
	m.ObserveRollback(indexmanifest.RollbackSucceeded)

	if got := gaugeValue(t, m.GenerationManifests.WithLabelValues("failed")); got != 3 {
		t.Fatalf("failed manifests = %v, want 3", got)
	}
	if got := gaugeValue(t, m.GenerationOldestAge.WithLabelValues("building")); got != 95 {
		t.Fatalf("oldest building age = %v, want 95", got)
	}
	if got := gaugeValue(t, m.GenerationDiagnostics.WithLabelValues("backend_diverged")); got != 4 {
		t.Fatalf("backend divergence = %v, want 4", got)
	}
	if got := counterValue(t, m.GenerationReconciliations.WithLabelValues("repair_scheduled")); got != 2 {
		t.Fatalf("scheduled repairs = %v, want 2", got)
	}
	if got := counterValue(t, m.GenerationRetentions.WithLabelValues("failed")); got != 2 {
		t.Fatalf("retention failures = %v, want 2", got)
	}
	if got := counterValue(t, m.GenerationRollbacks.WithLabelValues("success")); got != 1 {
		t.Fatalf("successful rollbacks = %v, want 1", got)
	}
}

func TestDeletionMetricsExposeOnlyBoundedStateAndOutcomeLabels(t *testing.T) {
	m := New("test_ai_etl_deletion")
	m.SetDeletionOperations(deletionworkflow.OperationsSnapshot{
		Pending: 3, Processing: 2, Failed: 1, ExpiredLeases: 1, OldestAge: 95 * time.Second,
	})
	m.ObserveDeletion(deletionworkflow.Report{Completed: 4, Failed: 2, Conflicted: 1}, nil)
	if got := gaugeValue(t, m.DeletionJobs.WithLabelValues("pending")); got != 3 {
		t.Fatalf("pending=%v", got)
	}
	if got := gaugeValue(t, m.DeletionDiagnostics.WithLabelValues("failed")); got != 1 {
		t.Fatalf("failed=%v", got)
	}
	if got := gaugeValue(t, m.DeletionOldestAge); got != 95 {
		t.Fatalf("oldest=%v", got)
	}
	if got := counterValue(t, m.DeletionOutcomes.WithLabelValues("completed")); got != 4 {
		t.Fatalf("completed=%v", got)
	}
}

func histogramCount(t *testing.T, metric prometheus.Metric) uint64 {
	t.Helper()

	var out dto.Metric
	if err := metric.Write(&out); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	if out.Histogram == nil {
		t.Fatalf("expected histogram metric")
	}
	return out.Histogram.GetSampleCount()
}

func gaugeValue(t *testing.T, metric prometheus.Metric) float64 {
	t.Helper()

	var out dto.Metric
	if err := metric.Write(&out); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	if out.Gauge == nil {
		t.Fatalf("expected gauge metric")
	}
	return out.Gauge.GetValue()
}

func counterValue(t *testing.T, metric prometheus.Metric) float64 {
	t.Helper()

	var out dto.Metric
	if err := metric.Write(&out); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	if out.Counter == nil {
		t.Fatalf("expected counter metric")
	}
	return out.Counter.GetValue()
}
