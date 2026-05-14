// Package sparse implements BM25-based sparse vector encoding for hybrid search.
package sparse

import (
	"hash/fnv"
	"math"
	"strings"
	"unicode"

	"ai-etl-pipeline/internal/model"
)

// Params holds BM25 algorithm parameters.
type Params struct {
	K1     float64 // Term frequency saturation, default 1.2
	B      float64 // Document length normalization, default 0.75
	AvgDL  float64 // Average document length in tokens, default 256
	MinTF  int     // Minimum term frequency filter, default 1
	MaxDim uint32  // Maximum sparse dimension space (hash modulus), default 30000
}

// DefaultParams returns production-grade BM25 defaults.
func DefaultParams() Params {
	return Params{
		K1:     1.2,
		B:      0.75,
		AvgDL:  256,
		MinTF:  1,
		MaxDim: 30000,
	}
}

// Encoder generates BM25 sparse vectors from text.
type Encoder struct {
	params    Params
	stopWords map[string]struct{}
}

// NewEncoder creates a sparse vector encoder with the given BM25 parameters.
func NewEncoder(params Params) *Encoder {
	return &Encoder{
		params:    params,
		stopWords: defaultStopWords(),
	}
}

// Encode converts text into a BM25 sparse vector.
func (e *Encoder) Encode(text string) model.SparseVector {
	tokens := e.tokenize(text)
	if len(tokens) == 0 {
		return model.SparseVector{}
	}

	// Count term frequencies
	tf := make(map[string]int, len(tokens)/2)
	for _, t := range tokens {
		tf[t]++
	}

	// Calculate BM25 scores and map to sparse dimensions
	dl := float64(len(tokens))
	indices := make([]uint32, 0, len(tf))
	values := make([]float32, 0, len(tf))

	for term, freq := range tf {
		if freq < e.params.MinTF {
			continue
		}

		// Simplified BM25: score = tf * (k1 + 1) / (tf + k1 * (1 - b + b * dl/avgdl))
		ftf := float64(freq)
		numerator := ftf * (e.params.K1 + 1)
		denominator := ftf + e.params.K1*(1-e.params.B+e.params.B*dl/e.params.AvgDL)
		score := numerator / denominator

		if score <= 0 {
			continue
		}

		idx := e.termToIndex(term)
		indices = append(indices, idx)
		values = append(values, float32(score))
	}

	return model.SparseVector{
		Indices: indices,
		Values:  values,
	}
}

// L2Norm returns a L2-normalized copy of the sparse vector.
func L2Norm(sv model.SparseVector) model.SparseVector {
	if sv.IsEmpty() {
		return sv
	}

	var sumSq float64
	for _, v := range sv.Values {
		sumSq += float64(v) * float64(v)
	}
	norm := math.Sqrt(sumSq)
	if norm == 0 {
		return sv
	}

	normed := model.SparseVector{
		Indices: sv.Indices,
		Values:  make([]float32, len(sv.Values)),
	}
	for i, v := range sv.Values {
		normed.Values[i] = float32(float64(v) / norm)
	}
	return normed
}

// tokenize segments text into tokens supporting mixed Chinese/English.
// Chinese characters are treated as individual tokens (unigram).
// English words shorter than 2 chars and stop words are filtered out.
func (e *Encoder) tokenize(text string) []string {
	text = strings.ToLower(text)
	tokens := make([]string, 0, 64)
	var current strings.Builder

	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			current.WriteRune(r)
		} else {
			if current.Len() > 0 {
				token := current.String()
				if !e.isStopWord(token) && len(token) >= 2 {
					tokens = append(tokens, token)
				}
				current.Reset()
			}
			if unicode.Is(unicode.Han, r) {
				tokens = append(tokens, string(r))
			}
		}
	}

	if current.Len() > 0 {
		token := current.String()
		if !e.isStopWord(token) && len(token) >= 2 {
			tokens = append(tokens, token)
		}
	}

	return tokens
}

// termToIndex maps a term to a sparse vector index via FNV hash.
func (e *Encoder) termToIndex(term string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(term))
	return h.Sum32() % e.params.MaxDim
}

func (e *Encoder) isStopWord(word string) bool {
	_, ok := e.stopWords[word]
	return ok
}

func defaultStopWords() map[string]struct{} {
	words := []string{
		"the", "is", "at", "which", "on", "a", "an", "and", "or", "but",
		"in", "with", "to", "for", "of", "not", "no", "it", "this", "that",
		"are", "was", "were", "be", "been", "being", "have", "has", "had",
		"do", "does", "did", "will", "would", "could", "should", "may",
		"might", "shall", "can", "need", "dare", "ought", "used", "from",
		"by", "as", "into", "through", "during", "before", "after", "above",
		"below", "between", "out", "off", "over", "under", "again", "further",
		"then", "once", "here", "there", "when", "where", "why", "how", "all",
		"each", "every", "both", "few", "more", "most", "other", "some", "such",
		"than", "too", "very", "just", "because", "so", "if", "about", "up",
	}
	m := make(map[string]struct{}, len(words))
	for _, w := range words {
		m[w] = struct{}{}
	}
	return m
}
