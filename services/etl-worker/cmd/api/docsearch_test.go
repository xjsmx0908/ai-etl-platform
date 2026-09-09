package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/indexmanifest"
	"ai-etl-pipeline/internal/retrieval"
	"ai-etl-pipeline/internal/store"
)

type documentSearchVisibilityStub struct {
	visible map[string]bool
	err     error
	count   int
}

func (s documentSearchVisibilityStub) ResolveVisibility(_ context.Context, _ string, refs []indexmanifest.GenerationReference) ([]bool, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.count > 0 {
		return make([]bool, s.count), nil
	}
	result := make([]bool, len(refs))
	for i, ref := range refs {
		result[i] = s.visible[ref.GenerationID]
	}
	return result, nil
}

type fakeChunkLister struct {
	chunks      map[string][]store.StoredChunk
	err         error
	lastTenant  string
	lastDoc     string
	lastAllowed []string
}

func (f *fakeChunkLister) ListChunksByDoc(_ context.Context, tenantID, docID string, allowed []string) ([]store.StoredChunk, error) {
	f.lastTenant = tenantID
	f.lastDoc = docID
	f.lastAllowed = allowed
	if f.err != nil {
		return nil, f.err
	}
	return f.chunks[docID], nil
}

type fakeDocumentSearcher struct {
	candidates []retrieval.Candidate
	err        error
	lastReq    retrieval.SearchRequest
}

func (f *fakeDocumentSearcher) Search(_ context.Context, req retrieval.SearchRequest) ([]retrieval.Candidate, error) {
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return f.candidates, nil
}

func TestHandleDocumentChunks_Success(t *testing.T) {
	docs := newFakeDocStore()
	seedDoc(docs, "acme", "d1", "internal")
	lister := &fakeChunkLister{chunks: map[string][]store.StoredChunk{
		"d1": {
			{ChunkID: "c1", DocID: "d1", TenantID: "acme", Content: "first", Index: 0, Permission: "internal"},
			{ChunkID: "c2", DocID: "d1", TenantID: "acme", Content: "second", Index: 1, Permission: "internal", Metadata: map[string]string{"order": "A"}},
		},
	}}
	handler := handleDocumentChunks(docs, lister, testQueryService())

	req := httptest.NewRequest(http.MethodGet, "/v1/documents/d1/chunks", nil)
	req = req.WithContext(ctxWithRole("acme", "user"))
	req.SetPathValue("docID", "d1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		DocID string `json:"doc_id"`
		Total int    `json:"total"`
		Items []struct {
			ChunkID  string            `json:"chunk_id"`
			Index    int               `json:"index"`
			Content  string            `json:"content"`
			Metadata map[string]string `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Total != 2 || len(resp.Items) != 2 {
		t.Fatalf("expected 2 chunks, got total=%d items=%d", resp.Total, len(resp.Items))
	}
	if lister.lastTenant != "acme" || lister.lastDoc != "d1" {
		t.Fatalf("expected tenant acme doc d1, got %q %q", lister.lastTenant, lister.lastDoc)
	}
	if !reflect.DeepEqual(lister.lastAllowed, []string{"public", "internal"}) {
		t.Fatalf("expected allowed [public internal], got %v", lister.lastAllowed)
	}
}

func TestHandleDocumentChunksFiltersUnpublishedReplacementGeneration(t *testing.T) {
	docs := newFakeDocStore()
	seedDoc(docs, "acme", "d1", "internal")
	lister := &fakeChunkLister{chunks: map[string][]store.StoredChunk{
		"d1": {
			{ChunkID: "old", DocID: "d1", TenantID: "acme", DocumentVersionID: "job-old", GenerationID: "gen-published", Content: "80 yuan", Index: 0},
			{ChunkID: "new", DocID: "d1", TenantID: "acme", DocumentVersionID: "job-new", GenerationID: "gen-replacement", Content: "120 yuan", Index: 0},
		},
	}}
	handler := handleDocumentChunks(docs, lister, testQueryService(),
		documentSearchVisibilityStub{visible: map[string]bool{"gen-published": true}})
	req := httptest.NewRequest(http.MethodGet, "/v1/documents/d1/chunks", nil)
	req = req.WithContext(ctxWithRole("acme", "user"))
	req.SetPathValue("docID", "d1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "120 yuan") || !strings.Contains(rec.Body.String(), "80 yuan") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleDocumentChunks_NotFoundForCrossTenantRoleOrMissing(t *testing.T) {
	docs := newFakeDocStore()
	seedDoc(docs, "acme", "d1", "internal")
	lister := &fakeChunkLister{}
	handler := handleDocumentChunks(docs, lister, testQueryService())

	cases := []struct {
		name   string
		tenant string
		role   string
		docID  string
	}{
		{"cross tenant", "other", "user", "d1"},
		{"role not allowed", "acme", "readonly", "d1"},
		{"missing doc", "acme", "user", "ghost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/documents/"+tc.docID+"/chunks", nil)
			req = req.WithContext(ctxWithRole(tc.tenant, tc.role))
			req.SetPathValue("docID", tc.docID)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected 404, got %d", rec.Code)
			}
		})
	}
}

func TestHandleDocumentChunks_StoreError500(t *testing.T) {
	docs := newFakeDocStore()
	seedDoc(docs, "acme", "d1", "internal")
	handler := handleDocumentChunks(docs, &fakeChunkLister{err: context.DeadlineExceeded}, testQueryService())

	req := httptest.NewRequest(http.MethodGet, "/v1/documents/d1/chunks", nil)
	req = req.WithContext(ctxWithRole("acme", "user"))
	req.SetPathValue("docID", "d1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
}

func TestHandleDocumentSearch_SuccessAggregatesAndEnriches(t *testing.T) {
	docs := newFakeDocStore()
	seedDoc(docs, "acme", "docA", "internal")
	seedDoc(docs, "acme", "docB", "public")
	searcher := &fakeDocumentSearcher{candidates: []retrieval.Candidate{
		{DocID: "docA", Content: "pipeline chunk one", Score: 0.9},
		{DocID: "docA", Content: "pipeline chunk two", Score: 0.5},
		{DocID: "docB", Content: "unrelated", Score: 0.7},
	}}
	handler := handleDocumentSearch(config.Config{RetrievalEnableES: true}, docs, searcher, testQueryService())

	req := httptest.NewRequest(http.MethodGet, "/v1/documents/search?q=pipeline", nil)
	req = req.WithContext(ctxWithRole("acme", "user"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Items []struct {
			DocID      string  `json:"doc_id"`
			FileName   string  `json:"file_name"`
			Permission string  `json:"permission"`
			HitCount   int     `json:"hit_count"`
			Snippet    string  `json:"snippet"`
			BestScore  float64 `json:"best_score"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(resp.Items))
	}
	// docA has the highest best score and must sort first.
	if resp.Items[0].DocID != "docA" || resp.Items[0].BestScore != 0.9 {
		t.Fatalf("expected docA first with 0.9, got %v", resp.Items[0])
	}
	if resp.Items[0].HitCount != 2 {
		t.Fatalf("expected docA hit_count 2, got %d", resp.Items[0].HitCount)
	}
	if resp.Items[0].FileName != "docA.pdf" || resp.Items[0].Permission != "internal" {
		t.Fatalf("expected enriched file_name/permission, got %v", resp.Items[0])
	}
	if searcher.lastReq.TenantID != "acme" {
		t.Fatalf("expected tenant acme, got %q", searcher.lastReq.TenantID)
	}
	if !reflect.DeepEqual(searcher.lastReq.AllowedPermissions, []string{"public", "internal"}) {
		t.Fatalf("expected allowed [public internal], got %v", searcher.lastReq.AllowedPermissions)
	}
}

func TestHandleDocumentSearchFiltersUnpublishedReplacementGeneration(t *testing.T) {
	docs := newFakeDocStore()
	seedDoc(docs, "acme", "docA", "internal")
	searcher := &fakeDocumentSearcher{candidates: []retrieval.Candidate{
		{DocID: "docA", DocumentVersionID: "job-old", GenerationID: "gen-published", Content: "approved", Score: 0.8},
		{DocID: "docA", DocumentVersionID: "job-new", GenerationID: "gen-replacement", Content: "unapproved", Score: 0.9},
	}}
	handler := handleDocumentSearch(config.Config{RetrievalEnableES: true}, docs, searcher, testQueryService(),
		documentSearchVisibilityStub{visible: map[string]bool{"gen-published": true}})
	req := httptest.NewRequest(http.MethodGet, "/v1/documents/search?q=policy", nil)
	req = req.WithContext(ctxWithRole("acme", "user"))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "unapproved") || !strings.Contains(rr.Body.String(), "approved") {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleDocumentSearchFailsClosedWhenReleaseVisibilityUnavailable(t *testing.T) {
	searcher := &fakeDocumentSearcher{candidates: []retrieval.Candidate{{
		DocID: "docA", DocumentVersionID: "job-1", GenerationID: "gen-1", Content: "must not leak",
	}}}
	for _, visibility := range []documentSearchVisibilityStub{
		{err: context.DeadlineExceeded},
		{count: 2},
	} {
		handler := handleDocumentSearch(config.Config{RetrievalEnableES: true}, newFakeDocStore(), searcher, testQueryService(), visibility)
		req := httptest.NewRequest(http.MethodGet, "/v1/documents/search?q=policy", nil)
		req = req.WithContext(ctxWithRole("acme", "user"))
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusServiceUnavailable || strings.Contains(rr.Body.String(), "must not leak") {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	}
}

func TestHandleDocumentSearch_EmptyQuery400(t *testing.T) {
	handler := handleDocumentSearch(config.Config{RetrievalEnableES: true}, newFakeDocStore(), &fakeDocumentSearcher{}, testQueryService())
	req := httptest.NewRequest(http.MethodGet, "/v1/documents/search?q=", nil)
	req = req.WithContext(ctxWithRole("acme", "user"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleDocumentSearch_ESDisabled503(t *testing.T) {
	handler := handleDocumentSearch(config.Config{RetrievalEnableES: false}, newFakeDocStore(), &fakeDocumentSearcher{}, testQueryService())
	req := httptest.NewRequest(http.MethodGet, "/v1/documents/search?q=pipeline", nil)
	req = req.WithContext(ctxWithRole("acme", "user"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}
