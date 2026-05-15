package query

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
)

func TestHandleQuery_UnauthorizedWithoutTenantContext(t *testing.T) {
	svc := NewService(config.Config{})
	req := httptest.NewRequest(http.MethodPost, "/v1/query", strings.NewReader(`{"question":"hello"}`))
	w := httptest.NewRecorder()

	svc.HandleQuery(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, w.Code)
	}
}

func TestHandleQuery_UsesTenantAndPermissionFromJWTContext(t *testing.T) {
	t.Setenv("LLM_ENDPOINT", "")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("LLM_MODEL", "test-llm")

	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer embedSrv.Close()

	var filterTenant string
	var permissionAny []string
	qdrantSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			filter, _ := body["filter"].(map[string]interface{})
			mustList, _ := filter["must"].([]interface{})
			for _, item := range mustList {
				cond, _ := item.(map[string]interface{})
				key, _ := cond["key"].(string)
				match, _ := cond["match"].(map[string]interface{})
				switch key {
				case "tenant_id":
					filterTenant, _ = match["value"].(string)
				case "permission":
					anyList, _ := match["any"].([]interface{})
					for _, v := range anyList {
						if s, ok := v.(string); ok {
							permissionAny = append(permissionAny, s)
						}
					}
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"points":[{"score":0.9,"payload":{"chunk_id":"c1","doc_id":"d1","content":"ctx","tenant_id":"tenant-from-jwt"}}]}}`))
	}))
	defer qdrantSrv.Close()

	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"answer"}}]}`))
	}))
	defer llmSrv.Close()
	t.Setenv("LLM_ENDPOINT", llmSrv.URL)

	cfg := config.Config{
		EmbedEndpoint:   embedSrv.URL,
		EmbedModel:      "test-embed",
		StoreEndpoint:   qdrantSrv.URL,
		StoreCollection: "docs",
		SparseK1:        1.2,
		SparseB:         0.75,
		SparseAvgDL:     256,
	}
	svc := NewService(cfg)

	reqBody := `{"question":"what is this","tenant_id":"attacker-tenant"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/query", strings.NewReader(reqBody))
	ctx := context.WithValue(req.Context(), auth.CtxTenantID, "tenant-from-jwt")
	ctx = context.WithValue(ctx, auth.CtxPermission, "user")
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	svc.HandleQuery(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d, body=%s", http.StatusOK, w.Code, w.Body.String())
	}
	if filterTenant != "tenant-from-jwt" {
		t.Fatalf("expected tenant filter %q, got %q", "tenant-from-jwt", filterTenant)
	}
	if !reflect.DeepEqual(permissionAny, []string{"public", "internal"}) {
		t.Fatalf("expected permission any %v, got %v", []string{"public", "internal"}, permissionAny)
	}
}

func TestAllowedDocumentPermissionsForRole(t *testing.T) {
	tests := []struct {
		name string
		role string
		want []string
	}{
		{name: "admin", role: "admin", want: []string{"public", "internal", "confidential"}},
		{name: "user", role: "user", want: []string{"public", "internal"}},
		{name: "readonly", role: "readonly", want: []string{"public"}},
		{name: "trim and case", role: " User ", want: []string{"public", "internal"}},
		{name: "unknown fallback", role: "guest", want: []string{"public"}},
		{name: "empty fallback", role: "", want: []string{"public"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := allowedDocumentPermissionsForRole(tc.role)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}
