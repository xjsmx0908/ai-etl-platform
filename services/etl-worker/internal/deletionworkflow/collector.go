package deletionworkflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ClaimRequest struct {
	Token string
	Lease time.Duration
	Limit int
}

type FinishResult struct {
	Job                  Job
	QdrantDeleted        bool
	ElasticsearchDeleted bool
	ObjectsDeleted       bool
	Reason               string
	RetryAfter           time.Duration
}

type CollectorStore interface {
	Claim(context.Context, ClaimRequest) ([]Job, error)
	Finish(context.Context, FinishResult) error
}

type DocumentDeleter interface {
	DeleteDocument(context.Context, string, string) error
}

type ObjectDeleter interface {
	DeleteObjects(context.Context, []string) error
}

type Options struct {
	Interval     time.Duration
	Lease        time.Duration
	BatchSize    int
	RetryBackoff time.Duration
}

type Report struct {
	Claimed    int
	Completed  int
	Failed     int
	Conflicted int
}

type Collector struct {
	store         CollectorStore
	qdrant        DocumentDeleter
	elasticsearch DocumentDeleter
	objects       ObjectDeleter
	options       Options
	observer      Observer
}

type Observer interface{ ObserveDeletion(Report, error) }

func (c *Collector) WithObserver(observer Observer) *Collector { c.observer = observer; return c }

func NewCollector(store CollectorStore, qdrant, elasticsearch DocumentDeleter, objects ObjectDeleter, options Options) *Collector {
	return &Collector{store: store, qdrant: qdrant, elasticsearch: elasticsearch, objects: objects, options: options}
}

func (c *Collector) RunOnce(ctx context.Context) (Report, error) {
	var report Report
	var runErr error
	defer func() {
		if c != nil && c.observer != nil {
			c.observer.ObserveDeletion(report, runErr)
		}
	}()
	if c == nil || c.store == nil || c.qdrant == nil || c.elasticsearch == nil || c.objects == nil {
		runErr = ErrInvalid
		return report, runErr
	}
	claim := ClaimRequest{Token: "deletion-" + uuid.NewString(), Lease: c.options.Lease, Limit: c.options.BatchSize}
	jobs, err := c.store.Claim(ctx, claim)
	if err != nil {
		runErr = fmt.Errorf("claim document deletion: %w", err)
		return report, runErr
	}
	report.Claimed = len(jobs)
	for _, job := range jobs {
		result := FinishResult{Job: job, RetryAfter: c.options.RetryBackoff}
		reasons := make([]string, 0, 3)
		if !job.QdrantDeletedAt.IsZero() {
			result.QdrantDeleted = true
		} else if err := c.qdrant.DeleteDocument(ctx, job.TenantID, job.DocumentID); err != nil {
			reasons = append(reasons, "qdrant: "+err.Error())
		} else {
			result.QdrantDeleted = true
		}
		if !job.ElasticsearchDeletedAt.IsZero() {
			result.ElasticsearchDeleted = true
		} else if err := c.elasticsearch.DeleteDocument(ctx, job.TenantID, job.DocumentID); err != nil {
			reasons = append(reasons, "elasticsearch: "+err.Error())
		} else {
			result.ElasticsearchDeleted = true
		}
		if !job.ObjectsDeletedAt.IsZero() {
			result.ObjectsDeleted = true
		} else if err := c.objects.DeleteObjects(ctx, job.ObjectKeys); err != nil {
			reasons = append(reasons, "objects: "+err.Error())
		} else {
			result.ObjectsDeleted = true
		}
		result.Reason = strings.Join(reasons, "; ")
		if err := c.store.Finish(ctx, result); err != nil {
			if errors.Is(err, ErrConflict) {
				report.Conflicted++
				continue
			}
			runErr = fmt.Errorf("finish document deletion: %w", err)
			return report, runErr
		}
		if result.Reason == "" {
			report.Completed++
		} else {
			report.Failed++
		}
	}
	return report, nil
}

func (c *Collector) Run(ctx context.Context) {
	interval := c.options.Interval
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if report, err := c.RunOnce(ctx); err != nil {
			if ctx.Err() == nil {
				slog.Error("document deletion collector failed", "error", err)
			}
		} else if report.Claimed > 0 {
			slog.Info("document deletion collector pass", "claimed", report.Claimed, "completed", report.Completed, "failed", report.Failed, "conflicted", report.Conflicted)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
