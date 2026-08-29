package s3

import (
	"context"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

func TestListOlderThanReturnsVersionedCandidatesWithoutLegacyStarvation(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	client := &Client{
		bucket: "documents",
		listObjects: func(context.Context, string, minio.ListObjectsOptions) <-chan minio.ObjectInfo {
			objects := make(chan minio.ObjectInfo, 3)
			objects <- minio.ObjectInfo{Key: "tenant/legacy-a.txt", LastModified: now.Add(-72 * time.Hour)}
			objects <- minio.ObjectInfo{Key: "tenant/legacy-b.txt", LastModified: now.Add(-72 * time.Hour)}
			objects <- minio.ObjectInfo{Key: "tenant/doc/versions/orphan.txt", LastModified: now.Add(-48 * time.Hour)}
			close(objects)
			return objects
		},
	}

	got, err := client.ListOlderThan(context.Background(), now.Add(-24*time.Hour), 1)
	if err != nil {
		t.Fatalf("ListOlderThan: %v", err)
	}
	if len(got) != 1 || got[0].Key != "tenant/doc/versions/orphan.txt" {
		t.Fatalf("candidates = %+v, want the versioned orphan", got)
	}
}

func TestListOlderThanContinuesAfterPreviousBoundedBatch(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	keys := []string{
		"tenant/a/versions/referenced.txt",
		"tenant/b/versions/orphan.txt",
	}
	client := &Client{
		bucket: "documents",
		listObjects: func(_ context.Context, _ string, opts minio.ListObjectsOptions) <-chan minio.ObjectInfo {
			objects := make(chan minio.ObjectInfo, len(keys))
			for _, key := range keys {
				if key > opts.StartAfter {
					objects <- minio.ObjectInfo{Key: key, LastModified: now.Add(-48 * time.Hour)}
				}
			}
			close(objects)
			return objects
		},
	}

	first, err := client.ListOlderThan(context.Background(), now.Add(-24*time.Hour), 1)
	if err != nil || len(first) != 1 || first[0].Key != keys[0] {
		t.Fatalf("first batch = (%+v, %v), want %q", first, err, keys[0])
	}
	second, err := client.ListOlderThan(context.Background(), now.Add(-24*time.Hour), 1)
	if err != nil || len(second) != 1 || second[0].Key != keys[1] {
		t.Fatalf("second batch = (%+v, %v), want %q", second, err, keys[1])
	}
	third, err := client.ListOlderThan(context.Background(), now.Add(-24*time.Hour), 1)
	if err != nil || len(third) != 1 || third[0].Key != keys[0] {
		t.Fatalf("wrapped batch = (%+v, %v), want %q", third, err, keys[0])
	}
}
