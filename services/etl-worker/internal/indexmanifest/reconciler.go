package indexmanifest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ReconciliationClaim struct {
	Limit int
	Lease time.Duration
	Token string
}

type ReconciliationResult struct {
	Manifest       Manifest
	Qdrant         BackendObservation
	Elasticsearch  BackendObservation
	Healthy        bool
	Reason         string
	ReconcileAfter time.Duration
}

type RepairDisposition string

const (
	RepairNotNeeded RepairDisposition = "not_needed"
	RepairScheduled RepairDisposition = "scheduled"
	RepairPending   RepairDisposition = "pending"
	RepairExhausted RepairDisposition = "exhausted"
)

type ReconciliationStore interface {
	ClaimReconciliation(context.Context, ReconciliationClaim) ([]Manifest, error)
	FinishReconciliation(context.Context, ReconciliationResult, int) (RepairDisposition, error)
}

type ReconcilerOptions struct {
	BatchSize  int
	Interval   time.Duration
	Lease      time.Duration
	MaxRepairs int
}

type ReconciliationReport struct {
	Checked         int
	Healthy         int
	Diverged        int
	RepairScheduled int
	RepairPending   int
	RepairExhausted int
	Conflicted      int
}

// Reconciler observes active projections and delegates durable, bounded replay
// scheduling to its store. It never reconstructs chunks from manifest hashes.
type Reconciler struct {
	store         ReconciliationStore
	qdrant        Projection
	elasticsearch Projection
	options       ReconcilerOptions
}

func NewReconciler(store ReconciliationStore, qdrant, elasticsearch Projection, options ReconcilerOptions) *Reconciler {
	return &Reconciler{store: store, qdrant: qdrant, elasticsearch: elasticsearch, options: options}
}

func (r *Reconciler) RunOnce(ctx context.Context) (ReconciliationReport, error) {
	if r.store == nil || r.qdrant == nil || r.elasticsearch == nil {
		return ReconciliationReport{}, ErrInvalidManifest
	}
	claim := ReconciliationClaim{Limit: r.options.BatchSize, Lease: r.options.Lease, Token: newReconciliationToken()}
	manifests, err := r.store.ClaimReconciliation(ctx, claim)
	if err != nil {
		return ReconciliationReport{}, fmt.Errorf("claim manifest reconciliation: %w", err)
	}
	report := ReconciliationReport{Checked: len(manifests)}
	for _, manifest := range manifests {
		identity := GenerationIdentity{GenerationID: manifest.GenerationID, VersionIdentity: VersionIdentity{
			TenantID: manifest.TenantID, DocumentID: manifest.DocumentID, DocumentVersionID: manifest.DocumentVersionID,
		}}
		result := ReconciliationResult{Manifest: manifest, ReconcileAfter: r.options.Interval}
		reasons := make([]string, 0, 2)
		var observationErr error
		result.Qdrant, observationErr = r.qdrant.ObserveGeneration(ctx, identity)
		if observationErr != nil {
			reasons = append(reasons, "observe qdrant: "+observationErr.Error())
		}
		result.Elasticsearch, observationErr = r.elasticsearch.ObserveGeneration(ctx, identity)
		if observationErr != nil {
			reasons = append(reasons, "observe elasticsearch: "+observationErr.Error())
		}
		result.Reason = strings.Join(reasons, "; ")
		result.Healthy = result.Reason == "" && observationMatches(manifest, result.Qdrant) && observationMatches(manifest, result.Elasticsearch)
		if !result.Healthy && result.Reason == "" {
			result.Reason = "projection identity mismatch"
		}
		disposition, finishErr := r.store.FinishReconciliation(ctx, result, r.options.MaxRepairs)
		if finishErr != nil {
			if errors.Is(finishErr, ErrConflict) {
				report.Conflicted++
				continue
			}
			return report, fmt.Errorf("finish manifest reconciliation: %w", finishErr)
		}
		if result.Healthy {
			report.Healthy++
		} else {
			report.Diverged++
		}
		switch disposition {
		case RepairScheduled:
			report.RepairScheduled++
		case RepairPending:
			report.RepairPending++
		case RepairExhausted:
			report.RepairExhausted++
		}
	}
	return report, nil
}

// Run performs an immediate pass, then reconciles periodically until shutdown.
// A failed pass is logged and retried on the next interval.
func (r *Reconciler) Run(ctx context.Context) {
	interval := r.options.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		report, err := r.RunOnce(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("index manifest reconciliation failed", "error", err)
			}
		} else if report.Checked > 0 {
			slog.Info("index manifest reconciliation completed",
				"checked", report.Checked, "healthy", report.Healthy,
				"diverged", report.Diverged, "repair_scheduled", report.RepairScheduled,
				"repair_pending", report.RepairPending, "repair_exhausted", report.RepairExhausted,
				"conflicted", report.Conflicted)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func newReconciliationToken() string {
	return "reconcile-" + uuid.NewString()
}

func observationMatches(manifest Manifest, observation BackendObservation) bool {
	return observation.Count == manifest.ExpectedChunkCount && observation.Digest == manifest.ExpectedChunkDigest
}
