package main

import (
	"context"
	"log/slog"

	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/store"
)

// reconcileDocuments backfills the document registry from Qdrant: any document
// that exists as vectors but has no registry row gets one (status=completed).
// It never deletes rows missing from Qdrant, so an outage or empty collection
// cannot destroy the registry. Opt-in via RECONCILE_DOCS_ON_STARTUP.
func reconcileDocuments(ctx context.Context, cfg config.Config, docs docstore.Store) {
	qs, err := store.NewQdrantStorer(cfg.StoreEndpoint, cfg.StoreAPIKey, cfg.StoreCollection, cfg.EmbedDimension)
	if err != nil {
		slog.Warn("document reconciliation skipped: qdrant unavailable", "error", err)
		return
	}
	defer qs.Close()

	refs, err := qs.ListDocs(ctx, 0, 100)
	if err != nil {
		slog.Warn("document reconciliation failed: cannot scroll qdrant", "error", err)
		return
	}
	added := 0
	for _, ref := range refs {
		if err := docs.ReconcileUpsert(ctx, ref.TenantID, ref.DocID, ref.Permission); err != nil {
			slog.Warn("document reconciliation upsert failed", "tenant_id", ref.TenantID, "doc_id", ref.DocID, "error", err)
			continue
		}
		added++
	}
	slog.Info("document registry reconciliation complete",
		"qdrant_documents", len(refs), "registry_rows", added)
}
