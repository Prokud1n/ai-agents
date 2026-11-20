package gateway

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fileToken = "123:SECRET"

// errAny marks a case where any refusal is right (os.Root reports an escape with its own error).
var errAny = errors.New("any error")

// filesLayout builds a Bot API files directory and a directory outside it:
//
//	base/files/123:SECRET/videos/file_0.mp4   (10 bytes)
//	base/files/123:SECRET/videos/big.mp4      (100 bytes)
//	base/files/123:SECRET/videos/link.mp4     → base/outside/secret.txt
//	base/files/123:SECRET/escape              → base/outside
//	base/outside/secret.txt
func filesLayout(t *testing.T) (filesDir, outsideDir string) {
	t.Helper()

	base := t.TempDir()
	files := filepath.Join(base, "files")
	videos := filepath.Join(files, fileToken, "videos")
	outside := filepath.Join(base, "outside")

	require.NoError(t, os.MkdirAll(videos, 0o750))
	require.NoError(t, os.MkdirAll(outside, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(videos, "file_0.mp4"), []byte("0123456789"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(videos, "big.mp4"), make([]byte, 100), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(videos, "link.mp4")))
	require.NoError(t, os.Symlink(outside, filepath.Join(files, fileToken, "escape")))

	return files, outside
}

func TestIncomingFilePath(t *testing.T) {
	t.Parallel()

	files, outside := filesLayout(t)
	videos := filepath.Join(files, fileToken, "videos")

	tests := []struct {
		name         string
		input        string
		expectedSize int64
		expectedErr  error // nil: accepted
	}{
		{name: "file inside the directory", input: filepath.Join(videos, "file_0.mp4"), expectedSize: 10},
		{
			name:         "path is cleaned",
			input:        filepath.Join(files, fileToken, "documents") + "/../videos/./file_0.mp4",
			expectedSize: 10,
		},
		{name: "relative path of the public api", input: "videos/file_0.mp4", expectedErr: ErrNotLocalFile},
		{name: "dot-dot outside", input: files + "/../outside/secret.txt", expectedErr: ErrFileOutsideDir},
		{name: "absolute path outside", input: filepath.Join(outside, "secret.txt"), expectedErr: ErrFileOutsideDir},
		{name: "proc environ", input: "/proc/self/environ", expectedErr: ErrFileOutsideDir},
		{name: "file symlink outside", input: filepath.Join(videos, "link.mp4"), expectedErr: ErrFileOutsideDir},
		{
			name:        "directory symlink outside",
			input:       filepath.Join(files, fileToken, "escape", "secret.txt"),
			expectedErr: errAny,
		},
		{name: "not a regular file", input: videos, expectedErr: ErrFileOutsideDir},
		{name: "too large", input: filepath.Join(videos, "big.mp4"), expectedErr: ErrFileTooLarge},
		{name: "missing", input: filepath.Join(videos, "missing.mp4"), expectedErr: os.ErrNotExist},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir, rel, err := locate([]string{files}, tt.input)

			var size int64
			if err == nil {
				size, err = statInRoot(dir, rel, 50)
			}

			checkFileErr(t, tt.expectedErr, err)

			if tt.expectedErr == nil {
				assert.Equal(t, tt.expectedSize, size)
			}
		})
	}
}

func TestMediaOpen(t *testing.T) {
	t.Parallel()

	files, _ := filesLayout(t)

	tests := []struct {
		name         string
		input        *Media
		expectedData string
		expectedErr  error
	}{
		{
			name:         "downloaded file",
			input:        &Media{dir: files, rel: filepath.Join(fileToken, "videos", "file_0.mp4"), maxSize: 50},
			expectedData: "0123456789",
		},
		{name: "not downloaded", input: &Media{}, expectedErr: ErrMediaNotDownloaded},
		{
			name:        "path through a symlink outside",
			input:       &Media{dir: files, rel: filepath.Join(fileToken, "escape", "secret.txt"), maxSize: 50},
			expectedErr: errAny,
		},
		{
			name:        "file grew above the limit",
			input:       &Media{dir: files, rel: filepath.Join(fileToken, "videos", "big.mp4"), maxSize: 50},
			expectedErr: ErrFileTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reader, err := tt.input.Open()

			checkFileErr(t, tt.expectedErr, err)

			if tt.expectedErr != nil {
				return
			}

			data, err := io.ReadAll(reader)
			require.NoError(t, err)
			require.NoError(t, reader.Close())
			assert.Equal(t, tt.expectedData, string(data))
		})
	}
}

// checkFileErr: nil expects success, errAny any error; an error never contains the path with the token.
func checkFileErr(t *testing.T, expected, err error) {
	t.Helper()

	switch {
	case expected == nil:
		require.NoError(t, err)

		return
	case errors.Is(expected, errAny):
		require.Error(t, err)
	default:
		require.ErrorIs(t, err, expected)
	}

	assert.NotContains(t, err.Error(), "SECRET", "errors must not contain the path with the token")
}
