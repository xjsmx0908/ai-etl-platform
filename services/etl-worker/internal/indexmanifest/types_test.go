package indexmanifest

import (
	"testing"

	"ai-etl-pipeline/internal/model"
)

func TestChunkIdentityDigestIsOrderIndependent(t *testing.T) {
	identity := GenerationIdentity{GenerationID: "gen-1", VersionIdentity: VersionIdentity{TenantID: "tenant-a", DocumentID: "doc-2", DocumentVersionID: "version-1"}}
	chunks := []model.Chunk{
		{ChunkID: "doc-2_0001", DocID: "doc-2", TenantID: "tenant-a", Index: 1, Content: "second"},
		{ChunkID: "doc-2_0000", DocID: "doc-2", TenantID: "tenant-a", Index: 0, Content: "first"},
	}
	reordered := []model.Chunk{chunks[1], chunks[0]}
	first, err := ChunkIdentityDigest(identity, chunks)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ChunkIdentityDigest(identity, reordered)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("digest changed with processing order: %s != %s", first, second)
	}
	const independentlyCalculated = "sha256:3324730f76efebd63c13a279f606471cefc87b12c94242267798986444c45e1c"
	if first != independentlyCalculated {
		t.Fatalf("digest=%s want=%s", first, independentlyCalculated)
	}
}

func TestChunkIdentityDigestChangesWhenIdentityOrContentChanges(t *testing.T) {
	identity := GenerationIdentity{GenerationID: "gen-1", VersionIdentity: VersionIdentity{TenantID: "tenant-a", DocumentID: "doc", DocumentVersionID: "version-1"}}
	base := []model.Chunk{{ChunkID: "doc_0000", DocID: "doc", TenantID: "tenant-a", Index: 0, Content: "hello"}}
	want, err := ChunkIdentityDigest(identity, base)
	if err != nil {
		t.Fatal(err)
	}
	changedContent, err := ChunkIdentityDigest(identity, []model.Chunk{{ChunkID: "doc_0000", DocID: "doc", TenantID: "tenant-a", Index: 0, Content: "goodbye"}})
	if err != nil {
		t.Fatal(err)
	}
	changedID, err := ChunkIdentityDigest(identity, []model.Chunk{{ChunkID: "other_0000", DocID: "doc", TenantID: "tenant-a", Index: 0, Content: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if want == changedContent || want == changedID {
		t.Fatal("digest did not change")
	}
	identity.GenerationID = "gen-2"
	changedGeneration, err := ChunkIdentityDigest(identity, base)
	if err != nil {
		t.Fatal(err)
	}
	if want == changedGeneration {
		t.Fatal("digest did not bind the generation identity")
	}
}

func TestChunkIdentityDigestRejectsCrossDocumentAndDuplicateChunks(t *testing.T) {
	identity := GenerationIdentity{GenerationID: "gen-1", VersionIdentity: VersionIdentity{TenantID: "tenant-a", DocumentID: "doc", DocumentVersionID: "version-1"}}
	if _, err := ChunkIdentityDigest(identity, []model.Chunk{{ChunkID: "x", DocID: "other", TenantID: "tenant-a", Index: 0}}); err == nil {
		t.Fatal("accepted a chunk from another document")
	}
	chunk := model.Chunk{ChunkID: "doc_0000", DocID: "doc", TenantID: "tenant-a", Index: 0}
	if _, err := ChunkIdentityDigest(identity, []model.Chunk{chunk, chunk}); err == nil {
		t.Fatal("accepted duplicate chunk identity")
	}
}

func TestIdentityDigestMatchesChunkDigestFromBackendContentHashes(t *testing.T) {
	identity := GenerationIdentity{GenerationID: "gen-1", VersionIdentity: VersionIdentity{TenantID: "tenant-a", DocumentID: "doc", DocumentVersionID: "job-1"}}
	chunks := []model.Chunk{
		{ChunkID: "doc_0001", DocID: "doc", TenantID: "tenant-a", Index: 1, Content: "second"},
		{ChunkID: "doc_0000", DocID: "doc", TenantID: "tenant-a", Index: 0, Content: "first"},
	}
	want, err := ChunkIdentityDigest(identity, chunks)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := IdentityDigest(identity, []ChunkIdentity{
		{ChunkID: "doc_0000", Index: 0, ContentHash: ContentHash("first")},
		{ChunkID: "doc_0001", Index: 1, ContentHash: ContentHash("second")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if observed != want {
		t.Fatalf("backend digest=%s want=%s", observed, want)
	}
}
