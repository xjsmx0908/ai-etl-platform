package query

import (
	"strings"

	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/retrieval"
)

func sourceContextsFromCandidates(candidates []retrieval.Candidate, documents map[string]docstore.Governance) []SourceContext {
	sources := make([]SourceContext, 0, len(candidates))
	for _, c := range candidates {
		source := SourceContext{
			ChunkID:         c.ChunkID,
			DocID:           c.DocID,
			Content:         c.Content,
			Score:           c.Score,
			TenantID:        c.TenantID,
			KnowledgeBase:   c.Metadata["knowledge_base_id"],
			ApplicableScope: c.Metadata["applicable_scope"],
		}
		if document, ok := documents[c.DocID]; ok {
			source.FileName = document.FileName
			source.EffectiveDate = effectiveDateString(document)
		}
		sources = append(sources, source)
	}
	return sources
}

func citationsFromAnswer(answer string, sources []SourceContext) []SourceContext {
	marker := strings.LastIndex(answer, "来源:")
	if unicodeMarker := strings.LastIndex(answer, "来源："); unicodeMarker > marker {
		marker = unicodeMarker
	}
	if marker < 0 {
		return []SourceContext{}
	}
	citationText := answer[marker:]
	seenDocs := make(map[string]struct{})
	citations := make([]SourceContext, 0)
	for _, source := range sources {
		if source.DocID == "" || !strings.Contains(citationText, source.DocID) {
			continue
		}
		if _, exists := seenDocs[source.DocID]; exists {
			continue
		}
		seenDocs[source.DocID] = struct{}{}
		citations = append(citations, source)
	}
	return citations
}
