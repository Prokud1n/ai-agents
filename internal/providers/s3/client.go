package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
)

const (
	// uploadPartSize: a file above it is uploaded in parts of this size, each buffered in memory; with
	// 10 000 parts at most it covers files up to ~160 GB.
	uploadPartSize = 16 << 20
	noSuchKeyCode  = "NoSuchKey"
)

var (
	errBucketNotFound = errors.New("bucket not found")
	// ErrObjectNotFound: there is no object under the key.
	ErrObjectNotFound = errors.New("s3 object not found")
)

// Client is an S3-compatible object storage client bound to the media bucket.
type Client struct {
	mc     *minio.Client
	bucket string
	region string
}

func NewClient(lc fx.Lifecycle, cfg *config.S3, log *slog.Logger) (*Client, error) {
	mc, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create s3 client: %w", err)
	}

	client := &Client{mc: mc, bucket: cfg.Bucket, region: cfg.Region}

	lc.Append(fx.StartHook(func(ctx context.Context) error {
		created, err := client.ensureBucket(ctx)
		if err != nil {
			return err
		}

		log.InfoContext(ctx, "s3: connected", "endpoint", cfg.Endpoint, "bucket", cfg.Bucket, "bucket_created", created)

		return nil
	}))

	return client, nil
}

// Conn returns the underlying minio client for object operations.
func (c *Client) Conn() *minio.Client {
	return c.mc
}

// PutObject uploads size bytes of r under the key with the content type; an object under the key is replaced.
func (c *Client) PutObject(ctx context.Context, key string, r io.Reader, size int64, contentType string) error {
	_, err := c.mc.PutObject(ctx, c.bucket, key, r, size, minio.PutObjectOptions{
		ContentType: contentType,
		PartSize:    uploadPartSize,
	})
	if err != nil {
		return fmt.Errorf("failed to put s3 object: %w", err)
	}

	return nil
}

// GetObject opens the object under the key for reading; the caller closes it. A missing object is
// ErrObjectNotFound.
func (c *Client) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	obj, err := c.mc.GetObject(ctx, c.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get s3 object: %w", err)
	}

	// GetObject is lazy: Stat makes the request, so a missing object is found here and not on the first read.
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()

		if minio.ToErrorResponse(err).Code == noSuchKeyCode {
			return nil, fmt.Errorf("failed to get s3 object: %w", ErrObjectNotFound)
		}

		return nil, fmt.Errorf("failed to get s3 object: %w", err)
	}

	return obj, nil
}

// ObjectExists reports whether there is an object under the key (a HEAD request, nothing is read).
func (c *Client) ObjectExists(ctx context.Context, key string) (bool, error) {
	if _, err := c.mc.StatObject(ctx, c.bucket, key, minio.StatObjectOptions{}); err != nil {
		if minio.ToErrorResponse(err).Code == noSuchKeyCode {
			return false, nil
		}

		return false, fmt.Errorf("failed to stat s3 object: %w", err)
	}

	return true, nil
}

func (c *Client) Bucket() string {
	return c.bucket
}

func (c *Client) Ping(ctx context.Context) error {
	exists, err := c.mc.BucketExists(ctx, c.bucket)
	if err != nil {
		return fmt.Errorf("failed to check s3 bucket: %w", err)
	}

	if !exists {
		return fmt.Errorf("failed to find s3 bucket %q: %w", c.bucket, errBucketNotFound)
	}

	return nil
}

func (c *Client) ensureBucket(ctx context.Context) (bool, error) {
	exists, err := c.mc.BucketExists(ctx, c.bucket)
	if err != nil {
		return false, fmt.Errorf("failed to connect to s3: %w", err)
	}

	if exists {
		return false, nil
	}

	if err := c.mc.MakeBucket(ctx, c.bucket, minio.MakeBucketOptions{Region: c.region}); err != nil {
		return false, fmt.Errorf("failed to create s3 bucket: %w", err)
	}

	return true, nil
}
