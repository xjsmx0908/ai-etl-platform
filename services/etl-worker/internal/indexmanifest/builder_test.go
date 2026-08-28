package indexmanifest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"ai-etl-pipeline/internal/model"
)

type buildLifecycleStub struct {
	events   []string
	manifest Manifest
	active   string
	failed   string
}

func (s *buildLifecycleStub) Begin(_ context.Context, manifest Manifest) (Manifest, error) {
	s.events = append(s.events, "begin")
	manifest.State = StateBuilding
	manifest.ExpectedActiveGenerationID = s.active
	s.manifest = manifest
	return manifest, nil
}

func (s *buildLifecycleStub) SealExpected(_ context.Context, generationID string, count int, digest string) (Manifest, error) {
	s.events = append(s.events, "seal")
	s.manifest.GenerationID = generationID
	s.manifest.ExpectedChunkCount = count
	s.manifest.ExpectedChunkDigest = digest
	s.manifest.ExpectedSealed = true
	return s.manifest, nil
}

func (s *buildLifecycleStub) Observe(_ context.Context, _ string, backend Backend, observation BackendObservation) error {
	s.events = append(s.events, "observe:"+string(backend))
	if backend == BackendQdrant {
		s.manifest.Qdrant = observation
	} else {
		s.manifest.Elasticsearch = observation
	}
	return nil
}

func (s *buildLifecycleStub) MarkReady(context.Context, string) error {
	s.events = append(s.events, "ready")
	s.manifest.State = StateReady
	return nil
}

func (s *buildLifecycleStub) Fail(_ context.Context, _ string, reason string) error {
	s.events = append(s.events, "fail")
	s.failed = reason
	s.manifest.State = StateFailed
	return nil
}

func (s *buildLifecycleStub) Retry(context.Context, string) error { return nil }

func (s *buildLifecycleStub) ActiveGeneration(context.Context, VersionIdentity) (string, bool, error) {
	return s.active, s.active != "", nil
}

func (s *buildLifecycleStub) Activate(_ context.Context, target ActivationTarget) error {
	s.events = append(s.events, "activate")
	if target.ExpectedActiveGenerationID != s.active {
		return ErrConflict
	}
	s.active = target.GenerationID
	s.manifest.State = StateActive
	return nil
}

type buildProjectionStub struct {
	name      string
	events    *[]string
	chunks    []model.Chunk
	upsertErr error
}

func (p *buildProjectionStub) UpsertGeneration(_ context.Context, _ GenerationIdentity, chunk model.Chunk) error {
	*p.events = append(*p.events, "upsert:"+p.name)
	if p.upsertErr != nil {
		return p.upsertErr
	}
	p.chunks = append(p.chunks, chunk)
	return nil
}

func (p *buildProjectionStub) ObserveGeneration(_ context.Context, identity GenerationIdentity) (BackendObservation, error) {
	*p.events = append(*p.events, "observe-backend:"+p.name)
	digest, err := ChunkIdentityDigest(identity, p.chunks)
	return BackendObservation{Count: len(p.chunks), Digest: digest}, err
}

func testBuildRequest() BuildRequest {
	return BuildRequest{
		Version: VersionIdentity{TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1"},
		Definition: BuildDefinition{
			ChunkerVersion: "text-v1:size=4096:overlap=200", EmbeddingModel: "embed-v1",
			VectorDimension: 3, SchemaVersion: "generation-payload-v1",
			CollectionVersion: "documents-v1", IndexVersion: "documents-text-v2",
		},
	}
}

func TestBuilderPersistsBeforeProjectionAndPublishesOnlyAfterDualVerification(t *testing.T) {
	lifecycle := &buildLifecycleStub{}
	qdrant := &buildProjectionStub{name: "qdrant", events: &lifecycle.events}
	elasticsearch := &buildProjectionStub{name: "elasticsearch", events: &lifecycle.events}
	builder := NewBuilder(lifecycle, qdrant, elasticsearch)

	build, err := builder.Begin(context.Background(), testBuildRequest())
	if err != nil {
		t.Fatal(err)
	}
	chunk := model.Chunk{ChunkID: "doc-1_0000", TenantID: "acme", DocID: "doc-1", Index: 0, Content: "verified"}
	if err := build.Upsert(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	if err := build.Complete(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := []string{"begin", "upsert:qdrant", "upsert:elasticsearch", "seal", "observe-backend:qdrant", "observe:qdrant", "observe-backend:elasticsearch", "observe:elasticsearch", "ready", "activate"}
	if !reflect.DeepEqual(lifecycle.events, want) {
		t.Fatalf("events = %#v, want %#v", lifecycle.events, want)
	}
	if lifecycle.active != build.Identity().GenerationID {
		t.Fatalf("active generation = %q", lifecycle.active)
	}
}

func TestBuilderGenerationIsStableForRedeliveryAndChangesWithConfiguration(t *testing.T) {
	lifecycle := &buildLifecycleStub{}
	projection := &buildProjectionStub{name: "projection", events: &lifecycle.events}
	builder := NewBuilder(lifecycle, projection, projection)
	first, err := builder.Begin(context.Background(), testBuildRequest())
	if err != nil {
		t.Fatal(err)
	}
	second, err := builder.Begin(context.Background(), testBuildRequest())
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity().GenerationID != second.Identity().GenerationID {
		t.Fatal("redelivery changed generation identity")
	}
	changed := testBuildRequest()
	changed.Definition.EmbeddingModel = "embed-v2"
	third, err := builder.Begin(context.Background(), changed)
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity().GenerationID == third.Identity().GenerationID {
		t.Fatal("configuration change reused generation identity")
	}
}

func TestBuilderDoesNotPublishWhenElasticsearchWriteFails(t *testing.T) {
	lifecycle := &buildLifecycleStub{}
	qdrant := &buildProjectionStub{name: "qdrant", events: &lifecycle.events}
	elasticsearch := &buildProjectionStub{name: "elasticsearch", events: &lifecycle.events, upsertErr: errors.New("elasticsearch unavailable")}
	build, err := NewBuilder(lifecycle, qdrant, elasticsearch).Begin(context.Background(), testBuildRequest())
	if err != nil {
		t.Fatal(err)
	}
	err = build.Upsert(context.Background(), model.Chunk{ChunkID: "doc-1_0000", TenantID: "acme", DocID: "doc-1", Index: 0, Content: "partial"})
	if err == nil {
		t.Fatal("Upsert succeeded while Elasticsearch was unavailable")
	}
	if abortErr := build.Abort(context.Background(), err); abortErr != nil {
		t.Fatal(abortErr)
	}
	if lifecycle.active != "" || lifecycle.failed == "" {
		t.Fatalf("active=%q failed=%q", lifecycle.active, lifecycle.failed)
	}
}

func TestBuilderRedeliveryCannotAdoptAnewerActiveGeneration(t *testing.T) {
	lifecycle := &buildLifecycleStub{active: "gen-old"}
	qdrant := &buildProjectionStub{name: "qdrant", events: &lifecycle.events}
	elasticsearch := &buildProjectionStub{name: "elasticsearch", events: &lifecycle.events}
	build, err := NewBuilder(lifecycle, qdrant, elasticsearch).Begin(context.Background(), testBuildRequest())
	if err != nil {
		t.Fatal(err)
	}
	chunk := model.Chunk{ChunkID: "doc-1_0000", TenantID: "acme", DocID: "doc-1", Index: 0, Content: "stale"}
	if err := build.Upsert(context.Background(), chunk); err != nil {
		t.Fatal(err)
	}
	// Another generation wins after this build has persisted its predecessor.
	lifecycle.active = "gen-new-winner"
	if err := build.Complete(context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatalf("Complete error=%v, want stale-writer conflict", err)
	}
	if lifecycle.active != "gen-new-winner" {
		t.Fatalf("stale build replaced winner with %q", lifecycle.active)
	}
}
