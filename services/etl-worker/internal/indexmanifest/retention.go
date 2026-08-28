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

type RollbackRequest struct {
	Version                    VersionIdentity
	TargetGenerationID         string
	ExpectedActiveGenerationID string
	Window                     time.Duration
	Lease                      time.Duration
	ClaimToken                 string
}

type RollbackOutcome string

const (
	RollbackSucceeded RollbackOutcome = "success"
	RollbackConflict  RollbackOutcome = "conflict"
	RollbackNotReady  RollbackOutcome = "not_ready"
	RollbackFailed    RollbackOutcome = "failed"
)

func newRetentionToken() string { return "retention-" + uuid.NewString() }

type RollbackCommit struct {
	Request       RollbackRequest
	Qdrant        BackendObservation
	Elasticsearch BackendObservation
}

type RollbackStore interface {
	RollbackTarget(context.Context, RollbackRequest) (Manifest, error)
	Rollback(context.Context, RollbackCommit) error
}

// Rollbacker keeps backend verification outside the transaction while the
// store owns the final compare-and-set promotion under a database lock.
type Rollbacker struct {
	store         RollbackStore
	qdrant        Projection
	elasticsearch Projection
	observer      RollbackObserver
}

type RollbackObserver interface {
	ObserveRollback(RollbackOutcome)
}

func NewRollbacker(store RollbackStore, qdrant, elasticsearch Projection) *Rollbacker {
	return &Rollbacker{store: store, qdrant: qdrant, elasticsearch: elasticsearch}
}

func (r *Rollbacker) WithObserver(observer RollbackObserver) *Rollbacker {
	r.observer = observer
	return r
}

func (r *Rollbacker) Rollback(ctx context.Context, request RollbackRequest) error {
	outcome := RollbackFailed
	defer func() {
		if r.observer != nil {
			r.observer.ObserveRollback(outcome)
		}
	}()
	if r.store == nil || r.qdrant == nil || r.elasticsearch == nil ||
		request.Version.TenantID == "" || request.Version.DocumentID == "" || request.Version.DocumentVersionID == "" ||
		request.TargetGenerationID == "" || request.ExpectedActiveGenerationID == "" ||
		request.TargetGenerationID == request.ExpectedActiveGenerationID || request.Window <= 0 {
		return ErrInvalidManifest
	}
	if request.Lease <= 0 {
		request.Lease = 5 * time.Minute
	}
	if request.ClaimToken == "" {
		request.ClaimToken = newRetentionToken()
	}
	manifest, err := r.store.RollbackTarget(ctx, request)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			outcome = RollbackConflict
		}
		return fmt.Errorf("load rollback target: %w", err)
	}
	identity := GenerationIdentity{VersionIdentity: request.Version, GenerationID: request.TargetGenerationID}
	qdrant, err := r.qdrant.ObserveGeneration(ctx, identity)
	if err != nil {
		return fmt.Errorf("observe rollback qdrant generation: %w", err)
	}
	elasticsearch, err := r.elasticsearch.ObserveGeneration(ctx, identity)
	if err != nil {
		return fmt.Errorf("observe rollback elasticsearch generation: %w", err)
	}
	if !observationMatches(manifest, qdrant) || !observationMatches(manifest, elasticsearch) {
		outcome = RollbackNotReady
		return ErrNotReady
	}
	if err := r.store.Rollback(ctx, RollbackCommit{Request: request, Qdrant: qdrant, Elasticsearch: elasticsearch}); err != nil {
		if errors.Is(err, ErrConflict) {
			outcome = RollbackConflict
		}
		return fmt.Errorf("commit generation rollback: %w", err)
	}
	outcome = RollbackSucceeded
	return nil
}

type RetentionClaim struct {
	Window time.Duration
	Lease  time.Duration
	Limit  int
	Token  string
}

type RetentionResult struct {
	Manifest             Manifest
	QdrantDeleted        bool
	ElasticsearchDeleted bool
	Reason               string
	RetryAfter           time.Duration
}

type RetentionStore interface {
	ClaimRetention(context.Context, RetentionClaim) ([]Manifest, error)
	FinishRetention(context.Context, RetentionResult) error
}

type GenerationDeleter interface {
	DeleteGeneration(context.Context, GenerationIdentity) error
}

type RetentionOptions struct {
	Window    time.Duration
	Interval  time.Duration
	Lease     time.Duration
	BatchSize int
}

type RetentionReport struct {
	Claimed    int
	Deleted    int
	Failed     int
	Conflicted int
}

type RetentionObserver interface {
	ObserveRetention(RetentionReport, error)
}

// RetentionCollector owns the cross-backend deletion order. The store only
// removes a manifest after both idempotent projection deletes are confirmed.
type RetentionCollector struct {
	store         RetentionStore
	qdrant        GenerationDeleter
	elasticsearch GenerationDeleter
	options       RetentionOptions
	observer      RetentionObserver
}

func (c *RetentionCollector) WithObserver(observer RetentionObserver) *RetentionCollector {
	c.observer = observer
	return c
}

func NewRetentionCollector(store RetentionStore, qdrant, elasticsearch GenerationDeleter, options RetentionOptions) *RetentionCollector {
	return &RetentionCollector{store: store, qdrant: qdrant, elasticsearch: elasticsearch, options: options}
}

func (c *RetentionCollector) RunOnce(ctx context.Context) (RetentionReport, error) {
	var report RetentionReport
	var runErr error
	defer func() {
		if c.observer != nil {
			c.observer.ObserveRetention(report, runErr)
		}
	}()
	if c.store == nil || c.qdrant == nil || c.elasticsearch == nil || c.options.Window <= 0 {
		runErr = ErrInvalidManifest
		return report, runErr
	}
	claim := RetentionClaim{Window: c.options.Window, Lease: c.options.Lease, Limit: c.options.BatchSize, Token: newRetentionToken()}
	manifests, err := c.store.ClaimRetention(ctx, claim)
	if err != nil {
		runErr = fmt.Errorf("claim generation retention: %w", err)
		return report, runErr
	}
	report = RetentionReport{Claimed: len(manifests)}
	for _, manifest := range manifests {
		identity := GenerationIdentity{GenerationID: manifest.GenerationID, VersionIdentity: VersionIdentity{
			TenantID: manifest.TenantID, DocumentID: manifest.DocumentID, DocumentVersionID: manifest.DocumentVersionID,
		}}
		result := RetentionResult{Manifest: manifest, RetryAfter: c.options.Interval}
		reasons := make([]string, 0, 2)
		if !manifest.QdrantDeletedAt.IsZero() {
			result.QdrantDeleted = true
		} else if err := c.qdrant.DeleteGeneration(ctx, identity); err != nil {
			reasons = append(reasons, "delete qdrant: "+err.Error())
		} else {
			result.QdrantDeleted = true
		}
		if !manifest.ElasticsearchDeletedAt.IsZero() {
			result.ElasticsearchDeleted = true
		} else if err := c.elasticsearch.DeleteGeneration(ctx, identity); err != nil {
			reasons = append(reasons, "delete elasticsearch: "+err.Error())
		} else {
			result.ElasticsearchDeleted = true
		}
		result.Reason = strings.Join(reasons, "; ")
		if err := c.store.FinishRetention(ctx, result); err != nil {
			if errors.Is(err, ErrConflict) {
				report.Conflicted++
				continue
			}
			runErr = fmt.Errorf("finish generation retention: %w", err)
			return report, runErr
		}
		if result.Reason == "" {
			report.Deleted++
		} else {
			report.Failed++
		}
	}
	return report, nil
}

func (c *RetentionCollector) Run(ctx context.Context) {
	interval := c.options.Interval
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		report, err := c.RunOnce(ctx)
		if err != nil {
			if ctx.Err() == nil {
				slog.Error("generation retention failed", "error", err)
			}
		} else if report.Claimed > 0 {
			slog.Info("generation retention completed", "claimed", report.Claimed, "deleted", report.Deleted, "failed", report.Failed, "conflicted", report.Conflicted)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
