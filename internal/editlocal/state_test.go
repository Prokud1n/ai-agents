package editlocal

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSourceKey = "incoming/1/1000000000000000007/local.mov"

func TestTaskID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		seq       int64
		expected  int64
		expectErr bool
	}{
		{name: "first", seq: 1, expected: TaskIDBase + 1},
		{name: "large", seq: 123456, expected: TaskIDBase + 123456},
		{name: "last", seq: math.MaxInt64 - TaskIDBase, expected: math.MaxInt64},
		{name: "zero", seq: 0, expectErr: true},
		{name: "negative", seq: -1, expectErr: true},
		{name: "overflow", seq: math.MaxInt64 - TaskIDBase + 1, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			id, err := TaskID(tt.seq)
			if tt.expectErr {
				require.ErrorIs(t, err, ErrInvalidSequence)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, id)
			assert.True(t, IsLocalTaskID(id))
		})
	}
}

func TestIsLocalTaskID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		id       int64
		expected bool
	}{
		{name: "a task of the app", id: 42, expected: false},
		{name: "a huge task of the app", id: TaskIDBase, expected: false},
		{name: "a local edit", id: TaskIDBase + 1, expected: true},
		{name: "negative", id: -TaskIDBase - 1, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, IsLocalTaskID(tt.id))
		})
	}
}

func TestStateNext(t *testing.T) {
	t.Parallel()

	first := &State{TaskID: TaskIDBase + 7, SourceKey: testSourceKey, Caption: "подпись", Version: 1}

	second := first.Next("сделай 40 секунд")
	third := second.Next("убери начало")

	assert.Equal(t, 1, first.Version, "the state is not changed")
	assert.Empty(t, first.Revisions)
	assert.Equal(t, 2, second.Version)
	assert.Equal(t, []Revision{{Version: 2, Text: "сделай 40 секунд"}}, second.Revisions)
	assert.Equal(t, 3, third.Version)
	assert.Equal(t, []Revision{{Version: 2, Text: "сделай 40 секунд"}, {Version: 3, Text: "убери начало"}}, third.Revisions)
	assert.Equal(t, "подпись", third.Caption)
	assert.Equal(t, testSourceKey, third.SourceKey)
}

func TestSaveAndLoadState(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	_, err := LoadState(dir)
	require.ErrorIs(t, err, ErrNoState, "a new output directory")

	state := &State{
		TaskID: TaskIDBase + 7, SourceKey: testSourceKey, SourceFile: "clip.mov", Caption: "подпись", Version: 1,
		UpdatedAt: time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC),
	}
	require.NoError(t, SaveState(dir, state))

	loaded, err := LoadState(dir)
	require.NoError(t, err)
	assert.Equal(t, state, loaded)

	revised := loaded.Next("короче")
	require.NoError(t, SaveState(dir, revised))

	loaded, err = LoadState(dir)
	require.NoError(t, err)
	assert.Equal(t, 2, loaded.Version)
	assert.Len(t, loaded.Revisions, 1)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no temporary files left")
	assert.Equal(t, StateFile, entries[0].Name())
}

func TestLoadStateInvalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "not json", content: "{"},
		{name: "a task of the app", content: `{"task_id": 42, "source_key": "` + testSourceKey + `", "version": 1}`},
		{name: "no version", content: `{"task_id": 1000000000000000007, "source_key": "` + testSourceKey + `"}`},
		{name: "bad key", content: `{"task_id": 1000000000000000007, "source_key": "../etc/passwd", "version": 1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, StateFile), []byte(tt.content), 0o600))

			_, err := LoadState(dir)
			require.ErrorIs(t, err, ErrInvalidState)
		})
	}
}

func TestSaveStateRefusesInvalid(t *testing.T) {
	t.Parallel()

	err := SaveState(t.TempDir(), &State{TaskID: 42, SourceKey: testSourceKey, Version: 1})
	require.ErrorIs(t, err, ErrInvalidState)
}
