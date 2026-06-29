package parser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ai-etl-pipeline/internal/model"
)

func TestParseFile_UsesInternalTokenFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	tokenPath := filepath.Join(tmpDir, "parser_internal_token")
	if err := os.WriteFile(tokenPath, []byte("file-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	filePath := filepath.Join(tmpDir, "doc.txt")
	if err := os.WriteFile(filePath, []byte("hello parser"), 0o600); err != nil {
		t.Fatalf("write temp doc: %v", err)
	}

	t.Setenv("PARSER_INTERNAL_TOKEN", "")
	t.Setenv("PARSER_INTERNAL_TOKEN_FILE", tokenPath)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Internal-Token"); got != "file-token" {
			t.Fatalf("expected X-Internal-Token=file-token, got %q", got)
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		if got := r.FormValue("metadata"); got != `{"contract_no":"CN-2026-0001"}` {
			t.Fatalf("expected metadata form field, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"doc_id":"d1","tenant_id":"t1","chunks":[{"chunk_id":"d1_0","doc_id":"d1","tenant_id":"t1","content":"hello","index":0,"metadata":{"contract_no":"CN-2026-0001"}}],"total_chunks":1,"parse_time_ms":1.2,"file_size_bytes":12,"status":"success"}`))
	}))
	defer srv.Close()

	client := NewClient(srv.URL)
	chunks, err := client.ParseFile(context.Background(), model.Task{
		DocID:    "d1",
		TenantID: "t1",
		FilePath: filePath,
		Metadata: map[string]string{"contract_no": "CN-2026-0001"},
	})
	if err != nil {
		t.Fatalf("ParseFile failed: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Metadata["contract_no"] != "CN-2026-0001" {
		t.Fatalf("expected metadata on chunk, got %+v", chunks[0].Metadata)
	}
}
