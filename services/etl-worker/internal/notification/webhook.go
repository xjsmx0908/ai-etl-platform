package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// WebhookDispatcher POSTs sanitized events to an internal or external webhook.
type WebhookDispatcher struct {
	URL     string
	Token   string
	Timeout time.Duration
	Client  *http.Client
}

type webhookBody struct {
	EventID    string            `json:"event_id"`
	EventType  string            `json:"event_type"`
	TenantID   string            `json:"tenant_id"`
	Source     string            `json:"source"`
	SourceID   string            `json:"source_id"`
	OccurredAt time.Time         `json:"occurred_at"`
	Payload    map[string]string `json:"payload"`
}

func (d WebhookDispatcher) Dispatch(ctx context.Context, event Event) error {
	url := strings.TrimSpace(d.URL)
	if url == "" {
		return ErrSkipped
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := d.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	body, err := json.Marshal(webhookBody{
		EventID:    event.ID,
		EventType:  event.Type,
		TenantID:   event.TenantID,
		Source:     event.Source,
		SourceID:   event.SourceID,
		OccurredAt: event.CreatedAt.UTC(),
		Payload:    SanitizePayload(event.Payload),
	})
	if err != nil {
		return fmt.Errorf("encode notification webhook: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(d.Token); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("notification webhook status %d", resp.StatusCode)
}

// SkipDispatcher closes events without calling an external system.
type SkipDispatcher struct{}

func (SkipDispatcher) Dispatch(context.Context, Event) error { return ErrSkipped }
