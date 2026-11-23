// Package s3test gives integration tests a real S3 (RustFS).
//
// S3 of the compose stack is not published to the host, so `make test-integration` starts a separate throwaway
// RustFS container (the image of the compose s3 service, published on 127.0.0.1:S3_TEST_HOST_PORT, 9100 by
// default) and passes its address and test-only keys in S3_TEST_ENDPOINT, S3_TEST_ACCESS_KEY and
// S3_TEST_SECRET_KEY. The rules are the same as in pgtest and redistest: a test with "Integration" in its name
// calls Connect, which skips it unless INTEGRATION=1; with INTEGRATION=1 a missing S3_TEST_ENDPOINT fails the test
// instead of skipping it.
//
// Isolation: each test gets its own bucket; its objects and the bucket are deleted when the test ends.
package s3test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx/fxtest"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/s3"
)

const (
	enableEnv     = "INTEGRATION"
	endpointEnv   = "S3_TEST_ENDPOINT"
	accessKeyEnv  = "S3_TEST_ACCESS_KEY"
	signingKeyEnv = "S3_TEST_SECRET_KEY"
	region        = "us-east-1"
	// cleanupTimeout bounds deleting the test's objects and bucket.
	cleanupTimeout = 30 * time.Second
)

var bucketCounter atomic.Int64

// Connect returns a started client bound to a new bucket of the test S3; the bucket is deleted when the test
// ends. Without INTEGRATION=1 the test is skipped.
func Connect(t *testing.T) *s3.Client {
	t.Helper()

	if os.Getenv(enableEnv) != "1" {
		t.Skip("integration test: run with INTEGRATION=1 and a test S3 (make up && make test-integration)")
	}

	endpoint := os.Getenv(endpointEnv)
	require.NotEmpty(t, endpoint, "%s is not set: run the tests with make test-integration", endpointEnv)

	// Bucket names: 3-63 lower-case letters, digits and hyphens.
	bucket := fmt.Sprintf("test-%d-%d-%d", time.Now().UnixNano(), os.Getpid(), bucketCounter.Add(1))

	lc := fxtest.NewLifecycle(t)
	client, err := s3.NewClient(lc, &config.S3{
		Endpoint:  endpoint,
		AccessKey: os.Getenv(accessKeyEnv),
		SecretKey: os.Getenv(signingKeyEnv),
		Bucket:    bucket,
		Region:    region,
	}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	lc.RequireStart() // creates the bucket
	t.Cleanup(lc.RequireStop)
	t.Cleanup(func() { removeBucket(t, client) })

	return client
}

// removeBucket deletes every object of the test's bucket and the bucket itself.
func removeBucket(t *testing.T, client *s3.Client) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	conn, bucket := client.Conn(), client.Bucket()

	for obj := range conn.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
		if obj.Err != nil {
			t.Errorf("failed to list test objects: %v", obj.Err)

			return
		}

		if err := conn.RemoveObject(ctx, bucket, obj.Key, minio.RemoveObjectOptions{}); err != nil {
			t.Errorf("failed to delete test object %s: %v", obj.Key, err)
		}
	}

	if err := conn.RemoveBucket(ctx, bucket); err != nil {
		t.Errorf("failed to delete test bucket %s: %v", bucket, err)
	}
}
