package retrieval

import (
	"sort"
	"strings"
)

const rrfK = 60.0

// Fuse deduplicates candidates by chunk_id and combines backend rankings with
// weighted reciprocal rank fusion. Rank-based scoring avoids mixing Qdrant and
// Elasticsearch raw-score scales.
func Fuse(results map[string][]Candidate, route Route, limit int) []Candidate {
	if limit <= 0 {
		limit = 50
	}

	type fusedCandidate struct {
		Candidate
		sourceSet map[string]struct{}
	}

	byChunk := make(map[string]*fusedCandidate)
	for source, candidates := range results {
		weight := route.weightFor(source)
		if weight <= 0 {
			continue
		}
		for i, candidate := range candidates {
			key := candidate.GenerationID + "\x00" + candidate.ChunkID
			if key == "\x00" {
				key = candidate.Source + ":" + candidate.DocID + ":" + candidate.Content
			}
			if key == "::" {
				continue
			}

			rank := candidate.Rank
			if rank <= 0 {
				rank = i + 1
			}
			fusionScore := weight / (rrfK + float64(rank))

			current, ok := byChunk[key]
			if !ok {
				c := candidate
				// Capture the backend's raw score before RRF overwrites it.
				if c.Relevance == 0 {
					c.Relevance = candidate.Score
					c.RelevanceSource = source
				}
				c.Score = fusionScore
				c.Rank = rank
				if c.Source == "" {
					c.Source = source
				}
				byChunk[key] = &fusedCandidate{
					Candidate: c,
					sourceSet: map[string]struct{}{source: {}},
				}
				continue
			}

			current.Score += fusionScore
			// Prefer Qdrant's cosine score as the relevance signal: it is a
			// bounded similarity measure, while BM25 is unbounded and corpus-
			// dependent, so the two cannot share a threshold.
			if source == SourceQdrant && current.RelevanceSource != SourceQdrant {
				current.Relevance = candidate.Relevance
				if candidate.RelevanceSource != SourceQdrant {
					current.Relevance = candidate.Score
				}
				current.RelevanceSource = source
			}
			current.sourceSet[source] = struct{}{}
			if current.Content == "" {
				current.Content = candidate.Content
			}
			if current.DocID == "" {
				current.DocID = candidate.DocID
			}
			if current.TenantID == "" {
				current.TenantID = candidate.TenantID
			}
			current.Metadata = mergeCandidateMetadata(current.Metadata, candidate.Metadata)
		}
	}

	fused := make([]Candidate, 0, len(byChunk))
	for _, item := range byChunk {
		sources := make([]string, 0, len(item.sourceSet))
		for source := range item.sourceSet {
			sources = append(sources, source)
		}
		sort.Strings(sources)
		item.Source = strings.Join(sources, ",")
		fused = append(fused, item.Candidate)
	}

	sort.SliceStable(fused, func(i, j int) bool {
		if fused[i].Score == fused[j].Score {
			return fused[i].ChunkID < fused[j].ChunkID
		}
		return fused[i].Score > fused[j].Score
	})

	if len(fused) > limit {
		fused = fused[:limit]
	}
	for i := range fused {
		fused[i].Rank = i + 1
	}
	return fused
}

func (r Route) weightFor(source string) float64 {
	switch source {
	case SourceQdrant:
		return r.QdrantWeight
	case SourceElasticsearch:
		return r.ElasticWeight
	default:
		return 0
	}
}
