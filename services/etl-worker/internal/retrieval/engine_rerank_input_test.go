package retrieval

import (
	"context"
	"fmt"
	"testing"
	"time"

	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/sparse"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// A Cross-Encoder Reranker costs O(candidates handed to it) but returns at most
// rerankTopK. Those are two numbers, and before RETRIEVAL_RERANK_INPUT_K existed
// only one of them was expressible: a 50-candidate pool cost 50 forward passes
// to produce 20 results (measured live: 21.07s for one query). These tests keep
// the two numbers apart, so a later refactor cannot quietly fold the work budget
// back into the output size.
//
// The cost of a cap is real, not theoretical: measured on the demo stack with no
// cap, the candidates that became context reached fused rank 34 of 50, so the
// tail is scored and sometimes promoted rather than scored and thrown away. That
// is why the shipped value is 0 and why the knob is measured rather than picked.

type rerankInputRecorder struct {
	got     []Candidate
	gotTopK int
}

func (r *rerankInputRecorder) Rerank(_ context.Context, _ string, candidates []Candidate, topK int) ([]Candidate, error) {
	r.got = append([]Candidate(nil), candidates...)
	r.gotTopK = topK
	ranked := append([]Candidate(nil), candidates...)
	return topCandidates(ranked, topK), nil
}

// rerankInputPool builds a pool whose members are distinguishable by ChunkID and
// that carries no lexical overlap with the probe question, so the assertions are
// about how many candidates travel and not about how stabilizeRanking reorders.
func rerankInputPool(n int) []Candidate {
	pool := make([]Candidate, 0, n)
	for i := 0; i < n; i++ {
		pool = append(pool, Candidate{
			ChunkID: fmt.Sprintf("chunk-%03d", i),
			DocID:   fmt.Sprintf("doc-%03d", i),
			Content: fmt.Sprintf("内容 %03d", i),
			Rank:    i + 1,
		})
	}
	return pool
}

func newRerankInputEngine(t *testing.T, inputK, poolSize int) (*Engine, *rerankInputRecorder) {
	t.Helper()
	embedServer := newPolicyEvalEmbedServer(t)
	t.Cleanup(embedServer.Close)

	recorder := &rerankInputRecorder{}
	engine := &Engine{
		cfg: config.Config{
			EmbedEndpoint:         embedServer.URL,
			EmbedModel:            "rerank-input-embed",
			RetrievalEnableRerank: true,
			// always: the cap must be observable regardless of route heuristics.
			RetrievalRerankPolicy: config.RerankPolicyAlways,
			RerankEndpoint:        "http://reranker.local/rerank",
			RetrievalRerankInputK: inputK,
		},
		sparseEncoder: sparse.NewEncoder(sparse.DefaultParams()),
		httpClient:    embedServer.Client(),
		retrievers: map[string]Retriever{
			SourceQdrant: staticPolicyEvalRetriever{name: SourceQdrant, candidates: rerankInputPool(poolSize)},
		},
		cache:       NoopCache{},
		reranker:    recorder,
		timeout:     time.Second,
		candidateK:  poolSize,
		defaultTopK: 5,
	}
	return engine, recorder
}

func runRerankInputProbe(t *testing.T, engine *Engine) Result {
	t.Helper()
	result, err := engine.Retrieve(context.Background(), Request{
		Question:           "查询候选池规模与算力",
		TopK:               5,
		TenantID:           "tenant-rerank-input",
		AllowedPermissions: []string{"public"},
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	return result
}

func TestRerankScoresTheWholePoolWhenInputKIsUnset(t *testing.T) {
	engine, recorder := newRerankInputEngine(t, 0, 50)
	runRerankInputProbe(t, engine)

	if len(recorder.got) != 50 {
		t.Fatalf("expected the uncapped default to hand over all 50 candidates, got %d", len(recorder.got))
	}
	// The output cap is a separate decision and must not follow the input.
	if recorder.gotTopK != defaultRerankTopK {
		t.Fatalf("expected top_k to stay at %d, got %d", defaultRerankTopK, recorder.gotTopK)
	}
}

func TestRerankInputKBoundsTheWorkNotTheOutput(t *testing.T) {
	capped, cappedRecorder := newRerankInputEngine(t, 12, 50)
	uncapped, uncappedRecorder := newRerankInputEngine(t, 0, 50)
	cappedResult := runRerankInputProbe(t, capped)
	runRerankInputProbe(t, uncapped)

	if len(uncappedRecorder.got) != 50 {
		t.Fatalf("expected the uncapped run to hand over 50 candidates, got %d", len(uncappedRecorder.got))
	}
	if len(cappedRecorder.got) != 12 {
		t.Fatalf("expected the cap to hand over 12 candidates, got %d", len(cappedRecorder.got))
	}
	// "Taken from the head of the stabilized order" means the capped input is a
	// prefix of the uncapped one. Anything else would drop candidates by some
	// other rule than "fusion ranked them lower".
	for i, candidate := range cappedRecorder.got {
		if candidate.ChunkID != uncappedRecorder.got[i].ChunkID {
			t.Fatalf("capped input diverges from the head of the pool at %d: %s vs %s",
				i, candidate.ChunkID, uncappedRecorder.got[i].ChunkID)
		}
	}
	// Both runs must still return the same number of results: capping the input
	// is meant to change which candidates were considered, not how many survive.
	if len(cappedResult.Sources) == 0 {
		t.Fatal("expected the capped run to still return sources")
	}
}

func TestRerankInputKAboveThePoolIsANoop(t *testing.T) {
	engine, recorder := newRerankInputEngine(t, 500, 50)
	runRerankInputProbe(t, engine)

	if len(recorder.got) != 50 {
		t.Fatalf("expected a cap above the pool size to be a no-op, got %d candidates", len(recorder.got))
	}
	if recorder.gotTopK != defaultRerankTopK {
		t.Fatalf("expected top_k %d, got %d", defaultRerankTopK, recorder.gotTopK)
	}
}

// TestRerankInputKIsWhatDropsATailCandidate pins the cost of the knob, so nobody
// reads it as a free optimisation. With the cap off the reranker sees the whole
// pool and can promote a candidate that fusion ranked last; with the cap on it
// never sees it. That trade is the reason the shipped value is chosen from
// measurement (retrieval.deepest_selected_input_rank, which reports how deep the
// candidates that become context reach) rather than picked.
func TestRerankInputKIsWhatDropsATailCandidate(t *testing.T) {
	// Score by ChunkID, promoting whatever candidate the caller marks. The
	// promoted one is whichever sits deepest in the stabilized order, so the
	// test does not depend on where stabilizeRanking puts a given ChunkID.
	uncappedEngine, uncapped := newRerankInputEngine(t, 0, 50)
	runRerankInputProbe(t, uncappedEngine)
	deepest := uncapped.got[len(uncapped.got)-1].ChunkID

	uncappedEngine.reranker = &promotingByChunkID{target: deepest}
	result := runRerankInputProbe(t, uncappedEngine)
	if len(result.Sources) == 0 || result.Sources[0].ChunkID != deepest {
		t.Fatalf("expected the promoted tail candidate %q to lead the uncapped result, got %q",
			deepest, firstChunkID(result))
	}

	// Same pool, same promoted candidate, cap set below its position: it can no
	// longer be promoted, and that is the documented cost.
	cappedEngine, _ := newRerankInputEngine(t, 12, 50)
	cappedEngine.reranker = &promotingByChunkID{target: deepest}
	cappedResult := runRerankInputProbe(t, cappedEngine)
	if firstChunkID(cappedResult) == deepest {
		t.Fatalf("expected the cap to make %q unreachable, but it still led the result", deepest)
	}
}

// promotingByChunkID moves one named candidate to the head before truncating, so
// the promotion is visible in the engine's output. It does its own truncation:
// wrapping a helper that already truncated would hide the tail candidate before
// it could be promoted, which is exactly what this test needs to observe.
type promotingByChunkID struct {
	target  string
	got     []Candidate
	gotTopK int
}

func (r *promotingByChunkID) Rerank(_ context.Context, _ string, candidates []Candidate, topK int) ([]Candidate, error) {
	r.got = append([]Candidate(nil), candidates...)
	r.gotTopK = topK
	ranked := append([]Candidate(nil), candidates...)
	for i := range ranked {
		if ranked[i].ChunkID != r.target {
			continue
		}
		head := ranked[i]
		copy(ranked[1:i+1], ranked[:i])
		ranked[0] = head
		break
	}
	return topCandidates(ranked, topK), nil
}

func TestDeepestInputRankReportsHowDeepTheRerankerReached(t *testing.T) {
	pool := rerankInputPool(50)

	// The reranker returned the head and one candidate from position 30: the
	// deepest input position used is 30, which is what sizes the knob.
	selected := []Candidate{pool[0], pool[30]}
	if got := deepestInputRank(selected, pool); got != 30 {
		t.Fatalf("expected deepest input rank 30, got %d", got)
	}
	if got := deepestInputRank(nil, pool); got != -1 {
		t.Fatalf("expected -1 for an empty selection, got %d", got)
	}
	if got := deepestInputRank(selected, nil); got != -1 {
		t.Fatalf("expected -1 for an empty pool, got %d", got)
	}
	// A candidate that is not in the pool must not be reported as rank 0, which
	// would read as "the reranker only ever reached the head".
	foreign := []Candidate{{ChunkID: "not-in-pool"}}
	if got := deepestInputRank(foreign, pool); got != -1 {
		t.Fatalf("expected -1 when nothing maps, got %d", got)
	}
	// Empty ChunkIDs are not identities and must not be matched to each other.
	anonymous := []Candidate{{ChunkID: ""}}
	anonymousPool := []Candidate{{ChunkID: ""}, {ChunkID: ""}}
	if got := deepestInputRank(anonymous, anonymousPool); got != -1 {
		t.Fatalf("expected -1 for identity-less candidates, got %d", got)
	}
}

// withSpanRecorder installs a recording tracer on the engine so the depth
// attributes can be read back. Both of them are emitted as span attributes, so
// without a recorder the test could only assert that the numbers were computed,
// not that anything ever published them.
func withSpanRecorder(t *testing.T, engine *Engine) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})
	engine.tracer = provider.Tracer("rerank-input-test")
	return recorder
}

// spanIntAttribute returns the last value recorded for an int attribute on the
// named span. The boolean reports presence, because an attribute that is absent
// must not be read as zero: 0 is a legitimate depth ("the head of the pool").
func spanIntAttribute(t *testing.T, recorder *tracetest.SpanRecorder, spanName, key string) (int, bool) {
	t.Helper()
	value, present := 0, false
	for _, span := range recorder.Ended() {
		if span.Name() != spanName {
			continue
		}
		for _, attr := range span.Attributes() {
			if string(attr.Key) != key {
				continue
			}
			value, present = int(attr.Value.AsInt64()), true
		}
	}
	return value, present
}

// reorderingReranker returns a chosen arrangement of the candidates it was
// handed, addressed by position in its own input. Expressing the arrangement
// that way, rather than by ChunkID, keeps the test independent of how
// stabilizeRanking orders the pool.
type reorderingReranker struct {
	order   []int
	got     []Candidate
	gotTopK int
}

func (r *reorderingReranker) Rerank(_ context.Context, _ string, candidates []Candidate, topK int) ([]Candidate, error) {
	r.got = append([]Candidate(nil), candidates...)
	r.gotTopK = topK
	ranked := make([]Candidate, 0, len(candidates))
	seen := make([]bool, len(candidates))
	for _, i := range r.order {
		if i < 0 || i >= len(candidates) || seen[i] {
			continue
		}
		seen[i] = true
		ranked = append(ranked, candidates[i])
	}
	for i := range candidates {
		if !seen[i] {
			ranked = append(ranked, candidates[i])
		}
	}
	return topCandidates(ranked, topK), nil
}

// TestSelectedInputRankIsNotTheRerankerOutputRank keeps the two depth signals
// apart. They are easy to conflate -- both are "how deep did we go" -- but they
// size different things: the reranker's reach says what a cap would withhold
// from scoring, the selected depth says whether the withheld candidates were
// going to be used. Sizing RETRIEVAL_RERANK_INPUT_K from the first number alone
// is what produced the wrong conclusion that the pool tail is wasted work.
func TestSelectedInputRankIsNotTheRerankerOutputRank(t *testing.T) {
	engine, _ := newRerankInputEngine(t, 0, 50)
	recorder := withSpanRecorder(t, engine)
	// The reranker hands back the first five of its input plus one candidate from
	// position 40. Its output therefore reaches depth 40, while the five that
	// survive into the context reach only depth 4.
	engine.reranker = &reorderingReranker{order: []int{0, 1, 2, 3, 4, 40}}
	result := runRerankInputProbe(t, engine)

	used, ok := spanIntAttribute(t, recorder, "Retrieval.Rerank", "rerank.deepest_used_input_rank")
	if !ok {
		t.Fatal("expected rerank.deepest_used_input_rank on the rerank span")
	}
	if used != 40 {
		t.Fatalf("expected the reranker's reach to be 40, got %d", used)
	}
	if len(result.Sources) != 5 {
		t.Fatalf("expected 5 context candidates, got %d", len(result.Sources))
	}
	selected, ok := spanIntAttribute(t, recorder, "RetrievalEngine.Retrieve", "retrieval.deepest_selected_input_rank")
	if !ok {
		t.Fatal("expected retrieval.deepest_selected_input_rank on the retrieval span")
	}
	if selected != 4 {
		t.Fatalf("expected the selected depth to be 4, got %d", selected)
	}
	if selected == used {
		t.Fatal("the two depth signals collapsed into one number; they must be independent")
	}
}

// TestSelectedInputRankFollowsTheRerankerOrder is the converse case: when the
// reranker does promote a deep candidate into the context, the selected depth
// must report it. Otherwise a cap could be set from a number that never moves.
func TestSelectedInputRankFollowsTheRerankerOrder(t *testing.T) {
	engine, _ := newRerankInputEngine(t, 0, 50)
	recorder := withSpanRecorder(t, engine)
	engine.reranker = &reorderingReranker{order: []int{40, 0, 1, 2, 3}}
	result := runRerankInputProbe(t, engine)

	if len(result.Sources) == 0 {
		t.Fatal("expected sources")
	}
	deep := engine.reranker.(*reorderingReranker).got[40].ChunkID
	if result.Sources[0].ChunkID != deep {
		t.Fatalf("expected %q to lead the context, got %q", deep, result.Sources[0].ChunkID)
	}
	selected, ok := spanIntAttribute(t, recorder, "RetrievalEngine.Retrieve", "retrieval.deepest_selected_input_rank")
	if !ok {
		t.Fatal("expected retrieval.deepest_selected_input_rank on the retrieval span")
	}
	if selected != 40 {
		t.Fatalf("expected the selected depth to report the promoted candidate at 40, got %d", selected)
	}
}

// TestSelectedInputRankIsAbsentWhenTheRerankerFails guards the attribute's
// meaning. After a reranker failure `ranked` falls back to the fusion order, in
// which the deepest candidate is trivially the last one; publishing that as the
// selected depth would report a saturated number that looks like evidence the
// cap is unsafe.
func TestSelectedInputRankIsAbsentWhenTheRerankerFails(t *testing.T) {
	engine, _ := newRerankInputEngine(t, 0, 50)
	recorder := withSpanRecorder(t, engine)
	engine.reranker = &failingPolicyEvalReranker{}
	runRerankInputProbe(t, engine)

	if _, ok := spanIntAttribute(t, recorder, "RetrievalEngine.Retrieve", "retrieval.deepest_selected_input_rank"); ok {
		t.Fatal("expected no selected depth when the reranker did not produce the ordering")
	}
}
