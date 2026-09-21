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
	ClaimFailedRepairs(context.Context, ReconciliationClaim, int) ([]Manifest, error)
	ScheduleFailedRepair(context.Context, Manifest) error
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
	// RepairReplayed counts failed generations whose durable ingestion path was
	// reopened for another build attempt.
	RepairReplayed int
	// RepairUnavailable counts failed generations that have no ingestion job to
	// replay. Their repair budget is consumed without a rebuild, so they reach
	// repair_exhausted and escalate instead of being re-claimed forever.
	RepairUnavailable int
}

type ReconciliationObserver interface {
	ObserveReconciliation(ReconciliationReport, error)
}

// Reconciler observes active projections and delegates durable, bounded replay
// scheduling to its store. It never reconstructs chunks from manifest hashes.
type Reconciler struct {
	store         ReconciliationStore
	qdrant        Projection
	elasticsearch Projection
	options       ReconcilerOptions
	observer      ReconciliationObserver
}

func (r *Reconciler) WithObserver(observer ReconciliationObserver) *Reconciler {
	r.observer = observer
	return r
}

func NewReconciler(store ReconciliationStore, qdrant, elasticsearch Projection, options ReconcilerOptions) *Reconciler {
	return &Reconciler{store: store, qdrant: qdrant, elasticsearch: elasticsearch, options: options}
}

// RunOnce performs one pass over both kinds of durable generation damage.
//
// Active generations are observed across backends and divergences are replayed
// through their ingestion outbox. Failed generations are a different decision:
// there is no projection to observe and nothing to compare, only a build that
// never finished. They are claimed separately and replayed through the same
// durable path, bounded by the same repair budget, so a build that cannot
// succeed escalates instead of sitting dead forever.
func (r *Reconciler) RunOnce(ctx context.Context) (ReconciliationReport, error) {
	var report ReconciliationReport
	var runErr error
	defer func() {
		if r.observer != nil {
			r.observer.ObserveReconciliation(report, runErr)
		}
	}()
	if r.store == nil || r.qdrant == nil || r.elasticsearch == nil {
		runErr = ErrInvalidManifest
		return report, runErr
	}
	active, activeErr := r.reconcileActive(ctx)
	replayed, replayErr := r.replayFailed(ctx)
	report = mergeReconciliationReports(active, replayed)
	runErr = errors.Join(activeErr, replayErr)
	return report, runErr
}

func mergeReconciliationReports(a, b ReconciliationReport) ReconciliationReport {
	return ReconciliationReport{
		Checked:           a.Checked + b.Checked,
		Healthy:           a.Healthy + b.Healthy,
		Diverged:          a.Diverged + b.Diverged,
		RepairScheduled:   a.RepairScheduled + b.RepairScheduled,
		RepairPending:     a.RepairPending + b.RepairPending,
		RepairExhausted:   a.RepairExhausted + b.RepairExhausted,
		Conflicted:        a.Conflicted + b.Conflicted,
		RepairReplayed:    a.RepairReplayed + b.RepairReplayed,
		RepairUnavailable: a.RepairUnavailable + b.RepairUnavailable,
	}
}

func (r *Reconciler) reconcileActive(ctx context.Context) (ReconciliationReport, error) {
	var report ReconciliationReport
	claim := ReconciliationClaim{Limit: r.options.BatchSize, Lease: r.options.Lease, Token: newReconciliationToken()}
	manifests, err := r.store.ClaimReconciliation(ctx, claim)
	if err != nil {
		return report, fmt.Errorf("claim manifest reconciliation: %w", err)
	}
	report.Checked = len(manifests)
	// One manifest whose durable bookkeeping cannot be written must not stop the
	// rest of the batch. Every claimed manifest is already leased, so returning
	// here would leave all the later ones unobserved until their lease expires:
	// a single unrepairable row would silently reduce the whole reconciliation
	// pass to whatever happened to be claimed before it. Collect the failures and
	// keep draining; the caller still receives them.
	var finishFailures []error
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
			finishFailures = append(finishFailures, fmt.Errorf("finish manifest reconciliation %s: %w", manifest.GenerationID, finishErr))
			continue
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
	return report, errors.Join(finishFailures...)
}

// replayFailed reopens the durable ingestion path for generations whose build
// failed. Nothing else does: a failed generation is not active, so the active
// pass never claims it, and the outbox row that carried its one delivery is
// already published, so the relay never picks it up again. Left alone it stays
// failed for good while every health signal reports the platform as healthy.
func (r *Reconciler) replayFailed(ctx context.Context) (ReconciliationReport, error) {
	var report ReconciliationReport
	claim := ReconciliationClaim{Limit: r.options.BatchSize, Lease: r.options.Lease, Token: newReconciliationToken()}
	manifests, err := r.store.ClaimFailedRepairs(ctx, claim, r.maxRepairs())
	if err != nil {
		return report, fmt.Errorf("claim failed generation repairs: %w", err)
	}
	report.Checked = len(manifests)
	var failures []error
	for _, manifest := range manifests {
		scheduleErr := r.store.ScheduleFailedRepair(ctx, manifest)
		if scheduleErr == nil {
			report.RepairReplayed++
			continue
		}
		if errors.Is(scheduleErr, ErrConflict) {
			report.Conflicted++
			continue
		}
		if errors.Is(scheduleErr, ErrNoReplayPath) {
			report.RepairUnavailable++
			continue
		}
		if errors.Is(scheduleErr, ErrRepairInFlight) {
			report.RepairPending++
			continue
		}
		failures = append(failures, fmt.Errorf("schedule failed generation repair %s: %w", manifest.GenerationID, scheduleErr))
	}
	return report, errors.Join(failures...)
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
				"repair_replayed", report.RepairReplayed, "repair_unavailable", report.RepairUnavailable,
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

// maxRepairs applies the same default the store uses, so an unconfigured
// reconciler still bounds its replay budget instead of asking the store to
// claim with a budget of zero.
func (r *Reconciler) maxRepairs() int {
	if r.options.MaxRepairs <= 0 {
		return 3
	}
	return r.options.MaxRepairs
}

func observationMatches(manifest Manifest, observation BackendObservation) bool {
	return observation.Count == manifest.ExpectedChunkCount && observation.Digest == manifest.ExpectedChunkDigest
}
