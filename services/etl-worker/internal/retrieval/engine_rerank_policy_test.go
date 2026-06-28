package retrieval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/sparse"
)

func TestEngineRerankPolicy_UsesRerankerWhenSemanticRankingImproves(t *testing.T) {
	embedServer := newPolicyEvalEmbedServer(t)
	defer embedServer.Close()

	retrievers := map[string]Retriever{
		SourceQdrant: staticPolicyEvalRetriever{
			name: SourceQdrant,
			candidates: []Candidate{
				{ChunkID: "semantic-distractor", DocID: "doc-faq", Content: "Travel policy mentions receipts and reimbursement examples.", Rank: 1},
				{ChunkID: "semantic-target", DocID: "doc-expense", Content: "公司报销制度规定员工提交发票后由财务审批。", Rank: 2},
			},
		},
	}
	req := Request{
		Question:           "公司的报销制度是什么",
		TopK:               1,
		TenantID:           "tenant-policy-eval",
		AllowedPermissions: []string{"public"},
	}

	offEngine := newPolicyEvalEngine(embedServer.URL, embedServer.Client(), retrievers, NoopReranker{}, false, config.RerankPolicyAuto)
	offResult, err := offEngine.Retrieve(context.Background(), req)
	if err != nil {
		t.Fatalf("retrieve without rerank: %v", err)
	}
	if offResult.Route.Strategy != StrategySemantic {
		t.Fatalf("expected semantic route, got %+v", offResult.Route)
	}
	if got := firstChunkID(offResult); got != "semantic-distractor" {
		t.Fatalf("expected fused ranking to keep distractor first without rerank, got %q", got)
	}

	reranker := &scoringPolicyEvalReranker{
		scores: map[string]float64{
			"semantic-target":     0.99,
			"semantic-distractor": 0.10,
		},
	}
	autoEngine := newPolicyEvalEngine(embedServer.URL, embedServer.Client(), retrievers, reranker, true, config.RerankPolicyAuto)
	autoResult, err := autoEngine.Retrieve(context.Background(), req)
	if err != nil {
		t.Fatalf("retrieve with auto rerank: %v", err)
	}
	if got := firstChunkID(autoResult); got != "semantic-target" {
		t.Fatalf("expected semantic rerank to promote target, got %q", got)
	}
	if reranker.calls != 1 {
		t.Fatalf("expected reranker called once for semantic query, got %d", reranker.calls)
	}
}

func TestEngineRerankPolicy_SkipsRerankerWhenExactFusionIsBetter(t *testing.T) {
	embedServer := newPolicyEvalEmbedServer(t)
	defer embedServer.Close()

	retrievers := map[string]Retriever{
		SourceQdrant: staticPolicyEvalRetriever{
			name: SourceQdrant,
			candidates: []Candidate{
				{ChunkID: "exact-distractor", DocID: "doc-general-refund", Content: "通用退款政策说明。", Rank: 1},
				{ChunkID: "exact-target", DocID: "doc-order-a20240601001", Content: "订单 A20240601001 的退款状态为已完成。", Rank: 2},
			},
		},
		SourceElasticsearch: staticPolicyEvalRetriever{
			name: SourceElasticsearch,
			candidates: []Candidate{
				{ChunkID: "exact-target", DocID: "doc-order-a20240601001", Content: "订单 A20240601001 的退款状态为已完成。", Rank: 1},
				{ChunkID: "exact-distractor", DocID: "doc-general-refund", Content: "通用退款政策说明。", Rank: 2},
			},
		},
	}
	req := Request{
		Question:           "订单 A20240601001 的退款状态",
		TopK:               1,
		TenantID:           "tenant-policy-eval",
		AllowedPermissions: []string{"public"},
	}

	autoReranker := &scoringPolicyEvalReranker{
		scores: map[string]float64{
			"exact-distractor": 0.99,
			"exact-target":     0.10,
		},
	}
	autoEngine := newPolicyEvalEngine(embedServer.URL, embedServer.Client(), retrievers, autoReranker, true, config.RerankPolicyAuto)
	autoResult, err := autoEngine.Retrieve(context.Background(), req)
	if err != nil {
		t.Fatalf("retrieve with auto rerank: %v", err)
	}
	if autoResult.Route.Strategy != StrategyExactKeyword {
		t.Fatalf("expected exact route, got %+v", autoResult.Route)
	}
	if got := firstChunkID(autoResult); got != "exact-target" {
		t.Fatalf("expected auto policy to keep exact fusion target first, got %q", got)
	}
	if autoReranker.calls != 0 {
		t.Fatalf("expected reranker skipped for exact query, got %d calls", autoReranker.calls)
	}

	alwaysReranker := &scoringPolicyEvalReranker{
		scores: map[string]float64{
			"exact-distractor": 0.99,
			"exact-target":     0.10,
		},
	}
	alwaysEngine := newPolicyEvalEngine(embedServer.URL, embedServer.Client(), retrievers, alwaysReranker, true, config.RerankPolicyAlways)
	alwaysResult, err := alwaysEngine.Retrieve(context.Background(), req)
	if err != nil {
		t.Fatalf("retrieve with always rerank: %v", err)
	}
	if got := firstChunkID(alwaysResult); got != "exact-distractor" {
		t.Fatalf("expected forced rerank to demonstrate exact-query regression, got %q", got)
	}
	if alwaysReranker.calls != 1 {
		t.Fatalf("expected reranker called once for always policy, got %d", alwaysReranker.calls)
	}
}

type staticPolicyEvalRetriever struct {
	name       string
	candidates []Candidate
}

func (r staticPolicyEvalRetriever) Name() string {
	return r.name
}

func (r staticPolicyEvalRetriever) Search(context.Context, SearchRequest) ([]Candidate, error) {
	out := append([]Candidate(nil), r.candidates...)
	for i := range out {
		if out[i].Source == "" {
			out[i].Source = r.name
		}
	}
	return out, nil
}

type scoringPolicyEvalReranker struct {
	scores map[string]float64
	calls  int
}

func (r *scoringPolicyEvalReranker) Rerank(_ context.Context, _ string, candidates []Candidate, topK int) ([]Candidate, error) {
	r.calls++
	ranked := append([]Candidate(nil), candidates...)
	for i := range ranked {
		ranked[i].Score = r.scores[ranked[i].ChunkID]
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].Score > ranked[j].Score
	})
	return topCandidates(ranked, topK), nil
}

func newPolicyEvalEngine(embedEndpoint string, client *http.Client, retrievers map[string]Retriever, reranker Reranker, enableRerank bool, policy string) *Engine {
	endpoint := ""
	if enableRerank {
		endpoint = "http://reranker.local/rerank"
	}
	return &Engine{
		cfg: config.Config{
			EmbedEndpoint:         embedEndpoint,
			EmbedModel:            "policy-eval-embed",
			RetrievalEnableRerank: enableRerank,
			RetrievalRerankPolicy: policy,
			RerankEndpoint:        endpoint,
		},
		sparseEncoder: sparse.NewEncoder(sparse.DefaultParams()),
		httpClient:    client,
		retrievers:    retrievers,
		cache:         NoopCache{},
		reranker:      reranker,
		timeout:       time.Second,
		candidateK:    10,
		defaultTopK:   1,
	}
}

func newPolicyEvalEmbedServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data": []map[string]interface{}{
				{"embedding": []float64{0.1, 0.2, 0.3}},
			},
		})
	}))
}

func firstChunkID(result Result) string {
	if len(result.Sources) == 0 {
		return ""
	}
	return result.Sources[0].ChunkID
}
