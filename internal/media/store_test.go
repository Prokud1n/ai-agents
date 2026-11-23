package media

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/s3"
)

const testContent = "video-bytes"

var errStorageDown = errors.New("storage is down")

type storedObject struct {
	data        []byte
	contentType string
}

// fakeStorage keeps objects in memory and records the sizes it was given.
type fakeStorage struct {
	mu      sync.Mutex
	objects map[string]storedObject
	sizes   []int64
	putErr  error
	getErr  error
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{objects: map[string]storedObject{}}
}

func (f *fakeStorage) PutObject(_ context.Context, key string, r io.Reader, size int64, contentType string) error {
	if f.putErr != nil {
		return f.putErr
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.sizes = append(f.sizes, size)
	f.objects[key] = storedObject{data: data, contentType: contentType}

	return nil
}

func (f *fakeStorage) GetObject(_ context.Context, key string) (io.ReadCloser, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	obj, ok := f.objects[key]
	if !ok {
		return nil, s3.ErrObjectNotFound
	}

	return io.NopCloser(bytes.NewReader(obj.data)), nil
}

func newTestStore(t *testing.T, storage *fakeStorage) *Store {
	t.Helper()

	store, err := NewStore(storage, &config.Media{KeyPrefix: testPrefix, UploadTimeout: time.Minute},
		slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	return store
}

func TestNewStoreRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input config.Media
	}{
		{name: "prefix with dots", input: config.Media{KeyPrefix: "../x", UploadTimeout: time.Minute}},
		{name: "no upload timeout", input: config.Media{KeyPrefix: testPrefix}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewStore(newFakeStorage(), &tt.input, slog.New(slog.DiscardHandler))

			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
}

func TestStoreSaveAndDownload(t *testing.T) {
	t.Parallel()

	storage := newFakeStorage()
	store := newTestStore(t, storage)
	file := testFile(mimeMOV, "IMG_1.MOV")

	// The reader is not at its start and Telegram's size is not used: the size comes from the file.
	reader := strings.NewReader(testContent)
	_, err := reader.Seek(3, io.SeekStart)
	require.NoError(t, err)

	key, err := store.Save(t.Context(), file, reader)
	require.NoError(t, err)
	assert.Equal(t, "incoming/-1001234567890/42/AgADBAADq-_x.mov", key)

	// A redelivery writes the same key.
	again, err := store.Save(t.Context(), file, strings.NewReader(testContent))
	require.NoError(t, err)
	assert.Equal(t, key, again)

	assert.Len(t, storage.objects, 1)
	assert.Equal(t, storedObject{data: []byte(testContent), contentType: mimeMOV}, storage.objects[key])
	assert.Equal(t, []int64{int64(len(testContent)), int64(len(testContent))}, storage.sizes)

	dir := t.TempDir()

	local, err := store.Download(t.Context(), key, dir)
	require.NoError(t, err)
	assert.Equal(t, dir, filepath.Dir(local.Path))
	assert.Equal(t, extMOV, filepath.Ext(local.Path))

	data, err := os.ReadFile(local.Path)
	require.NoError(t, err)
	assert.Equal(t, testContent, string(data))

	require.NoError(t, local.Remove())
	require.NoError(t, local.Remove(), "removing twice is not an error")
	assert.NoFileExists(t, local.Path)
}

func TestStoreSaveErrors(t *testing.T) {
	t.Parallel()

	storage := newFakeStorage()
	storage.putErr = errStorageDown
	store := newTestStore(t, storage)

	_, err := store.Save(t.Context(), testFile(mimeMP4, ""), strings.NewReader(testContent))
	require.ErrorIs(t, err, errStorageDown)

	_, err = store.Save(t.Context(), &File{ChatID: testChatID, MessageID: testMessageID, FileUniqueID: "a/b"},
		strings.NewReader(testContent))
	require.ErrorIs(t, err, ErrInvalidFile)
}

func TestStoreDownloadErrors(t *testing.T) {
	t.Parallel()

	broken := newFakeStorage()
	broken.getErr = errStorageDown

	tests := []struct {
		name        string
		storage     *fakeStorage
		key         string
		expectedErr error
	}{
		{name: "missing object", storage: newFakeStorage(), key: "incoming/-100/1/u.mp4", expectedErr: ErrFileNotFound},
		{name: "key of another prefix", storage: newFakeStorage(), key: "other/-100/1/u.mp4", expectedErr: ErrInvalidKey},
		{name: "not a media key", storage: newFakeStorage(), key: "incoming/../../etc/passwd", expectedErr: ErrInvalidKey},
		{name: "storage error", storage: broken, key: "incoming/-100/1/u.mp4", expectedErr: errStorageDown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()

			_, err := newTestStore(t, tt.storage).Download(t.Context(), tt.key, dir)
			require.ErrorIs(t, err, tt.expectedErr)

			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			assert.Empty(t, entries, "no local file is left")
		})
	}
}
