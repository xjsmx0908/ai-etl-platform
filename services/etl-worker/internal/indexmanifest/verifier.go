package indexmanifest

import (
	"context"
	"fmt"
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
	identity := GenerationIdentity{GenerationID: manifest.GenerationID, VersionIdentity: VersionIdentity{TenantID: manifest.TenantID, DocumentID: manifest.DocumentID, DocumentVersionID: manifest.DocumentVersionID}}
	for _, backend := range []struct {
		name       Backend
		projection Projection
	}{{BackendQdrant, v.qdrant}, {BackendElasticsearch, v.elasticsearch}} {
		observation, err := backend.projection.ObserveGeneration(ctx, identity)
		if err != nil {
			return v.fail(ctx, manifest.GenerationID, fmt.Errorf("observe %s: %w", backend.name, err))
		}
		if err := v.lifecycle.Observe(ctx, manifest.GenerationID, backend.name, observation); err != nil {
			return fmt.Errorf("record %s observation: %w", backend.name, err)
		}
		if observation.Count != manifest.ExpectedChunkCount || observation.Digest != manifest.ExpectedChunkDigest {
			return v.fail(ctx, manifest.GenerationID, fmt.Errorf("%s observation mismatch", backend.name))
		}
	}
	if err := v.lifecycle.MarkReady(ctx, manifest.GenerationID); err != nil {
		return fmt.Errorf("mark verified manifest ready: %w", err)
	}
	return nil
}

func (v *Verifier) fail(ctx context.Context, generationID string, cause error) error {
	if err := v.lifecycle.Fail(ctx, generationID, cause.Error()); err != nil {
		return fmt.Errorf("%v; record manifest failure: %w", cause, err)
	}
	return cause
}

var _ VerificationLifecycle = (*PostgresStore)(nil)
