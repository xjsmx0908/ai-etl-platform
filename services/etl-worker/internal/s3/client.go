// Package s3 provides MinIO/S3 object storage for document uploads.
package s3

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"ai-etl-pipeline/internal/ingestion"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Client wraps MinIO client for document storage.
type Client struct {
	client      *minio.Client
	bucket      string
	region      string
	autoCreate  bool
	listObjects func(context.Context, string, minio.ListObjectsOptions) <-chan minio.ObjectInfo
	listMu      sync.Mutex
	listCursor  string
}

// Config holds MinIO connection settings.
type Config struct {
	Endpoint   string
	AccessKey  string
	SecretKey  string
	Bucket     string
	Region     string
	UseSSL     bool
	AutoCreate bool
}

// New creates a MinIO client and ensures bucket exists.
func New(cfg Config) (*Client, error) {
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("create minio client: %w", err)
	}

	ctx := context.Background()
	exists, err := mc.BucketExists(ctx, cfg.Bucket)
	if err != nil {
		return nil, fmt.Errorf("check bucket %s: %w", cfg.Bucket, err)
	}

	if !exists && cfg.AutoCreate {
		if err := mc.MakeBucket(ctx, cfg.Bucket, minio.MakeBucketOptions{Region: cfg.Region}); err != nil {
			return nil, fmt.Errorf("create bucket %s: %w", cfg.Bucket, err)
		}
		slog.Info("minio bucket created", "bucket", cfg.Bucket)
	}

	slog.Info("minio connected", "endpoint", cfg.Endpoint, "bucket", cfg.Bucket)
	return &Client{
		client:      mc,
		bucket:      cfg.Bucket,
		region:      cfg.Region,
		autoCreate:  cfg.AutoCreate,
		listObjects: mc.ListObjects,
	}, nil
}

// Upload stores a file with the given key and content type.
func (c *Client) Upload(ctx context.Context, key string, reader io.Reader, size int64, contentType string) error {
	_, err := c.client.PutObject(ctx, c.bucket, key, reader, size, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return fmt.Errorf("upload %s: %w", key, err)
	}
	slog.Debug("file uploaded", "key", key, "size", size)
	return nil
}

// Download retrieves a file by key.
func (c *Client) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := c.client.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", key, err)
	}
	return obj, nil
}

// Delete removes a file by key.
func (c *Client) Delete(ctx context.Context, key string) error {
	if err := c.client.RemoveObject(ctx, c.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}

// ListOlderThan returns at most limit object candidates older than cutoff.
// The collector rechecks timestamps and PostgreSQL references before deletion.
func (c *Client) ListOlderThan(ctx context.Context, cutoff time.Time, limit int) ([]ingestion.ObjectCandidate, error) {
	if limit <= 0 {
		limit = 100
	}
	listObjects := c.listObjects
	if listObjects == nil {
		listObjects = c.client.ListObjects
	}
	c.listMu.Lock()
	defer c.listMu.Unlock()

	out, err := c.listOlderThanFrom(ctx, listObjects, cutoff, limit, c.listCursor)
	if err != nil {
		return nil, err
	}
	// Reaching the end after a previous bounded batch resets the cursor. Retry
	// once from the start so a collector interval is not wasted on an empty page.
	if len(out) == 0 && c.listCursor != "" {
		c.listCursor = ""
		return c.listOlderThanFrom(ctx, listObjects, cutoff, limit, "")
	}
	return out, nil
}

func (c *Client) listOlderThanFrom(ctx context.Context, listObjects func(context.Context, string, minio.ListObjectsOptions) <-chan minio.ObjectInfo, cutoff time.Time, limit int, startAfter string) ([]ingestion.ObjectCandidate, error) {
	listCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	objects := listObjects(listCtx, c.bucket, minio.ListObjectsOptions{Recursive: true, StartAfter: startAfter})
	out := make([]ingestion.ObjectCandidate, 0, limit)
	for object := range objects {
		if object.Err != nil {
			return nil, fmt.Errorf("list object candidates: %w", object.Err)
		}
		if !strings.Contains(object.Key, "/versions/") || !object.LastModified.Before(cutoff) {
			continue
		}
		out = append(out, ingestion.ObjectCandidate{Key: object.Key, ModifiedAt: object.LastModified})
		c.listCursor = object.Key
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// DeleteByPrefix removes every object whose key starts with prefix. Used for
// document deletion, where the object key is tenant/{docID}{ext} and only the
// doc_id is known at delete time.
func (c *Client) DeleteByPrefix(ctx context.Context, prefix string) error {
	objects := c.client.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{Prefix: prefix})
	var deleteErr error
	for obj := range objects {
		if obj.Err != nil {
			return fmt.Errorf("list objects for prefix %s: %w", prefix, obj.Err)
		}
		if err := c.client.RemoveObject(ctx, c.bucket, obj.Key, minio.RemoveObjectOptions{}); err != nil {
			deleteErr = fmt.Errorf("delete %s: %w", obj.Key, err)
		}
	}
	return deleteErr
}

// DeleteByPrefixExcept removes every object under prefix except keepKey. Upload
// upsert uses it to wipe a document's previous objects after the replacement has
// already been written under the same tenant/{docID} prefix.
func (c *Client) DeleteByPrefixExcept(ctx context.Context, prefix, keepKey string) error {
	objects := c.client.ListObjects(ctx, c.bucket, minio.ListObjectsOptions{Prefix: prefix})
	var deleteErr error
	for obj := range objects {
		if obj.Err != nil {
			return fmt.Errorf("list objects for prefix %s: %w", prefix, obj.Err)
		}
		if obj.Key == keepKey {
			continue
		}
		if err := c.client.RemoveObject(ctx, c.bucket, obj.Key, minio.RemoveObjectOptions{}); err != nil {
			deleteErr = fmt.Errorf("delete %s: %w", obj.Key, err)
		}
	}
	return deleteErr
}

// Exists checks if a file exists.
func (c *Client) Exists(ctx context.Context, key string) (bool, error) {
	_, err := c.client.StatObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// PresignURL generates a presigned URL for direct upload/download.
func (c *Client) PresignURL(ctx context.Context, key string, expirySeconds int) (string, error) {
	expiry := time.Duration(expirySeconds) * time.Second
	reqParams := make(url.Values)
	u, err := c.client.PresignedGetObject(ctx, c.bucket, key, expiry, reqParams)
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", key, err)
	}
	return u.String(), nil
}
