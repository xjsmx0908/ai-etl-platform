// Package indexmanifest owns the durable lifecycle of generation-scoped search indexes.
package indexmanifest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ai-etl-pipeline/internal/model"
)

type ManifestState string

const (
	StateBuilding ManifestState = "building"
	StateReady    ManifestState = "ready"
	StateFailed   ManifestState = "failed"
	StateActive   ManifestState = "active"
	StateRetired  ManifestState = "retired"
)

type Backend string

const (
	BackendQdrant        Backend = "qdrant"
	BackendElasticsearch Backend = "elasticsearch"
)

type BackendObservation struct {
	Count      int
	Digest     string
	ObservedAt time.Time
}

// Projection is the generation-aware seam implemented by each derived index.
type Projection interface {
	UpsertGeneration(context.Context, GenerationIdentity, model.Chunk) error
	ObserveGeneration(context.Context, GenerationIdentity) (BackendObservation, error)
}

type ChunkIdentity struct {
	ChunkID     string
	Index       int
	ContentHash string
}

func ContentHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(h[:])
}

// VersionIdentity binds an index generation to one durable ingestion job. The
// PostgreSQL adapter enforces this tuple against ingestion_jobs.
type VersionIdentity struct {
	TenantID          string
	DocumentID        string
	DocumentVersionID string
}

type GenerationIdentity struct {
	VersionIdentity
	GenerationID string
}

type ActivationTarget struct {
	Version                    VersionIdentity
	GenerationID               string
	ExpectedActiveGenerationID string
}

type Manifest struct {
	GenerationID, TenantID, DocumentID, DocumentVersionID                          string
	ChunkerVersion, EmbeddingModel, SchemaVersion, CollectionVersion, IndexVersion string
	VectorDimension, ExpectedChunkCount                                            int
	ExpectedChunkDigest                                                            string
	Qdrant, Elasticsearch                                                          BackendObservation
	State                                                                          ManifestState
	Attempts                                                                       int
	LastError                                                                      string
	CreatedAt, LastAttemptAt, VerifiedAt, ActivatedAt                              time.Time
}

var (
	ErrInvalidManifest = errors.New("indexmanifest: invalid manifest")
	ErrNotReady        = errors.New("indexmanifest: manifest is not ready")
	ErrConflict        = errors.New("indexmanifest: compare-and-set conflict")
)

// ChunkIdentityDigest returns a stable digest of chunk identities and content
// hashes. Sorting makes it independent of delivery or worker completion order.
func ChunkIdentityDigest(identity GenerationIdentity, chunks []model.Chunk) (string, error) {
	if identity.GenerationID == "" || identity.TenantID == "" || identity.DocumentID == "" || identity.DocumentVersionID == "" {
		return "", fmt.Errorf("%w: generation identity is required", ErrInvalidManifest)
	}
	items := make([]ChunkIdentity, 0, len(chunks))
	for _, c := range chunks {
		if c.ChunkID == "" || c.Index < 0 || c.TenantID != identity.TenantID || c.DocID != identity.DocumentID {
			return "", fmt.Errorf("%w: chunk identity does not match generation", ErrInvalidManifest)
		}
		items = append(items, ChunkIdentity{ChunkID: c.ChunkID, Index: c.Index, ContentHash: ContentHash(c.Content)})
	}
	return IdentityDigest(identity, items)
}

func IdentityDigest(identity GenerationIdentity, identities []ChunkIdentity) (string, error) {
	if identity.GenerationID == "" || identity.TenantID == "" || identity.DocumentID == "" || identity.DocumentVersionID == "" {
		return "", fmt.Errorf("%w: generation identity is required", ErrInvalidManifest)
	}
	items := make([]string, 0, len(identities))
	seen := make(map[string]struct{}, len(identities))
	for _, chunk := range identities {
		if chunk.ChunkID == "" || chunk.Index < 0 || chunk.ContentHash == "" {
			return "", fmt.Errorf("%w: incomplete chunk identity", ErrInvalidManifest)
		}
		key := fmt.Sprintf("%d\x00%s", chunk.Index, chunk.ChunkID)
		if _, exists := seen[key]; exists {
			return "", fmt.Errorf("%w: duplicate chunk identity", ErrInvalidManifest)
		}
		seen[key] = struct{}{}
		items = append(items, fmt.Sprintf("%d\x00%s\x00%s", chunk.Index, chunk.ChunkID, strings.TrimPrefix(chunk.ContentHash, "sha256:")))
	}
	sort.Strings(items)
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\n", identity.GenerationID, identity.TenantID, identity.DocumentID, identity.DocumentVersionID)
	for _, item := range items {
		_, _ = h.Write([]byte(item))
		_, _ = h.Write([]byte{'\n'})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}
