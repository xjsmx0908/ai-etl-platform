package store

import (
	"context"
	"testing"

	"ai-etl-pipeline/internal/indexmanifest"
	"ai-etl-pipeline/internal/model"
)

func TestMemoryStorerGenerationProjectionIsIdempotent(t *testing.T) {
	storer := NewMemoryStorer()
	identity := indexmanifest.GenerationIdentity{
		GenerationID: "gen-1",
		VersionIdentity: indexmanifest.VersionIdentity{
			TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1",
		},
	}
	chunk := model.Chunk{ChunkID: "doc-1_0000", TenantID: "acme", DocID: "doc-1", Index: 0, Content: "hello"}
	for range 2 {
		if err := storer.UpsertGeneration(context.Background(), identity, chunk); err != nil {
			t.Fatal(err)
		}
	}
	got, err := storer.ObserveGeneration(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	wantDigest, err := indexmanifest.ChunkIdentityDigest(identity, []model.Chunk{chunk})
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || got.Digest != wantDigest {
		t.Fatalf("observation = %+v", got)
	}
}
