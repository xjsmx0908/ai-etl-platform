// Package s3 provides MinIO/S3 object storage for document uploads.
package s3

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Client wraps MinIO client for document storage.
type Client struct {
	client     *minio.Client
	bucket     string
	region     string
	autoCreate bool
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
		client:     mc,
		bucket:     cfg.Bucket,
		region:     cfg.Region,
		autoCreate: cfg.AutoCreate,
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
