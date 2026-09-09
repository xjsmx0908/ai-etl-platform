package query

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"ai-etl-pipeline/internal/docstore"
)

func (s *Service) titleMatchDocIDs(ctx context.Context, tenantID, spaceID, question string, permissions []string) []string {
	if s == nil || s.documents == nil {
		return nil
	}
	spaceID = strings.TrimSpace(spaceID)
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" || spaceID == "" || strings.TrimSpace(question) == "" {
		return nil
	}
	docs, _, err := s.documents.List(ctx, docstore.ListQuery{
		TenantID:          tenantID,
		Status:            docstore.StatusCompleted,
		Permissions:       permissions,
		KnowledgeSpaceIDs: []string{spaceID},
		Limit:             500,
	})
	if err != nil {
		slog.Warn("title match document lookup failed", "tenant_id", tenantID, "space_id", spaceID, "error", err)
		return nil
	}
	return titleMatchedDocIDs(question, docs)
}

func titleMatchedDocIDs(question string, docs []docstore.Document) []string {
	q := normalizeTitleText(question)
	if q == "" {
		return nil
	}
	seen := make(map[string]struct{})
	ids := make([]string, 0, 2)
	for _, doc := range docs {
		if strings.TrimSpace(doc.PublicationStatus) != "" && doc.PublicationStatus != "published" {
			continue
		}
		if doc.DeletionStatus == "pending" {
			continue
		}
		stem := fileNameStem(doc.FileName)
		if utf8.RuneCountInString(stem) < 2 || !strings.Contains(q, stem) {
			continue
		}
		if _, ok := seen[doc.DocID]; ok {
			continue
		}
		seen[doc.DocID] = struct{}{}
		ids = append(ids, doc.DocID)
	}
	return ids
}

func fileNameStem(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	base := filepath.Base(name)
	ext := filepath.Ext(base)
	if ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	return normalizeTitleText(base)
}

func normalizeTitleText(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
