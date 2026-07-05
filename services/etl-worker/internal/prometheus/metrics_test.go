package prometheus

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-etl-pipeline/internal/agent"

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
