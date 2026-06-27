package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// NoopReranker keeps the fused order and exists so the rerank stage can be
// wired before an external cross-encoder service is deployed.
type NoopReranker struct{}

func (NoopReranker) Rerank(_ context.Context, _ string, candidates []Candidate, topK int) ([]Candidate, error) {
	return topCandidates(candidates, topK), nil
}

// HTTPReranker calls a cross-encoder rerank service with an OpenAI/Cohere-like
// JSON contract: {query, documents} -> {results:[{index,relevance_score}]}.
type HTTPReranker struct {
	endpoint string
	apiKey   string
	model    string
	client   *http.Client
}

func NewHTTPReranker(endpoint, apiKey, model string, client *http.Client) *HTTPReranker {
	return &HTTPReranker{
		endpoint: strings.TrimSpace(endpoint),
		apiKey:   strings.TrimSpace(apiKey),
		model:    strings.TrimSpace(model),
		client:   client,
	}
}

func (r *HTTPReranker) Rerank(ctx context.Context, query string, candidates []Candidate, topK int) ([]Candidate, error) {
	if r.endpoint == "" {
		return topCandidates(candidates, topK), nil
	}
	if len(candidates) == 0 {
		return []Candidate{}, nil
	}

	documents := make([]string, 0, len(candidates))
	for _, c := range candidates {
		documents = append(documents, c.Content)
	}

	body := map[string]interface{}{
		"model":     r.model,
		"query":     query,
		"documents": documents,
		"top_n":     topK,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal rerank request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create rerank request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("rerank error %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var decoded struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
			Score          float64 `json:"score"`
		} `json:"results"`
		Scores []float64 `json:"scores"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode rerank response: %w", err)
	}

	ranked := make([]Candidate, 0, len(candidates))
	if len(decoded.Results) > 0 {
		for _, item := range decoded.Results {
			if item.Index < 0 || item.Index >= len(candidates) {
				continue
			}
			c := candidates[item.Index]
			if item.RelevanceScore != 0 {
				c.Score = item.RelevanceScore
			} else {
				c.Score = item.Score
			}
			ranked = append(ranked, c)
		}
	} else if len(decoded.Scores) == len(candidates) {
		for i, score := range decoded.Scores {
			c := candidates[i]
			c.Score = score
			ranked = append(ranked, c)
		}
	} else {
		return nil, fmt.Errorf("rerank response did not include usable scores")
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].Score > ranked[j].Score
	})
	return topCandidates(ranked, topK), nil
}

func topCandidates(candidates []Candidate, topK int) []Candidate {
	if topK <= 0 || topK > len(candidates) {
		topK = len(candidates)
	}
	out := append([]Candidate(nil), candidates[:topK]...)
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}
