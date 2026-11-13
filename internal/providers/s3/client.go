package s3

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
)

var errBucketNotFound = errors.New("bucket not found")

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

func (c *Client) Bucket() string {
	return c.bucket
}

// Name is the dependency name in /readyz.
func (*Client) Name() string {
	return "s3"
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
