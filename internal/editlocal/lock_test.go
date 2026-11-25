package editlocal

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcquireLock(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "out")
	now := time.Now()

	release, err := acquireLock(dir, time.Hour, now)
	require.NoError(t, err)

	content, err := os.ReadFile(filepath.Join(dir, LockFile))
	require.NoError(t, err)

	info, ok := parseLock(string(content))
	require.True(t, ok)
	assert.Equal(t, os.Getpid(), info.pid)

	_, err = acquireLock(dir, time.Hour, now)
	require.ErrorIs(t, err, ErrOutLocked, "a live run holds it")

	release()
	assert.NoFileExists(t, filepath.Join(dir, LockFile))

	release, err = acquireLock(dir, time.Hour, now)
	require.NoError(t, err, "free again")
	release()
}

func TestAcquireLockStale(t *testing.T) {
	t.Parallel()

	host, err := os.Hostname()
	require.NoError(t, err)

	now := time.Now()
	recent := now.Add(-time.Minute).UTC().Format(time.RFC3339)
	old := now.Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	alive := strconv.Itoa(os.Getpid())

	tests := []struct {
		name     string
		content  string
		expected error
	}{
		{name: "a live run", content: lockContent(alive, host, recent), expected: ErrOutLocked},
		{name: "a run on another host", content: lockContent("1", "other-host", recent), expected: ErrOutLocked},
		{name: "too old", content: lockContent(alive, host, old), expected: ErrStaleLock},
		{name: "a dead process", content: lockContent("2147483646", host, recent), expected: ErrStaleLock},
		{name: "unreadable", content: "garbage", expected: ErrStaleLock},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, LockFile), []byte(tt.content), 0o600))

			_, err := acquireLock(dir, time.Hour, now)
			require.ErrorIs(t, err, tt.expected)
			assert.FileExists(t, filepath.Join(dir, LockFile), "a lock of another run is not removed")
		})
	}
}

func lockContent(pid, host, since string) string {
	return strings.Join([]string{pid, host, since, ""}, "\n")
}

func TestReleaseKeepsAnotherLock(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	release, err := acquireLock(dir, time.Hour, time.Now())
	require.NoError(t, err)

	// The lock was removed by hand and another run took the directory.
	other := lockContent("1", "other-host", time.Now().UTC().Format(time.RFC3339))
	require.NoError(t, os.WriteFile(filepath.Join(dir, LockFile), []byte(other), 0o600))

	release()

	content, err := os.ReadFile(filepath.Join(dir, LockFile))
	require.NoError(t, err)
	assert.Equal(t, other, string(content), "the lock of another run is not removed")
}
