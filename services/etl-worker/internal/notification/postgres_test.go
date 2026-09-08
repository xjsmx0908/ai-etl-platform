package notification

import (
	"context"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v5"
)

func TestPostgresStoreEnqueueUsesConflictNoOp(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	event, err := NewEvent(SourceReleaseCenter, "request-1", EventRequestOpened, "acme", map[string]string{
		"state": "approval_pending", "document_id": "doc-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec("INSERT INTO governance_notification_outbox").
		WithArgs(event.ID, event.DedupeKey, event.TenantID, event.Source, event.SourceID, event.Type, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := NewPostgresStore(mock).Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreClaimPending(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	rows := pgxmock.NewRows([]string{
		"event_id", "dedupe_key", "tenant_id", "source", "source_id", "event_type", "payload", "attempts", "created_at", "last_error",
	}).AddRow("ntf-1", "dedupe-1", "acme", SourceReleaseCenter, "request-1", EventRequestOpened, []byte(`{"state":"approval_pending","summary":"nope"}`), 1, time.Now().UTC(), "")
	mock.ExpectQuery("WITH candidates AS").WithArgs(10, time.Second.String()).WillReturnRows(rows)
	events, err := NewPostgresStore(mock).ClaimPending(context.Background(), 10, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Payload["summary"] != "" || events[0].Payload["state"] != "approval_pending" {
		t.Fatalf("events=%+v", events)
	}
}
