package query

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
)

// The predicate is tested in internal/retrieval; this pins the wiring: that the
// gate runs on the request path, returns the dedicated reason rather than the
// generic no-evidence one, and ships no sources.
//
// It runs before retrieval, so a service with no retriever is enough to prove
// it fired — a gate that did not fire would reach the retrieval call and fail
// loudly instead of returning 200.
func TestHandleQueryRefusesQuestionsThatReferToAPreviousTurn(t *testing.T) {
	svc := NewService(config.Config{})
	req := httptest.NewRequest(http.MethodPost, "/v1/query",
		strings.NewReader(`{"question":"刚才那份文档里还写了什么？"}`))
	ctx := context.WithValue(req.Context(), auth.CtxTenantID, "acme")
	ctx = context.WithValue(ctx, auth.CtxUserID, "alice")
	ctx = context.WithValue(ctx, auth.CtxPermission, "user")
	w := httptest.NewRecorder()

	svc.HandleQuery(w, req.WithContext(ctx))

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var body Response
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, w.Body.String())
	}
	if body.RefusalReason != RefusalContextRequired {
		t.Fatalf("refusal_reason = %q, want %q", body.RefusalReason, RefusalContextRequired)
	}
	if body.Answer != refusalAnswer(RefusalContextRequired) {
		t.Fatalf("answer = %q, want the context-required sentence", body.Answer)
	}
	// A refusal must never carry evidence: sources here would be the very
	// candidates the ungated path used to answer from.
	for name, sources := range map[string][]SourceContext{
		"sources":           body.Sources,
		"retrieved_sources": body.RetrievedSources,
		"citations":         body.Citations,
	} {
		if len(sources) != 0 {
			t.Fatalf("%s = %d entries, want none on a refusal", name, len(sources))
		}
	}
}

// The exemption is what keeps retrieval-only diagnostics (and the capacity
// runs that use them) able to ask a question containing a marker and still get
// candidates back.
func TestUnresolvedReferenceGateApplies(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		req  Request
		want bool
	}{
		{"normal answer request", config.Config{}, Request{}, true},
		{
			"retrieval-only with diagnostics is exempt",
			config.Config{RetrievalDiagnosticsEnabled: true},
			Request{RetrievalOnly: true},
			false,
		},
		{
			"retrieval-only without diagnostics is a normal request",
			config.Config{RetrievalDiagnosticsEnabled: false},
			Request{RetrievalOnly: true},
			true,
		},
		{
			"diagnostics enabled but not retrieval-only",
			config.Config{RetrievalDiagnosticsEnabled: true},
			Request{RetrievalOnly: false},
			true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := unresolvedReferenceGateApplies(tc.cfg, tc.req); got != tc.want {
				t.Fatalf("unresolvedReferenceGateApplies() = %v, want %v", got, tc.want)
			}
		})
	}
}

// Every reason a caller can see must have a sentence. Adding a reason without
// one would ship an empty answer, which is what the map's own comment forbids.
func TestRefusalContextRequiredHasASentence(t *testing.T) {
	if _, ok := refusalAnswers[RefusalContextRequired]; !ok {
		t.Fatalf("refusalAnswers has no entry for %q", RefusalContextRequired)
	}
	if refusalAnswer(RefusalContextRequired) == NoEvidenceAnswer {
		t.Fatalf("context_required falls back to the generic sentence, so a caller cannot tell why")
	}
}
