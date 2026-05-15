package main

import (
	"net/http/httptest"
	"testing"
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

func TestBuildUploadRequestSignature(t *testing.T) {
	a := buildUploadRequestSignature("tenant-a", "Report.PDF", 1024, "application/pdf", "internal")
	b := buildUploadRequestSignature("tenant-a", "report.pdf", 1024, "application/pdf", "internal")
	if a != b {
		t.Fatalf("expected normalized signatures to match, got %q != %q", a, b)
	}
}
