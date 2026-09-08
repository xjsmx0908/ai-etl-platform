package notification

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRelayRetriesFailedDeliveryAndSucceedsLater(t *testing.T) {
	store := NewMemoryStore()
	event, err := NewEvent(SourceReleaseCenter, "request-1", EventRequestOpened, "acme", map[string]string{
		"state": "approval_pending", "document_id": "doc-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	failing := Relay{Store: store, Dispatcher: dispatcherFunc(func(context.Context, Event) error {
		return errors.New("webhook down")
	}), BatchSize: 10, Lease: time.Second}
	if published, err := failing.RunOnce(context.Background()); err != nil || published != 0 {
		t.Fatalf("published=%d err=%v", published, err)
	}
	if snapshot, _ := store.Snapshot(context.Background()); snapshot.Pending != 1 || snapshot.Retried != 1 {
		t.Fatalf("expected pending retry snapshot, got %+v", snapshot)
	}

	ok := Relay{Store: store, Dispatcher: dispatcherFunc(func(context.Context, Event) error { return nil }), BatchSize: 10, Lease: time.Second}
	if published, err := ok.RunOnce(context.Background()); err != nil || published != 1 {
		t.Fatalf("published=%d err=%v", published, err)
	}
	events := store.Events()
	if len(events) != 1 || events[0].PublishedAt == nil {
		t.Fatalf("expected published event, got %+v", events)
	}
}

func TestRelaySkipsUnconfiguredWebhook(t *testing.T) {
	store := NewMemoryStore()
	event, err := NewEvent(SourceReleaseCenter, "request-2", EventRequestOpened, "acme", map[string]string{"state": "manual_exception"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	relay := Relay{Store: store, Dispatcher: SkipDispatcher{}, BatchSize: 10, Lease: time.Second}
	if published, err := relay.RunOnce(context.Background()); err != nil || published != 1 {
		t.Fatalf("published=%d err=%v", published, err)
	}
}

func TestWebhookDispatcherPostsSanitizedBody(t *testing.T) {
	var gotPath string
	var gotAuth string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		defer r.Body.Close()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	event, err := NewEvent(SourceReleaseCenter, "request-3", EventRequestOpened, "acme", map[string]string{
		"state": "approval_pending", "summary": "should not appear",
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := WebhookDispatcher{URL: server.URL + "/notifications", Token: "relay-token", Timeout: time.Second}
	if err := dispatcher.Dispatch(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/notifications" || gotAuth != "Bearer relay-token" {
		t.Fatalf("path=%s auth=%s", gotPath, gotAuth)
	}
	payload, _ := gotBody["payload"].(map[string]any)
	if payload["summary"] != nil {
		t.Fatalf("webhook leaked summary: %+v", gotBody)
	}
}

func TestWebhookDispatcherEmptyURLSkipped(t *testing.T) {
	if err := (WebhookDispatcher{}).Dispatch(context.Background(), Event{ID: "x"}); !errors.Is(err, ErrSkipped) {
		t.Fatalf("err=%v", err)
	}
}

func TestEnqueueIsIdempotent(t *testing.T) {
	store := NewMemoryStore()
	first, err := NewEvent(SourceReleaseCenter, "request-4", EventRequestOpened, "acme", map[string]string{"state": "approval_pending"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewEvent(SourceReleaseCenter, "request-4", EventRequestOpened, "acme", map[string]string{"state": "approval_pending"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if got := store.Events(); len(got) != 1 {
		t.Fatalf("expected one event, got %d", len(got))
	}
}

type dispatcherFunc func(context.Context, Event) error

func (f dispatcherFunc) Dispatch(ctx context.Context, event Event) error { return f(ctx, event) }
