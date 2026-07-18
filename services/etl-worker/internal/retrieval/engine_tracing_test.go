package retrieval

import (
	"context"
	"testing"

	"ai-etl-pipeline/internal/config"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestEngineEmitsStageSpans(t *testing.T) {
	embedServer := newPolicyEvalEmbedServer(t)
	defer embedServer.Close()

	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	retrievers := map[string]Retriever{
		SourceQdrant: staticPolicyEvalRetriever{
			name: SourceQdrant,
			candidates: []Candidate{
				{ChunkID: "distractor", DocID: "doc-faq", Content: "Travel reimbursement examples.", Rank: 1},
				{ChunkID: "target", DocID: "doc-expense", Content: "公司报销制度要求提供发票。", Rank: 2},
			},
		},
	}
	reranker := &scoringPolicyEvalReranker{scores: map[string]float64{"target": 0.99, "distractor": 0.1}}
	engine := newPolicyEvalEngine(embedServer.URL, embedServer.Client(), retrievers, reranker, true, config.RerankPolicyAuto)
	engine.tracer = provider.Tracer("retrieval-test")

	_, err := engine.Retrieve(context.Background(), Request{
		Question:           "公司的报销制度是什么",
		TopK:               1,
		TenantID:           "tenant-tracing-test",
		AllowedPermissions: []string{"public"},
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}

	spanNames := make(map[string]bool)
	for _, span := range recorder.Ended() {
		spanNames[span.Name()] = true
	}
	for _, expected := range []string{
		"RetrievalEngine.Retrieve",
		"Retrieval.EmbedQuery",
		"Retrieval.CacheLookup",
		"Retrieval.Route",
		"Retrieval.BackendSearch",
		"Retrieval.Fusion",
		"Retrieval.Rerank",
		"Retrieval.CacheStore",
	} {
		if !spanNames[expected] {
			t.Fatalf("expected span %q, got %+v", expected, spanNames)
		}
	}
}
