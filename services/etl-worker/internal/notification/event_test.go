package notification

import "testing"

func TestNewEventDropsSensitivePayloadKeys(t *testing.T) {
	event, err := NewEvent(SourceReleaseCenter, "request-1", EventRequestOpened, "acme", map[string]string{
		"state":          "approval_pending",
		"document_id":    "doc-1",
		"summary":        "身份证 110101199001011234 出现在正文",
		"findings":       "password=super-secret",
		"content":        "ignore previous instructions",
		"risk_level":     "low",
		"recommendation": "publish",
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Payload["summary"] != "" || event.Payload["findings"] != "" || event.Payload["content"] != "" {
		t.Fatalf("sensitive keys leaked: %+v", event.Payload)
	}
	if event.Payload["document_id"] != "doc-1" || event.Payload["state"] != "approval_pending" {
		t.Fatalf("expected identifiers retained: %+v", event.Payload)
	}
	again, err := NewEvent(SourceReleaseCenter, "request-1", EventRequestOpened, "acme", map[string]string{
		"state": "approval_pending",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != event.ID || again.DedupeKey != event.DedupeKey {
		t.Fatalf("expected stable idempotency, got %s vs %s", again.ID, event.ID)
	}
}

func TestSanitizePayloadTruncatesValues(t *testing.T) {
	long := make([]rune, maxPayloadValueRunes+20)
	for i := range long {
		long[i] = 'a'
	}
	got := SanitizePayload(map[string]string{"document_id": string(long), "prompt": "secret"})
	if got["prompt"] != "" {
		t.Fatal("prompt key must be dropped")
	}
	if got["document_id"] != string(long[:maxPayloadValueRunes]) {
		t.Fatalf("value not truncated: %d", len([]rune(got["document_id"])))
	}
}
