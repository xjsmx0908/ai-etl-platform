package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/idempotency"
	"ai-etl-pipeline/internal/ingestion"
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/query"
)

func TestValidateUploadExtension(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		wantExt  string
		wantErr  bool
	}{
		{name: "pdf allowed", filename: "report.pdf", wantExt: ".pdf"},
		{name: "uppercase allowed", filename: "REPORT.DOCX", wantExt: ".docx"},
		{name: "legacy spreadsheet allowed", filename: "accounts.xls", wantExt: ".xls"},
		{name: "spreadsheet allowed", filename: "policy.xlsx", wantExt: ".xlsx"},
		{name: "presentation allowed", filename: "training.pptx", wantExt: ".pptx"},
		{name: "unsupported rejected", filename: "malware.exe", wantErr: true},
		{name: "missing extension rejected", filename: "noext", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateUploadExtension(tc.filename)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantExt {
				t.Fatalf("expected %q, got %q", tc.wantExt, got)
			}
		})
	}
}

func TestApplyUploadScopeDefaultsAndPreservesExplicitValues(t *testing.T) {
	got, err := applyUploadScope(map[string]string{}, "", "", "user")
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if got["knowledge_base_id"] != "user-uploads" || got["applicable_scope"] != "organization" {
		t.Fatalf("unexpected defaults: %+v", got)
	}

	got, err = applyUploadScope(map[string]string{
		"knowledge_base_id": "enterprise-demo",
		"applicable_scope":  "demo",
	}, "", "", "admin")
	if err != nil {
		t.Fatalf("explicit metadata: %v", err)
	}
	if got["knowledge_base_id"] != "enterprise-demo" || got["applicable_scope"] != "demo" {
		t.Fatalf("explicit scope lost: %+v", got)
	}

	if _, err := applyUploadScope(map[string]string{}, "enterprise-demo", "demo", "user"); err == nil {
		t.Fatal("expected non-admin custom scope to be rejected")
	}
}

func TestNormalizePermission(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "default internal", input: "", want: "internal"},
		{name: "trim and normalize", input: "  PUBLIC ", want: "public"},
		{name: "internal allowed", input: "internal", want: "internal"},
		{name: "confidential allowed", input: "confidential", want: "confidential"},
		{name: "invalid rejected", input: "root", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizePermission(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestReadIdempotencyKey(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/upload", nil)
	req.Header.Set("Idempotency-Key", "plain-key")
	if got := readIdempotencyKey(req); got != "plain-key" {
		t.Fatalf("expected plain header key, got %q", got)
	}

	req2 := httptest.NewRequest("POST", "/v1/upload", nil)
	req2.Header.Set("Idempotency-Key", "plain-key")
	req2.Header.Set("X-Idempotency-Key", "x-key")
	if got := readIdempotencyKey(req2); got != "x-key" {
		t.Fatalf("expected X-Idempotency-Key to take precedence, got %q", got)
	}
}

func TestRequireScopes(t *testing.T) {
	handler := requireScopes("agent", "query")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/agent/runs", nil)
	ctx := context.WithValue(req.Context(), auth.CtxScopes, []string{"agent", "query"})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req.WithContext(ctx))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected allowed request, got %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/agent/runs", nil)
	ctx = context.WithValue(req.Context(), auth.CtxScopes, []string{"agent"})
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req.WithContext(ctx))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden request, got %d", rr.Code)
	}
}

func TestNewPlatformAuthenticatorUsesProductionIdentityPolicy(t *testing.T) {
	store := newFakeUserStore()
	verifier := newPlatformAuthenticator(config.Config{
		Environment: "production",
		JWTSecret:   "test-secret",
	}, store)
	token, err := auth.GenerateTestToken("test-secret", "forged", "offline", []string{auth.ScopeAdmin})
	if err != nil {
		t.Fatalf("GenerateTestToken: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/audit", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	auth.Middleware(verifier)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("production test identity reached protected handler")
	})).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", recorder.Code)
	}
}

func TestNewPlatformAuthenticatorAllowsFederatedSessionWhenOIDCEnabledInStaging(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "unused", "admin", "acme", true)
	user, found, err := store.GetByUsername(context.Background(), "alice")
	if err != nil || !found {
		t.Fatalf("load user: found=%v err=%v", found, err)
	}
	const secret = "test-secret-0123456789abcdef"
	token, _, err := auth.IssueFederatedToken(secret, user.ID, user.Username, user.Role, user.TenantID, user.TokenVersion)
	if err != nil {
		t.Fatalf("issue federated token: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/auth/session", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	auth.Middleware(newPlatformAuthenticator(config.Config{
		Environment: "staging", JWTSecret: secret, OIDCEnabled: true,
	}, store))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("expected federated staging session to pass, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestBuildUploadRequestSignature(t *testing.T) {
	a := buildUploadRequestSignature("tenant-a", "Report.PDF", 1024, "application/pdf", "internal", map[string]string{"contract_no": "CN-2026-0001"})
	b := buildUploadRequestSignature("tenant-a", "report.pdf", 1024, "application/pdf", "internal", map[string]string{"contract_no": "CN-2026-0001"})
	if a != b {
		t.Fatalf("expected normalized signatures to match, got %q != %q", a, b)
	}
	c := buildUploadRequestSignature("tenant-a", "report.pdf", 1024, "application/pdf", "internal", map[string]string{"contract_no": "CN-2026-0002"})
	if a == c {
		t.Fatal("expected metadata changes to affect upload signature")
	}
}

func TestExtendedUploadSignatureIncludesIdentityAndGovernance(t *testing.T) {
	base := buildUploadRequestSignature("tenant-a", "policy.pdf", 10, "application/pdf", "internal", nil)
	a := extendUploadRequestSignature(base, "policy-v2", uploadGovernance{Owner: "legal", Supersedes: "policy-v1"})
	b := extendUploadRequestSignature(base, "policy-v2", uploadGovernance{Owner: "finance", Supersedes: "policy-v1"})
	c := extendUploadRequestSignature(base, "policy-v3", uploadGovernance{Owner: "legal", Supersedes: "policy-v1"})
	if a == b || a == c {
		t.Fatalf("durable signatures must differ: a=%s b=%s c=%s", a, b, c)
	}
}

func TestConfig_DefaultMultipartMemory(t *testing.T) {
	cfg := config.Load()
	if cfg.MultipartMaxMemoryBytes != 4*1024*1024 {
		t.Fatalf("expected default multipart memory to be 4MB, got %d", cfg.MultipartMaxMemoryBytes)
	}
}

type noopProducer struct{}

func (noopProducer) Publish(context.Context, model.Task) error { return nil }

type unavailableProducer struct{}

func (unavailableProducer) Publish(context.Context, model.Task) error {
	return errors.New("kafka unavailable")
}

type noopObjectStore struct{}

func (noopObjectStore) Upload(context.Context, string, io.Reader, int64, string) error { return nil }
func (noopObjectStore) DeleteByPrefix(context.Context, string) error                   { return nil }

type operationsReaderStub struct {
	snapshot ingestion.OperationsSnapshot
	called   chan struct{}
}

func (s *operationsReaderStub) OperationsSnapshot(context.Context) (ingestion.OperationsSnapshot, error) {
	select {
	case s.called <- struct{}{}:
	default:
	}
	return s.snapshot, nil
}

type operationsObserverStub struct {
	observed chan ingestion.OperationsSnapshot
}

func (s *operationsObserverStub) SetIngestionOperations(pending, retried int, oldest time.Duration, jobs map[string]int, expired int) {
	s.observed <- ingestion.OperationsSnapshot{
		PendingOutbox: pending, RetriedOutbox: retried, OldestOutboxAge: oldest,
		Jobs: jobs, ExpiredProcessingLeases: expired,
	}
}

func TestIngestionOperationsMonitorPublishesPostgresSnapshot(t *testing.T) {
	want := ingestion.OperationsSnapshot{
		PendingOutbox: 4, RetriedOutbox: 2, OldestOutboxAge: 90 * time.Second,
		Jobs: map[string]int{"processing": 3}, ExpiredProcessingLeases: 1,
	}
	reader := &operationsReaderStub{snapshot: want, called: make(chan struct{}, 1)}
	observer := &operationsObserverStub{observed: make(chan ingestion.OperationsSnapshot, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runIngestionOperationsMonitor(ctx, reader, observer, time.Hour)
		close(done)
	}()

	select {
	case got := <-observer.observed:
		if got.PendingOutbox != want.PendingOutbox || got.RetriedOutbox != want.RetriedOutbox ||
			got.OldestOutboxAge != want.OldestOutboxAge || got.ExpiredProcessingLeases != want.ExpiredProcessingLeases {
			t.Fatalf("observed snapshot = %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("monitor did not publish its initial snapshot")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor did not stop after cancellation")
	}
}

func testUploadConfig() config.Config {
	return config.Config{MaxUploadSize: 1 << 20, MultipartMaxMemoryBytes: 64 << 10}
}

// testQueryService returns a query service for handler tests. Its retrieval
// cache falls back to NoopCache when no Redis is reachable, so the upload/delete
// handlers' cache-invalidation call is a safe no-op in unit tests.
func testQueryService() *query.Service {
	return query.NewService(testUploadConfig())
}

type noopIdempotencyStore struct{}

func (noopIdempotencyStore) Reserve(context.Context, string, string, string) (idempotency.ReserveResult, error) {
	return idempotency.ReserveResult{State: idempotency.ReserveNew}, nil
}

func (noopIdempotencyStore) Complete(context.Context, string, string, string, idempotency.CachedResponse) error {
	return nil
}

func (noopIdempotencyStore) Abort(context.Context, string, string, string) error { return nil }

func (noopIdempotencyStore) Close() error { return nil }

type captureTaskStatusStore struct {
	statuses []model.TaskStatus
}

func (s *captureTaskStatusStore) Save(_ context.Context, status model.TaskStatus) error {
	s.statuses = append(s.statuses, status)
	return nil
}

func (s *captureTaskStatusStore) Load(context.Context, string, string) (model.TaskStatus, bool, error) {
	return model.TaskStatus{}, false, nil
}

func (s *captureTaskStatusStore) Close() error { return nil }

func TestHandleUploadRequiresTenantContext(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/upload", nil)
	rr := httptest.NewRecorder()

	handler := handleUpload(testUploadConfig(), testQueryService(), noopProducer{}, noopObjectStore{}, noopIdempotencyStore{}, nil, nil, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != 401 {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestHandleUploadRecordsQueuedTaskStatus(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "status.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("hello status")); err != nil {
		t.Fatalf("write file body: %v", err)
	}
	if err := writer.WriteField("permission", "internal"); err != nil {
		t.Fatalf("write permission: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	statusStore := &captureTaskStatusStore{}
	req := httptest.NewRequest("POST", "/v1/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx := context.WithValue(req.Context(), auth.CtxTenantID, "tenant-a")
	ctx = context.WithValue(ctx, auth.CtxPermission, "user")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler := handleUpload(testUploadConfig(), testQueryService(), noopProducer{}, noopObjectStore{}, noopIdempotencyStore{}, statusStore, nil, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected accepted upload, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(statusStore.statuses) != 1 {
		t.Fatalf("expected one status save, got %+v", statusStore.statuses)
	}
	status := statusStore.statuses[0]
	if status.TenantID != "tenant-a" || status.TaskID == "" || status.Status != model.TaskStatusQueued || status.Stage != "queued" {
		t.Fatalf("unexpected queued status: %+v", status)
	}
}

// countingProducer records how many tasks were enqueued so tests can assert a
// rejected upload never reached Kafka.
type countingProducer struct{ published []model.Task }

func (p *countingProducer) Publish(_ context.Context, task model.Task) error {
	p.published = append(p.published, task)
	return nil
}

// pruningObjectStore records prefix wipes, including the spared key, so upsert
// ordering can be asserted.
type pruningObjectStore struct {
	uploaded      []string
	prunedExcept  [][2]string
	deletedPrefix []string
}

func (s *pruningObjectStore) Upload(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	// Drain the reader so the handler's TeeReader produces the real hash.
	_, _ = io.Copy(io.Discard, r)
	s.uploaded = append(s.uploaded, key)
	return nil
}

func (s *pruningObjectStore) DeleteByPrefix(_ context.Context, prefix string) error {
	s.deletedPrefix = append(s.deletedPrefix, prefix)
	return nil
}

func (s *pruningObjectStore) DeleteByPrefixExcept(_ context.Context, prefix, keepKey string) error {
	s.prunedExcept = append(s.prunedExcept, [2]string{prefix, keepKey})
	return nil
}

// uploadRequest builds a multipart upload request as the given role.
func uploadRequest(t *testing.T, filename, content, permission, docID, tenantID, userID, role string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatalf("write file body: %v", err)
	}
	if permission != "" {
		if err := writer.WriteField("permission", permission); err != nil {
			t.Fatalf("write permission: %v", err)
		}
	}
	if docID != "" {
		if err := writer.WriteField("doc_id", docID); err != nil {
			t.Fatalf("write doc_id: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest("POST", "/v1/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx := context.WithValue(req.Context(), auth.CtxTenantID, tenantID)
	ctx = context.WithValue(ctx, auth.CtxUserID, userID)
	ctx = context.WithValue(ctx, auth.CtxPermission, role)
	return req.WithContext(ctx)
}

// governanceUploadRequest is uploadRequest plus the controlled-document fields,
// so the governance write path can be exercised without a live database.
func governanceUploadRequest(t *testing.T, docID, tenantID, userID, role string, fields map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "policy.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte("差旅住宿标准：一线城市每晚上限八百元。")); err != nil {
		t.Fatalf("write file body: %v", err)
	}
	if err := writer.WriteField("permission", "internal"); err != nil {
		t.Fatalf("write permission: %v", err)
	}
	if docID != "" {
		if err := writer.WriteField("doc_id", docID); err != nil {
			t.Fatalf("write doc_id: %v", err)
		}
	}
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			t.Fatalf("write %s: %v", k, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest("POST", "/v1/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx := context.WithValue(req.Context(), auth.CtxTenantID, tenantID)
	ctx = context.WithValue(ctx, auth.CtxUserID, userID)
	ctx = context.WithValue(ctx, auth.CtxPermission, role)
	return req.WithContext(ctx)
}

// Classification must be authorized: a non-admin may not label a document
// confidential, since the read path would then hide it from its own uploader.
func TestHandleUploadRejectsConfidentialFromNonAdmin(t *testing.T) {
	for _, role := range []string{"user", "readonly", ""} {
		t.Run("role="+role, func(t *testing.T) {
			producer := &countingProducer{}
			req := uploadRequest(t, "secret.txt", "payroll", "confidential", "", "tenant-a", "u1", role)
			rr := httptest.NewRecorder()

			handler := handleUpload(testUploadConfig(), testQueryService(), producer, noopObjectStore{}, noopIdempotencyStore{}, nil, nil, nil)
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusForbidden {
				t.Fatalf("expected 403 for role %q, got %d body=%s", role, rr.Code, rr.Body.String())
			}
			if len(producer.published) != 0 {
				t.Fatalf("rejected upload must not enqueue a task, got %+v", producer.published)
			}
		})
	}
}

func TestHandleUploadAllowsConfidentialFromAdmin(t *testing.T) {
	producer := &countingProducer{}
	req := uploadRequest(t, "secret.txt", "payroll", "confidential", "", "tenant-a", "admin1", "admin")
	rr := httptest.NewRecorder()

	handler := handleUpload(testUploadConfig(), testQueryService(), producer, noopObjectStore{}, noopIdempotencyStore{}, nil, nil, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(producer.published) != 1 || producer.published[0].Permission != "confidential" {
		t.Fatalf("expected one confidential task, got %+v", producer.published)
	}
}

// A user must not be able to overwrite (and thereby cascade-delete) a document
// it cannot even read.
func TestHandleUploadRejectsReplacingConfidentialDocAsUser(t *testing.T) {
	docs := newFakeDocStore()
	if err := docs.Upsert(context.Background(), docstore.Document{
		TenantID: "tenant-a", DocID: "FIN-2024-001", Permission: "confidential",
		Status: docstore.StatusCompleted, UploadedBy: "admin1",
	}); err != nil {
		t.Fatalf("seed doc: %v", err)
	}

	producer := &countingProducer{}
	objects := &pruningObjectStore{}
	req := uploadRequest(t, "evil.txt", "overwrite", "internal", "FIN-2024-001", "tenant-a", "u1", "user")
	rr := httptest.NewRecorder()

	handler := handleUpload(testUploadConfig(), testQueryService(), producer, objects, noopIdempotencyStore{}, nil, docs, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(producer.published) != 0 || len(objects.uploaded) != 0 {
		t.Fatalf("rejected replace must not write anything: tasks=%+v objects=%+v", producer.published, objects.uploaded)
	}
	if len(objects.deletedPrefix) != 0 || len(objects.prunedExcept) != 0 {
		t.Fatalf("rejected replace must not delete anything: %+v %+v", objects.deletedPrefix, objects.prunedExcept)
	}
}

// Replacing someone else's document is denied even at a readable permission.
func TestHandleUploadRejectsReplacingOtherUsersDoc(t *testing.T) {
	docs := newFakeDocStore()
	if err := docs.Upsert(context.Background(), docstore.Document{
		TenantID: "tenant-a", DocID: "HR-2024-007", Permission: "internal",
		Status: docstore.StatusCompleted, UploadedBy: "someone-else",
	}); err != nil {
		t.Fatalf("seed doc: %v", err)
	}

	producer := &countingProducer{}
	req := uploadRequest(t, "mine.txt", "content", "internal", "HR-2024-007", "tenant-a", "u1", "user")
	rr := httptest.NewRecorder()

	handler := handleUpload(testUploadConfig(), testQueryService(), producer, noopObjectStore{}, noopIdempotencyStore{}, nil, docs, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(producer.published) != 0 {
		t.Fatalf("rejected replace must not enqueue: %+v", producer.published)
	}
	// The refusal must name the reason so the caller can act on it — a bare 403
	// would send a confused user (or frontend) in the wrong direction.
	if !strings.Contains(rr.Body.String(), "uploader") && !strings.Contains(rr.Body.String(), "admin") {
		t.Errorf("ownership refusal must say who may replace, got: %s", rr.Body.String())
	}
}

func TestHandleUploadAllowsReplacingOwnDoc(t *testing.T) {
	docs := newFakeDocStore()
	if err := docs.Upsert(context.Background(), docstore.Document{
		TenantID: "tenant-a", DocID: "mine-001", Permission: "internal",
		Status: docstore.StatusCompleted, UploadedBy: "u1",
	}); err != nil {
		t.Fatalf("seed doc: %v", err)
	}

	producer := &countingProducer{}
	objects := &pruningObjectStore{}
	req := uploadRequest(t, "mine.txt", "new content", "internal", "mine-001", "tenant-a", "u1", "user")
	rr := httptest.NewRecorder()

	handler := handleUpload(testUploadConfig(), testQueryService(), producer, objects, noopIdempotencyStore{}, nil, docs, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rr.Code, rr.Body.String())
	}
	// Old version retired only after the replacement was durably written, and the
	// new object must be spared from the prefix wipe.
	if len(objects.uploaded) != 1 {
		t.Fatalf("expected the replacement object to be written, got %+v", objects.uploaded)
	}
	if len(objects.prunedExcept) != 1 {
		t.Fatalf("expected one sparing prune, got %+v", objects.prunedExcept)
	}
	if got := objects.prunedExcept[0][1]; got != objects.uploaded[0] {
		t.Fatalf("prune must spare the new object %q, spared %q", objects.uploaded[0], got)
	}
	if len(objects.deletedPrefix) != 0 {
		t.Fatalf("upsert must not wipe the whole prefix, got %+v", objects.deletedPrefix)
	}
}

func TestHandleUploadRejectsReplacementPermissionChange(t *testing.T) {
	docs := newFakeDocStore()
	if err := docs.Upsert(context.Background(), docstore.Document{
		TenantID: "tenant-a", DocID: "mine-001", Permission: "confidential",
		KnowledgeSpaceID: "user-uploads", Status: docstore.StatusCompleted, UploadedBy: "admin1",
	}); err != nil {
		t.Fatal(err)
	}
	req := uploadRequest(t, "mine.txt", "new content", "internal", "mine-001", "tenant-a", "admin1", "admin")
	rr := httptest.NewRecorder()
	handler := handleUpload(testUploadConfig(), testQueryService(), &countingProducer{}, &pruningObjectStore{}, noopIdempotencyStore{}, nil, docs, nil)
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s, want replacement policy conflict", rr.Code, rr.Body.String())
	}
}

// A row backfilled by reconciliation has no uploader on record. Unknown
// ownership must not read as open ownership, or any user could claim every
// pre-registry document by guessing its doc_id.
func TestHandleUploadRejectsReplacingUnattributedDocAsUser(t *testing.T) {
	docs := newFakeDocStore()
	if err := docs.Upsert(context.Background(), docstore.Document{
		TenantID: "tenant-a", DocID: "LEGACY-001", Permission: "internal",
		Status: docstore.StatusCompleted, UploadedBy: "",
	}); err != nil {
		t.Fatalf("seed doc: %v", err)
	}

	producer := &countingProducer{}
	objects := &pruningObjectStore{}
	req := uploadRequest(t, "claim.txt", "content", "internal", "LEGACY-001", "tenant-a", "u1", "user")
	rr := httptest.NewRecorder()

	handler := handleUpload(testUploadConfig(), testQueryService(), producer, objects, noopIdempotencyStore{}, nil, docs, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(producer.published) != 0 || len(objects.uploaded) != 0 {
		t.Fatalf("rejected replace must not write: tasks=%+v objects=%+v", producer.published, objects.uploaded)
	}
}

// An admin may replace a document with no uploader on record — that is the
// escape hatch for the unattributed case above.
func TestHandleUploadAllowsAdminReplacingUnattributedDoc(t *testing.T) {
	docs := newFakeDocStore()
	if err := docs.Upsert(context.Background(), docstore.Document{
		TenantID: "tenant-a", DocID: "LEGACY-001", Permission: "internal",
		Status: docstore.StatusCompleted, UploadedBy: "",
	}); err != nil {
		t.Fatalf("seed doc: %v", err)
	}

	producer := &countingProducer{}
	req := uploadRequest(t, "fix.txt", "content", "internal", "LEGACY-001", "tenant-a", "admin1", "admin")
	rr := httptest.NewRecorder()

	handler := handleUpload(testUploadConfig(), testQueryService(), producer, &pruningObjectStore{}, noopIdempotencyStore{}, nil, docs, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// An admin may replace a document it did not upload.
func TestHandleUploadAllowsAdminReplacingOthersDoc(t *testing.T) {
	docs := newFakeDocStore()
	if err := docs.Upsert(context.Background(), docstore.Document{
		TenantID: "tenant-a", DocID: "FIN-2024-001", Permission: "confidential",
		Status: docstore.StatusCompleted, UploadedBy: "someone-else",
	}); err != nil {
		t.Fatalf("seed doc: %v", err)
	}

	producer := &countingProducer{}
	req := uploadRequest(t, "revised.txt", "revised", "confidential", "FIN-2024-001", "tenant-a", "admin1", "admin")
	rr := httptest.NewRecorder()

	handler := handleUpload(testUploadConfig(), testQueryService(), producer, &pruningObjectStore{}, noopIdempotencyStore{}, nil, docs, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rr.Code, rr.Body.String())
	}
}

// contentHash mirrors the handler's upload hash so tests can seed a registry row
// that a later upload of the same bytes must recognise as its own duplicate.
func contentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func decodeUploadResponse(t *testing.T, body *bytes.Buffer) uploadAcceptedResponse {
	t.Helper()
	var resp uploadAcceptedResponse
	if err := json.Unmarshal(body.Bytes(), &resp); err != nil {
		t.Fatalf("decode upload response %q: %v", body.String(), err)
	}
	return resp
}

// Identical bytes must not be ingested twice: a second copy would put duplicate
// chunks into the same candidate set, competing for Top-K slots that should hold
// distinct evidence.
func TestHandleUploadShortCircuitsDuplicateContent(t *testing.T) {
	const content = "季度营收 1.2 亿元"
	docs := newFakeDocStore()
	if err := docs.Upsert(context.Background(), docstore.Document{
		TenantID: "tenant-a", DocID: "FIN-2024-Q1", FileName: "营收.txt",
		ObjectKey: "tenant-a/FIN-2024-Q1.txt", FileHash: contentHash(content),
		Permission: "internal", Status: docstore.StatusCompleted, UploadedBy: "u1",
	}); err != nil {
		t.Fatalf("seed doc: %v", err)
	}

	producer := &countingProducer{}
	objects := &pruningObjectStore{}
	// Different file name, same bytes — identity is the content, not the name.
	req := uploadRequest(t, "营收-副本.txt", content, "internal", "", "tenant-a", "u1", "user")
	rr := httptest.NewRecorder()

	handler := handleUpload(testUploadConfig(), testQueryService(), producer, objects, noopIdempotencyStore{}, nil, docs, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for a duplicate, got %d body=%s", rr.Code, rr.Body.String())
	}
	resp := decodeUploadResponse(t, rr.Body)
	if resp.DuplicateOf != "FIN-2024-Q1" || resp.DocID != "FIN-2024-Q1" {
		t.Fatalf("expected the existing doc_id reported, got %+v", resp)
	}
	if resp.Status != "duplicate" {
		t.Fatalf("expected status=duplicate, got %q", resp.Status)
	}
	if len(producer.published) != 0 {
		t.Fatalf("duplicate must not enqueue an ETL task, got %+v", producer.published)
	}
	if len(objects.uploaded) != 0 {
		t.Fatalf("duplicate must not write a second object, got %+v", objects.uploaded)
	}
}

// An explicit doc_id is an explicit intent to replace that document, so the
// dedup short-circuit must not swallow a re-upload of unchanged bytes as a new
// version of the same file.
func TestHandleUploadSkipsDedupWhenDocIDSupplied(t *testing.T) {
	const content = "报销上限 5000 元"
	docs := newFakeDocStore()
	if err := docs.Upsert(context.Background(), docstore.Document{
		TenantID: "tenant-a", DocID: "HR-EXP-001", FileName: "报销标准.txt",
		FileHash: contentHash(content), Permission: "internal",
		Status: docstore.StatusCompleted, UploadedBy: "u1",
	}); err != nil {
		t.Fatalf("seed doc: %v", err)
	}

	producer := &countingProducer{}
	req := uploadRequest(t, "报销标准.txt", content, "internal", "HR-EXP-001", "tenant-a", "u1", "user")
	rr := httptest.NewRecorder()

	handler := handleUpload(testUploadConfig(), testQueryService(), producer, &pruningObjectStore{}, noopIdempotencyStore{}, nil, docs, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202 on the replace path, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(producer.published) != 1 {
		t.Fatalf("expected the replacement to be enqueued, got %+v", producer.published)
	}
}

// Same name, different content is a different document. Dedup keys on bytes only
// — collapsing by name would silently drop a revised file.
func TestHandleUploadIngestsSameNameDifferentContent(t *testing.T) {
	docs := newFakeDocStore()
	if err := docs.Upsert(context.Background(), docstore.Document{
		TenantID: "tenant-a", DocID: "POL-001", FileName: "差旅政策.txt",
		FileHash: contentHash("旧版：经济舱"), Permission: "internal",
		Status: docstore.StatusCompleted, UploadedBy: "u1",
	}); err != nil {
		t.Fatalf("seed doc: %v", err)
	}

	producer := &countingProducer{}
	req := uploadRequest(t, "差旅政策.txt", "新版：公务舱", "internal", "", "tenant-a", "u1", "user")
	rr := httptest.NewRecorder()

	handler := handleUpload(testUploadConfig(), testQueryService(), producer, &pruningObjectStore{}, noopIdempotencyStore{}, nil, docs, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(producer.published) != 1 {
		t.Fatalf("expected one task enqueued, got %+v", producer.published)
	}
	if got := producer.published[0].DocID; got == "POL-001" {
		t.Fatalf("expected a fresh doc_id, got the existing one %q", got)
	}
}

// A queued or failed row may never produce searchable chunks, so treating it as
// the canonical copy would leave the content unretrievable forever.
func TestHandleUploadIgnoresIncompleteDuplicate(t *testing.T) {
	const content = "尚在处理中的内容"
	docs := newFakeDocStore()
	if err := docs.Upsert(context.Background(), docstore.Document{
		TenantID: "tenant-a", DocID: "IN-FLIGHT", FileName: "a.txt",
		FileHash: contentHash(content), Permission: "internal",
		Status: docstore.StatusQueued, UploadedBy: "u1",
	}); err != nil {
		t.Fatalf("seed doc: %v", err)
	}

	producer := &countingProducer{}
	req := uploadRequest(t, "a.txt", content, "internal", "", "tenant-a", "u1", "user")
	rr := httptest.NewRecorder()

	handler := handleUpload(testUploadConfig(), testQueryService(), producer, &pruningObjectStore{}, noopIdempotencyStore{}, nil, docs, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(producer.published) != 1 {
		t.Fatalf("expected the upload to proceed, got %+v", producer.published)
	}
}

// The hash the ETL task carries must be the hash of the bytes that were stored:
// dedup, the registry row, and chunk provenance all key on it.
func TestHandleUploadPublishesContentHash(t *testing.T) {
	const content = "hash provenance"
	producer := &countingProducer{}
	req := uploadRequest(t, "h.txt", content, "internal", "", "tenant-a", "u1", "user")
	rr := httptest.NewRecorder()

	handler := handleUpload(testUploadConfig(), testQueryService(), producer, &pruningObjectStore{}, noopIdempotencyStore{}, nil, nil, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(producer.published) != 1 {
		t.Fatalf("expected one task, got %+v", producer.published)
	}
	if got, want := producer.published[0].FileHash, contentHash(content); got != want {
		t.Fatalf("expected file_hash %s, got %s", want, got)
	}
}

func TestHandleUploadAcceptsDurablyWhileKafkaIsUnavailable(t *testing.T) {
	admissions := ingestion.NewMemoryStore()
	req := uploadRequest(t, "policy.txt", "durable content", "internal", "", "tenant-a", "u1", "user")
	rr := httptest.NewRecorder()

	handler := handleUploadWithAdmission(testUploadConfig(), testQueryService(), unavailableProducer{},
		&pruningObjectStore{}, noopIdempotencyStore{}, nil, newFakeDocStore(), nil, admissions)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected durable 202, got %d body=%s", rr.Code, rr.Body.String())
	}
	var response uploadAcceptedResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.JobID == "" || response.EventID == "" {
		t.Fatalf("durable identifiers missing: %+v", response)
	}
	events, err := admissions.ClaimPending(context.Background(), 10, time.Minute)
	if err != nil || len(events) != 1 {
		t.Fatalf("pending outbox = %+v, err=%v", events, err)
	}
	if events[0].Task.FilePath != response.File || events[0].Task.JobID != response.JobID {
		t.Fatalf("persisted task = %+v, response=%+v", events[0].Task, response)
	}
}

func TestAdmissionIDsAreStableForIdempotentRetry(t *testing.T) {
	job1, event1 := admissionIDs("tenant-a", "request-42")
	job2, event2 := admissionIDs("tenant-a", "request-42")
	if job1 != job2 || event1 != event2 {
		t.Fatalf("stable IDs changed: (%s,%s) != (%s,%s)", job1, event1, job2, event2)
	}
	otherJob, _ := admissionIDs("tenant-b", "request-42")
	if otherJob == job1 {
		t.Fatal("tenant must be part of durable admission identity")
	}
}

func TestDurableUploadRetryKeepsOneAdmission(t *testing.T) {
	admissions := ingestion.NewMemoryStore()
	handler := handleUploadWithAdmission(testUploadConfig(), testQueryService(), unavailableProducer{},
		&pruningObjectStore{}, noopIdempotencyStore{}, nil, newFakeDocStore(), nil, admissions)
	var responses []uploadAcceptedResponse
	for range 2 {
		req := uploadRequest(t, "policy.txt", "durable content", "internal", "", "tenant-a", "u1", "user")
		req.Header.Set("Idempotency-Key", "request-42")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusAccepted {
			t.Fatalf("retry returned %d body=%s", rr.Code, rr.Body.String())
		}
		var response uploadAcceptedResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		responses = append(responses, response)
	}
	if responses[0].JobID != responses[1].JobID || responses[0].EventID != responses[1].EventID || responses[0].DocID != responses[1].DocID {
		t.Fatalf("retry changed durable identity: %+v vs %+v", responses[0], responses[1])
	}
	if got := admissions.PendingCount(); got != 1 {
		t.Fatalf("pending events = %d, want one durable admission", got)
	}
}

func TestCanWritePermission(t *testing.T) {
	cases := []struct {
		role, permission string
		want             bool
	}{
		{"admin", "confidential", true},
		{"admin", "internal", true},
		{"user", "internal", true},
		{"user", "public", true},
		{"user", "confidential", false},
		{"readonly", "public", true},
		{"readonly", "internal", false},
		{"", "internal", false},
		{"bogus", "confidential", false},
	}
	for _, c := range cases {
		if got := canWritePermission(c.role, c.permission); got != c.want {
			t.Errorf("canWritePermission(%q, %q) = %v, want %v", c.role, c.permission, got, c.want)
		}
	}
}

func TestHandleUploadRejectsOversizedMultipartBody(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "big.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(bytes.Repeat([]byte("a"), 2048)); err != nil {
		t.Fatalf("write file body: %v", err)
	}
	if err := writer.WriteField("permission", "internal"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest("POST", "/v1/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req = req.WithContext(context.WithValue(req.Context(), auth.CtxTenantID, "tenant-a"))

	rr := httptest.NewRecorder()
	handler := handleUpload(config.Config{MaxUploadSize: 1024, MultipartMaxMemoryBytes: 512}, testQueryService(), noopProducer{}, noopObjectStore{}, noopIdempotencyStore{}, nil, nil, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != 413 {
		t.Fatalf("expected 413 for oversized upload, got %d", rr.Code)
	}
	if got := rr.Body.String(); got == "" || !bytes.Contains([]byte(got), []byte("file too large")) {
		t.Fatalf("expected file-too-large message, got %q", got)
	}
}

// Governance fields decide whether a document counts as evidence at all, so
// writing them is admin-only — a stronger requirement than uploading content.
func TestHandleUploadPersistsGovernanceFieldsForAdmin(t *testing.T) {
	docs := newFakeDocStore()
	producer := &countingProducer{}
	req := governanceUploadRequest(t, "FIN-2024-002-v2", "tenant-a", "admin1", "admin", map[string]string{
		"effective_date": "2026-01-01",
		"supersedes":     "FIN-2024-002",
		"owner":          "财务部",
	})
	rr := httptest.NewRecorder()

	handler := handleUpload(testUploadConfig(), testQueryService(), producer, noopObjectStore{}, noopIdempotencyStore{}, nil, docs, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rr.Code, rr.Body.String())
	}
	got, found, err := docs.Get(context.Background(), "tenant-a", "FIN-2024-002-v2")
	if err != nil || !found {
		t.Fatalf("registry row missing: found=%v err=%v", found, err)
	}
	if got.Supersedes != "FIN-2024-002" {
		t.Errorf("supersedes = %q, want FIN-2024-002", got.Supersedes)
	}
	if got.Owner != "财务部" {
		t.Errorf("owner = %q, want 财务部", got.Owner)
	}
	if got.EffectiveDate.Format("2006-01-02") != "2026-01-01" {
		t.Errorf("effective_date = %q, want 2026-01-01", got.EffectiveDate.Format("2006-01-02"))
	}
	if got.DocStatus != docstore.DocStatusActive {
		t.Errorf("doc_status = %q, want active by default", got.DocStatus)
	}
}

// Rejected rather than silently ignored: a caller who believes they retired a
// document, but did not, is worse off than one who gets an explicit error.
func TestHandleUploadRejectsGovernanceFieldsFromNonAdmin(t *testing.T) {
	for _, field := range []string{"doc_status", "supersedes", "owner", "effective_date"} {
		t.Run(field, func(t *testing.T) {
			value := "superseded"
			switch field {
			case "supersedes":
				value = "FIN-2024-002"
			case "owner":
				value = "someone"
			case "effective_date":
				value = "2026-01-01"
			}
			producer := &countingProducer{}
			req := governanceUploadRequest(t, "", "tenant-a", "u1", "user", map[string]string{field: value})
			rr := httptest.NewRecorder()

			handler := handleUpload(testUploadConfig(), testQueryService(), producer, noopObjectStore{}, noopIdempotencyStore{}, nil, newFakeDocStore(), nil)
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 for %s from user, got %d body=%s", field, rr.Code, rr.Body.String())
			}
			if len(producer.published) != 0 {
				t.Fatalf("rejected upload must not enqueue a task, got %+v", producer.published)
			}
		})
	}
}

func TestHandleUploadValidatesGovernanceValues(t *testing.T) {
	cases := []struct {
		name   string
		docID  string
		fields map[string]string
	}{
		{"unknown doc_status", "", map[string]string{"doc_status": "retired"}},
		{"timestamp instead of date", "", map[string]string{"effective_date": "2026-01-01T00:00:00Z"}},
		{"self reference", "SELF-1", map[string]string{"supersedes": "SELF-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			producer := &countingProducer{}
			req := governanceUploadRequest(t, tc.docID, "tenant-a", "admin1", "admin", tc.fields)
			rr := httptest.NewRecorder()

			handler := handleUpload(testUploadConfig(), testQueryService(), producer, noopObjectStore{}, noopIdempotencyStore{}, nil, newFakeDocStore(), nil)
			handler.ServeHTTP(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

// A re-upload that omits governance fields must not wipe them: content and
// authority are separate concerns.
func TestHandleUploadPreservesGovernanceOnPlainReupload(t *testing.T) {
	docs := newFakeDocStore()
	if err := docs.Upsert(context.Background(), docstore.Document{
		TenantID: "tenant-a", DocID: "FIN-2024-002", Permission: "internal",
		Status: docstore.StatusCompleted, UploadedBy: "admin1",
		DocStatus: docstore.DocStatusSuperseded, Supersedes: "FIN-2023-002", Owner: "财务部",
	}); err != nil {
		t.Fatalf("seed doc: %v", err)
	}

	producer := &countingProducer{}
	req := uploadRequest(t, "v3.txt", "新版内容", "internal", "FIN-2024-002", "tenant-a", "admin1", "admin")
	rr := httptest.NewRecorder()

	handler := handleUpload(testUploadConfig(), testQueryService(), producer, &pruningObjectStore{}, noopIdempotencyStore{}, nil, docs, nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d body=%s", rr.Code, rr.Body.String())
	}
	got, _, err := docs.Get(context.Background(), "tenant-a", "FIN-2024-002")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DocStatus != docstore.DocStatusSuperseded {
		t.Errorf("doc_status = %q, want superseded preserved", got.DocStatus)
	}
	if got.Supersedes != "FIN-2023-002" {
		t.Errorf("supersedes = %q, want FIN-2023-002 preserved", got.Supersedes)
	}
	if got.Owner != "财务部" {
		t.Errorf("owner = %q, want 财务部 preserved", got.Owner)
	}
}
