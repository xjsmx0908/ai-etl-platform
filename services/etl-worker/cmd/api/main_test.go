package main

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/idempotency"
	"ai-etl-pipeline/internal/model"
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

func TestConfig_DefaultMultipartMemory(t *testing.T) {
	cfg := config.Load()
	if cfg.MultipartMaxMemoryBytes != 4*1024*1024 {
		t.Fatalf("expected default multipart memory to be 4MB, got %d", cfg.MultipartMaxMemoryBytes)
	}
}

type noopProducer struct{}

func (noopProducer) Publish(context.Context, model.Task) error { return nil }

type noopObjectStore struct{}

func (noopObjectStore) Upload(context.Context, string, io.Reader, int64, string) error { return nil }

type noopIdempotencyStore struct{}

func (noopIdempotencyStore) Reserve(context.Context, string, string, string) (idempotency.ReserveResult, error) {
	return idempotency.ReserveResult{State: idempotency.ReserveNew}, nil
}

func (noopIdempotencyStore) Complete(context.Context, string, string, string, idempotency.CachedResponse) error {
	return nil
}

func (noopIdempotencyStore) Abort(context.Context, string, string, string) error { return nil }

func (noopIdempotencyStore) Close() error { return nil }

func TestHandleUploadRequiresTenantContext(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/upload", nil)
	rr := httptest.NewRecorder()

	handler := handleUpload(1024*1024, 64*1024, noopProducer{}, noopObjectStore{}, noopIdempotencyStore{})
	handler.ServeHTTP(rr, req)

	if rr.Code != 401 {
		t.Fatalf("expected 401, got %d", rr.Code)
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
	handler := handleUpload(1024, 512, noopProducer{}, noopObjectStore{}, noopIdempotencyStore{})
	handler.ServeHTTP(rr, req)

	if rr.Code != 413 {
		t.Fatalf("expected 413 for oversized upload, got %d", rr.Code)
	}
	if got := rr.Body.String(); got == "" || !bytes.Contains([]byte(got), []byte("file too large")) {
		t.Fatalf("expected file-too-large message, got %q", got)
	}
}
