package s3_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/providers/s3"
	"github.com/Prokud1n/ai-agents/internal/s3test"
)

const (
	testKey         = "incoming/-100/1/u.mp4"
	testContentType = "video/mp4"
)

func readObject(t *testing.T, client *s3.Client, key string) []byte {
	t.Helper()

	obj, err := client.GetObject(t.Context(), key)
	require.NoError(t, err)

	defer func() { require.NoError(t, obj.Close()) }()

	data, err := io.ReadAll(obj)
	require.NoError(t, err)

	return data
}

// TestIntegrationClientObjects runs the client against RustFS: put and get, overwrite of the same key, a missing
// key, and an object above the part size (a multipart upload).
func TestIntegrationClientObjects(t *testing.T) {
	t.Parallel()

	client := s3test.Connect(t)
	ctx := t.Context()

	require.NoError(t, client.PutObject(ctx, testKey, strings.NewReader("first"), int64(len("first")), testContentType))
	assert.Equal(t, []byte("first"), readObject(t, client, testKey))

	info, err := client.Conn().StatObject(ctx, client.Bucket(), testKey, minio.StatObjectOptions{})
	require.NoError(t, err)
	assert.Equal(t, testContentType, info.ContentType)

	// The same key again replaces the object.
	require.NoError(t, client.PutObject(ctx, testKey, strings.NewReader("second"), int64(len("second")), testContentType))
	assert.Equal(t, []byte("second"), readObject(t, client, testKey))

	_, err = client.GetObject(ctx, "incoming/-100/1/missing.mp4")
	require.ErrorIs(t, err, s3.ErrObjectNotFound)

	// Above the 16 MiB part size: uploaded in parts.
	big := bytes.Repeat([]byte("0123456789abcdef"), (17<<20)/16)
	require.NoError(t, client.PutObject(ctx, "big.mp4", bytes.NewReader(big), int64(len(big)), testContentType))
	assert.Equal(t, big, readObject(t, client, "big.mp4"))
}
