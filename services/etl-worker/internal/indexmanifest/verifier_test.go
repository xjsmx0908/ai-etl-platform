package indexmanifest

import (
	"context"
	"errors"
	"testing"

	"ai-etl-pipeline/internal/model"
)

type projectionStub struct {
	observation BackendObservation
	err         error
}

func (p projectionStub) UpsertGeneration(context.Context, GenerationIdentity, model.Chunk) error {
	return p.err
}
func (p projectionStub) ObserveGeneration(context.Context, GenerationIdentity) (BackendObservation, error) {
	return p.observation, p.err
}

type lifecycleStub struct {
	observations  map[Backend]BackendObservation
	ready, failed bool
	reason        string
}

func (s *lifecycleStub) Observe(_ context.Context, _ string, backend Backend, observation BackendObservation) error {
	if s.observations == nil {
		s.observations = map[Backend]BackendObservation{}
	}
	s.observations[backend] = observation
	return nil
}
func (s *lifecycleStub) MarkReady(context.Context, string) error { s.ready = true; return nil }
func (s *lifecycleStub) Fail(_ context.Context, _ string, reason string) error {
	s.failed = true
	s.reason = reason
	return nil
}

func TestVerifierRecordsBothMatchingObservationsAndMarksReady(t *testing.T) {
	manifest := Manifest{GenerationID: "gen-1", TenantID: "tenant-a", DocumentID: "doc", DocumentVersionID: "job-1", ExpectedChunkCount: 2, ExpectedChunkDigest: "digest"}
	lifecycle := &lifecycleStub{}
	verifier := NewVerifier(projectionStub{observation: BackendObservation{Count: 2, Digest: "digest"}}, projectionStub{observation: BackendObservation{Count: 2, Digest: "digest"}}, lifecycle)
	if err := verifier.Verify(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	if !lifecycle.ready || lifecycle.failed || len(lifecycle.observations) != 2 {
		t.Fatalf("lifecycle=%+v", lifecycle)
	}
}

func TestVerifierFailsManifestOnBackendErrorOrIdentityMismatch(t *testing.T) {
	manifest := Manifest{GenerationID: "gen-1", TenantID: "tenant-a", DocumentID: "doc", DocumentVersionID: "job-1", ExpectedChunkCount: 2, ExpectedChunkDigest: "digest"}
	for name, projections := range map[string]struct{ qdrant, elastic projectionStub }{
		"backend error":              {projectionStub{err: errors.New("down")}, projectionStub{}},
		"equal count wrong identity": {projectionStub{observation: BackendObservation{Count: 2, Digest: "other"}}, projectionStub{observation: BackendObservation{Count: 2, Digest: "digest"}}},
	} {
		t.Run(name, func(t *testing.T) {
			lifecycle := &lifecycleStub{}
			err := NewVerifier(projections.qdrant, projections.elastic, lifecycle).Verify(context.Background(), manifest)
			if err == nil || !lifecycle.failed || lifecycle.ready {
				t.Fatalf("err=%v lifecycle=%+v", err, lifecycle)
			}
		})
	}
}
