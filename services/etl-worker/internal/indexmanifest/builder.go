package indexmanifest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"ai-etl-pipeline/internal/model"
)

type BuildDefinition struct {
	ChunkerVersion, EmbeddingModel, SchemaVersion, CollectionVersion, IndexVersion string
	VectorDimension                                                                int
}

type BuildRequest struct {
	Version    VersionIdentity
	Definition BuildDefinition
}

type BuildSession interface {
	Identity() GenerationIdentity
	Upsert(context.Context, model.Chunk) error
	Complete(context.Context) error
	Abort(context.Context, error) error
}

type BuildStarter interface {
	Begin(context.Context, BuildRequest) (BuildSession, error)
}

type BuildLifecycle interface {
	Begin(context.Context, Manifest) (Manifest, error)
	SealExpected(context.Context, string, int, string) (Manifest, error)
	VerificationLifecycle
	Retry(context.Context, string) error
	Activate(context.Context, ActivationTarget) error
}

// Builder hides manifest ordering, cross-backend verification, and activation
// from the ingestion pipeline.
type Builder struct {
	lifecycle     BuildLifecycle
	qdrant        Projection
	elasticsearch Projection
}

func NewBuilder(lifecycle BuildLifecycle, qdrant, elasticsearch Projection) *Builder {
	return &Builder{lifecycle: lifecycle, qdrant: qdrant, elasticsearch: elasticsearch}
}

type Build struct {
	lifecycle BuildLifecycle
	verifier  *Verifier
	identity  GenerationIdentity
	manifest  Manifest
	qdrant    Projection
	elastic   Projection
	chunks    []model.Chunk
}

func (b *Builder) Begin(ctx context.Context, request BuildRequest) (BuildSession, error) {
	manifest, err := manifestFor(request)
	if err != nil {
		return nil, err
	}
	manifest, err = b.lifecycle.Begin(ctx, manifest)
	if err != nil {
		return nil, fmt.Errorf("begin generation manifest: %w", err)
	}
	if manifest.State == StateFailed {
		if err := b.lifecycle.Retry(ctx, manifest.GenerationID); err != nil {
			return nil, fmt.Errorf("retry generation manifest: %w", err)
		}
		manifest.State = StateBuilding
	}
	identity := GenerationIdentity{VersionIdentity: request.Version, GenerationID: manifest.GenerationID}
	return &Build{
		lifecycle: b.lifecycle, verifier: NewVerifier(b.qdrant, b.elasticsearch, b.lifecycle),
		identity: identity, manifest: manifest, qdrant: b.qdrant, elastic: b.elasticsearch,
	}, nil
}

var _ BuildStarter = (*Builder)(nil)

func (b *Build) Identity() GenerationIdentity { return b.identity }

func (b *Build) Upsert(ctx context.Context, chunk model.Chunk) error {
	if err := b.qdrant.UpsertGeneration(ctx, b.identity, chunk); err != nil {
		return fmt.Errorf("write qdrant generation: %w", err)
	}
	if err := b.elastic.UpsertGeneration(ctx, b.identity, chunk); err != nil {
		return fmt.Errorf("write elasticsearch generation: %w", err)
	}
	b.chunks = append(b.chunks, chunk)
	return nil
}

func (b *Build) Complete(ctx context.Context) error {
	digest, err := ChunkIdentityDigest(b.identity, b.chunks)
	if err != nil {
		return err
	}
	manifest, err := b.lifecycle.SealExpected(ctx, b.identity.GenerationID, len(b.chunks), digest)
	if err != nil {
		return fmt.Errorf("seal generation manifest: %w", err)
	}
	b.manifest = manifest
	if manifest.State == StateActive {
		return nil
	}
	if manifest.State == StateBuilding {
		if err := b.verifier.Verify(ctx, manifest); err != nil {
			return err
		}
	}
	if manifest.ExpectedActiveGenerationID == b.identity.GenerationID {
		return nil
	}
	if err := b.lifecycle.Activate(ctx, ActivationTarget{
		Version: b.identity.VersionIdentity, GenerationID: b.identity.GenerationID,
		ExpectedActiveGenerationID: manifest.ExpectedActiveGenerationID,
	}); err != nil {
		return fmt.Errorf("activate generation: %w", err)
	}
	return nil
}

func (b *Build) Abort(ctx context.Context, cause error) error {
	if cause == nil {
		return ErrInvalidManifest
	}
	if b.manifest.State != StateBuilding {
		return nil
	}
	if err := b.lifecycle.Fail(ctx, b.identity.GenerationID, cause.Error()); err != nil && !errors.Is(err, ErrConflict) {
		return fmt.Errorf("fail generation manifest: %w", err)
	}
	b.manifest.State = StateFailed
	return nil
}

func manifestFor(request BuildRequest) (Manifest, error) {
	v, d := request.Version, request.Definition
	if v.TenantID == "" || v.DocumentID == "" || v.DocumentVersionID == "" ||
		d.ChunkerVersion == "" || d.EmbeddingModel == "" || d.VectorDimension <= 0 ||
		d.SchemaVersion == "" || d.CollectionVersion == "" || d.IndexVersion == "" {
		return Manifest{}, ErrInvalidManifest
	}
	parts := []string{v.TenantID, v.DocumentID, v.DocumentVersionID, d.ChunkerVersion,
		d.EmbeddingModel, fmt.Sprint(d.VectorDimension), d.SchemaVersion, d.CollectionVersion, d.IndexVersion}
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return Manifest{
		GenerationID: "gen-" + hex.EncodeToString(h[:]), TenantID: v.TenantID,
		DocumentID: v.DocumentID, DocumentVersionID: v.DocumentVersionID,
		ChunkerVersion: d.ChunkerVersion, EmbeddingModel: d.EmbeddingModel,
		VectorDimension: d.VectorDimension, SchemaVersion: d.SchemaVersion,
		CollectionVersion: d.CollectionVersion, IndexVersion: d.IndexVersion,
		State: StateBuilding,
	}, nil
}
