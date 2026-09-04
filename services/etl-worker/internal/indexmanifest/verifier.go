package indexmanifest

import (
	"context"
	"fmt"
	"time"
)

// Generation projections are eventually consistent even when individual
// writes use wait=true/refresh=wait_for. A just-completed build may therefore
// briefly observe fewer points than were written. Keep the strict count and
// digest rule, but give projections a short bounded window to become visible.
const (
	verificationAttempts   = 10
	verificationRetryDelay = 1 * time.Second
)

type VerificationLifecycle interface {
	Observe(context.Context, string, Backend, BackendObservation) error
	MarkReady(context.Context, string) error
	Fail(context.Context, string, string) error
}

// Verifier owns the cross-backend completeness rule so callers cannot mark a
// manifest ready after observing only one projection.
type Verifier struct {
	qdrant        Projection
	elasticsearch Projection
	lifecycle     VerificationLifecycle
}

func NewVerifier(qdrant, elasticsearch Projection, lifecycle VerificationLifecycle) *Verifier {
	return &Verifier{qdrant: qdrant, elasticsearch: elasticsearch, lifecycle: lifecycle}
}

func (v *Verifier) Verify(ctx context.Context, manifest Manifest) error {
	if manifest.ExpectedChunkCount <= 0 || manifest.ExpectedChunkDigest == "" {
		return v.fail(ctx, manifest.GenerationID, ErrNotReady)
	}
	identity := GenerationIdentity{GenerationID: manifest.GenerationID, VersionIdentity: VersionIdentity{TenantID: manifest.TenantID, DocumentID: manifest.DocumentID, DocumentVersionID: manifest.DocumentVersionID}}
	for _, backend := range []struct {
		name       Backend
		projection Projection
	}{{BackendQdrant, v.qdrant}, {BackendElasticsearch, v.elasticsearch}} {
		observation, err := observeUntilMatch(ctx, backend.projection, identity, manifest.ExpectedChunkCount, manifest.ExpectedChunkDigest)
		if err != nil {
			return v.fail(ctx, manifest.GenerationID, fmt.Errorf("observe %s: %w", backend.name, err))
		}
		if err := v.lifecycle.Observe(ctx, manifest.GenerationID, backend.name, observation); err != nil {
			return fmt.Errorf("record %s observation: %w", backend.name, err)
		}
	}
	if err := v.lifecycle.MarkReady(ctx, manifest.GenerationID); err != nil {
		return fmt.Errorf("mark verified manifest ready: %w", err)
	}
	return nil
}

func observeUntilMatch(ctx context.Context, projection Projection, identity GenerationIdentity, expectedCount int, expectedDigest string) (BackendObservation, error) {
	var last BackendObservation
	for attempt := 0; attempt < verificationAttempts; attempt++ {
		observation, err := projection.ObserveGeneration(ctx, identity)
		if err != nil {
			return BackendObservation{}, err
		}
		last = observation
		if observation.Count == expectedCount && observation.Digest == expectedDigest {
			return observation, nil
		}
		// Retry only an under-count. Equal-count digest mismatches and overfull
		// projections indicate wrong identities/duplicates, not visibility lag.
		if observation.Count >= expectedCount || attempt == verificationAttempts-1 {
			break
		}
		timer := time.NewTimer(verificationRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return BackendObservation{}, ctx.Err()
		case <-timer.C:
		}
	}
	return BackendObservation{}, fmt.Errorf("observation mismatch: count=%d digest=%s expected_count=%d expected_digest=%s", last.Count, last.Digest, expectedCount, expectedDigest)
}

func (v *Verifier) fail(ctx context.Context, generationID string, cause error) error {
	if err := v.lifecycle.Fail(ctx, generationID, cause.Error()); err != nil {
		return fmt.Errorf("%v; record manifest failure: %w", cause, err)
	}
	return cause
}

var _ VerificationLifecycle = (*PostgresStore)(nil)
