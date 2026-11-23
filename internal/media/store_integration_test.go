package media

import (
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/s3test"
)

// TestIntegrationStoreSaveAndDownload runs the store against RustFS: a video saved under its key, saved again
// (the same key, the new content), downloaded back within and above the size limit, and a missing key.
func TestIntegrationStoreSaveAndDownload(t *testing.T) {
	t.Parallel()

	store, err := NewStore(s3test.Connect(t), &config.Media{KeyPrefix: testPrefix, UploadTimeout: time.Minute},
		slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	ctx := t.Context()
	file := testFile(mimeMOV, "IMG_1.MOV")

	key, err := store.Save(ctx, file, strings.NewReader("old content"))
	require.NoError(t, err)

	again, err := store.Save(ctx, file, strings.NewReader(testContent))
	require.NoError(t, err)
	assert.Equal(t, key, again, "a redelivery writes the same key")

	dir := t.TempDir()

	local, err := store.Download(ctx, key, dir, int64(len(testContent)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, local.Remove()) })

	data, err := os.ReadFile(local.Path)
	require.NoError(t, err)
	assert.Equal(t, testContent, string(data), "the second save replaced the first")
	assert.Equal(t, int64(len(testContent)), local.Size)

	_, err = store.Download(ctx, key, dir, int64(len(testContent))-1)
	require.ErrorIs(t, err, ErrFileTooLarge)

	_, err = store.Download(ctx, "incoming/-100/1/missing.mp4", dir, int64(len(testContent)))
	require.ErrorIs(t, err, ErrFileNotFound)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "failed downloads leave no files")
}
