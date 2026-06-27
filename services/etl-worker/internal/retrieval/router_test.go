package retrieval

import "testing"

func TestRouteQuery(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		want    Strategy
		wantES  bool
		qWeight float64
		eWeight float64
	}{
		{
			name:    "structured id routes to exact keyword",
			query:   "订单 A20240518001 的退款状态",
			want:    StrategyExactKeyword,
			wantES:  true,
			qWeight: 0.25,
			eWeight: 0.75,
		},
		{
			name:    "semantic question routes to semantic",
			query:   "公司的报销制度是什么",
			want:    StrategySemantic,
			wantES:  true,
			qWeight: 0.80,
			eWeight: 0.20,
		},
		{
			name:    "fallback routes to hybrid",
			query:   "alpha beta policy",
			want:    StrategyHybrid,
			wantES:  true,
			qWeight: 0.55,
			eWeight: 0.45,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RouteQuery(tc.query, true)
			if got.Strategy != tc.want {
				t.Fatalf("expected strategy %s, got %s", tc.want, got.Strategy)
			}
			if got.UseElastic != tc.wantES {
				t.Fatalf("expected UseElastic=%v, got %v", tc.wantES, got.UseElastic)
			}
			if got.QdrantWeight != tc.qWeight || got.ElasticWeight != tc.eWeight {
				t.Fatalf("unexpected weights: qdrant=%v elastic=%v", got.QdrantWeight, got.ElasticWeight)
			}
		})
	}
}

func TestRouteQuery_DisablesElastic(t *testing.T) {
	got := RouteQuery("订单 A20240518001 的退款状态", false)
	if got.UseElastic {
		t.Fatal("expected elastic disabled")
	}
	if !got.UseQdrant || got.QdrantWeight != 1 {
		t.Fatalf("unexpected qdrant route: %+v", got)
	}
}
