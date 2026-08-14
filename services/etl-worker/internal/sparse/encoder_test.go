package sparse

import (
	"testing"
)

func TestEncode_BasicEnglish(t *testing.T) {
	enc := NewEncoder(DefaultParams())
	sv := enc.Encode("machine learning is a subset of artificial intelligence")

	if sv.IsEmpty() {
		t.Fatal("expected non-empty sparse vector")
	}
	if len(sv.Indices) != len(sv.Values) {
		t.Fatalf("indices/values length mismatch: %d vs %d", len(sv.Indices), len(sv.Values))
	}

	// All values should be positive (BM25 scores)
	for i, v := range sv.Values {
		if v <= 0 {
			t.Errorf("value at index %d should be positive, got %f", i, v)
		}
	}

	// All indices should be within MaxDim
	for i, idx := range sv.Indices {
		if idx >= DefaultParams().MaxDim {
			t.Errorf("index %d out of range: %d >= %d", i, idx, DefaultParams().MaxDim)
		}
	}
}

func TestEncode_EmptyInput(t *testing.T) {
	enc := NewEncoder(DefaultParams())
	sv := enc.Encode("")

	if !sv.IsEmpty() {
		t.Errorf("expected empty sparse vector for empty input, got %d entries", len(sv.Indices))
	}
}

func TestEncode_StopWordsFiltered(t *testing.T) {
	enc := NewEncoder(DefaultParams())

	// "the is a an" are all stop words, should produce empty vector
	sv := enc.Encode("the is a an")
	if !sv.IsEmpty() {
		t.Errorf("expected empty vector for stop-words-only input, got %d entries", len(sv.Indices))
	}
}

func TestEncode_ChineseText(t *testing.T) {
	enc := NewEncoder(DefaultParams())
	sv := enc.Encode("机器学习是人工智能的子集")

	if sv.IsEmpty() {
		t.Fatal("expected non-empty sparse vector for Chinese text")
	}

	// Chinese characters are treated as individual tokens
	if len(sv.Indices) == 0 {
		t.Error("expected indices for Chinese characters")
	}
}

func TestEncode_RepeatedTermsHigherScore(t *testing.T) {
	enc := NewEncoder(DefaultParams())

	single := enc.Encode("database")
	repeated := enc.Encode("database database database")

	if single.IsEmpty() || repeated.IsEmpty() {
		t.Fatal("both vectors should be non-empty")
	}

	// Repeated term should have a higher BM25 score (due to higher TF)
	if repeated.Values[0] <= single.Values[0] {
		t.Errorf("repeated term should have higher score: %f <= %f",
			repeated.Values[0], single.Values[0])
	}
}

func TestEncode_DifferentTextsProduceDifferentVectors(t *testing.T) {
	enc := NewEncoder(DefaultParams())

	v1 := enc.Encode("kubernetes container orchestration")
	v2 := enc.Encode("machine learning neural network")

	if v1.IsEmpty() || v2.IsEmpty() {
		t.Fatal("both vectors should be non-empty")
	}

	// Different texts should produce different index sets
	same := 0
	m := make(map[uint32]bool)
	for _, idx := range v1.Indices {
		m[idx] = true
	}
	for _, idx := range v2.Indices {
		if m[idx] {
			same++
		}
	}

	// Allow some hash collisions but most should be different
	if same == len(v1.Indices) && same == len(v2.Indices) {
		t.Error("completely different texts should produce different vectors")
	}
}

// Distinct terms that hash to the same sparse index (e.g. "0x5d" and "100"
// both map to index 12 under FNV-32a mod MaxDim) must not produce duplicate
// indices — Qdrant rejects those with HTTP 422 "indices: must be unique".
func TestEncode_HashCollisionMergesIndices(t *testing.T) {
	enc := NewEncoder(DefaultParams())
	sv := enc.Encode("0x5d and 100 are different terms")

	if sv.IsEmpty() {
		t.Fatal("expected non-empty sparse vector")
	}

	seen := make(map[uint32]bool, len(sv.Indices))
	for _, idx := range sv.Indices {
		if seen[idx] {
			t.Fatalf("duplicate sparse index %d emitted for colliding terms", idx)
		}
		seen[idx] = true
	}

	// Sorted indices make vectors deterministic.
	for i := 1; i < len(sv.Indices); i++ {
		if sv.Indices[i] <= sv.Indices[i-1] {
			t.Fatalf("indices not strictly sorted: %v", sv.Indices)
		}
	}
}

func TestL2Norm(t *testing.T) {
	enc := NewEncoder(DefaultParams())
	sv := enc.Encode("vector normalization test example")

	normed := L2Norm(sv)
	if normed.IsEmpty() {
		t.Fatal("normalized vector should not be empty")
	}

	// Check L2 norm ≈ 1.0
	var sumSq float64
	for _, v := range normed.Values {
		sumSq += float64(v) * float64(v)
	}
	if sumSq < 0.99 || sumSq > 1.01 {
		t.Errorf("L2 norm should be ~1.0, got %f", sumSq)
	}
}

func BenchmarkEncode(b *testing.B) {
	enc := NewEncoder(DefaultParams())
	text := "Machine learning is a method of data analysis that automates analytical model building. It is a branch of artificial intelligence based on the idea that systems can learn from data, identify patterns and make decisions."

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		enc.Encode(text)
	}
}
