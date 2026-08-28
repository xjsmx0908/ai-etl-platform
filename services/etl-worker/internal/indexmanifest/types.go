// Package indexmanifest owns the durable lifecycle of generation-scoped search indexes.
package indexmanifest

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
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
	Count  int
	Digest string
}

// GenerationIdentity scopes a digest to the exact catalog version and index
// generation. Identical content rebuilt as a new generation therefore cannot
// be mistaken for an observation of the old generation.
type GenerationIdentity struct {
	GenerationID      string
	TenantID          string
	DocumentID        string
	DocumentVersionID string
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
	CreatedAt, VerifiedAt, ActivatedAt                                             time.Time
}

var (
	ErrInvalidManifest   = errors.New("indexmanifest: invalid manifest")
	ErrNotReady          = errors.New("indexmanifest: manifest is not ready")
	ErrInvalidTransition = errors.New("indexmanifest: invalid state transition")
	ErrConflict          = errors.New("indexmanifest: compare-and-set conflict")
)

func (m *Manifest) MarkReady() error {
	if m.State != StateBuilding {
		return ErrInvalidTransition
	}
	if m.ExpectedChunkCount < 0 || m.ExpectedChunkDigest == "" ||
		m.Qdrant.Count != m.ExpectedChunkCount || m.Qdrant.Digest != m.ExpectedChunkDigest ||
		m.Elasticsearch.Count != m.ExpectedChunkCount || m.Elasticsearch.Digest != m.ExpectedChunkDigest {
		return ErrNotReady
	}
	m.State = StateReady
	return nil
}

func (m *Manifest) Activate() error {
	if m.State != StateReady {
		return ErrInvalidTransition
	}
	m.State = StateActive
	return nil
}

// ChunkIdentityDigest returns a stable digest of chunk identities and content
// hashes. Sorting makes it independent of delivery or worker completion order.
func ChunkIdentityDigest(identity GenerationIdentity, chunks []model.Chunk) (string, error) {
	if identity.GenerationID == "" || identity.TenantID == "" || identity.DocumentID == "" || identity.DocumentVersionID == "" {
		return "", fmt.Errorf("%w: generation identity is required", ErrInvalidManifest)
	}
	items := make([]string, 0, len(chunks))
	seen := make(map[string]struct{}, len(chunks))
	for _, c := range chunks {
		if c.ChunkID == "" || c.Index < 0 || c.TenantID != identity.TenantID || c.DocID != identity.DocumentID {
			return "", fmt.Errorf("%w: chunk identity does not match generation", ErrInvalidManifest)
		}
		key := fmt.Sprintf("%d\x00%s", c.Index, c.ChunkID)
		if _, exists := seen[key]; exists {
			return "", fmt.Errorf("%w: duplicate chunk identity", ErrInvalidManifest)
		}
		seen[key] = struct{}{}
		h := sha256.Sum256([]byte(c.Content))
		items = append(items, fmt.Sprintf("%d\x00%s\x00%s", c.Index, c.ChunkID, hex.EncodeToString(h[:])))
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
