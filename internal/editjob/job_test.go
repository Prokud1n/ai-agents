package editjob

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

const testSourceKey = "incoming/-1001234567890/42/AgADuniq.mov"

var testDeadline = time.Date(2026, 11, 23, 12, 45, 0, 0, time.UTC)

func TestJobID(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "task-17-v1", JobID(17, 1))
	assert.NotEqual(t, JobID(17, 1), JobID(17, 2))
}

func TestJobValidate(t *testing.T) {
	t.Parallel()

	valid := Job{
		ID: JobID(5, 1), TaskID: 5, Version: 1, SourceKey: testSourceKey, QueueDeadline: testDeadline, Timeout: time.Hour,
	}

	tests := []struct {
		name        string
		input       func(j Job) Job
		expectedErr error
	}{
		{name: "valid", input: func(j Job) Job { return j }},
		{name: "id of another task", input: func(j Job) Job { j.ID = JobID(6, 1); return j }, expectedErr: ErrInvalidJob},
		{name: "no source", input: func(j Job) Job { j.SourceKey = ""; return j }, expectedErr: ErrInvalidJob},
		{name: "no queue deadline", input: func(j Job) Job { j.QueueDeadline = time.Time{}; return j }, expectedErr: ErrInvalidJob},
		{name: "no timeout", input: func(j Job) Job { j.Timeout = 0; return j }, expectedErr: ErrInvalidJob},
		{
			name: "zero task", input: func(j Job) Job { j.TaskID, j.ID = 0, JobID(0, 1); return j },
			expectedErr: ErrInvalidJob,
		},
		{name: "auto stage", input: func(j Job) Job { j.Stage = AutoStage; return j }},
		{
			name:  "render stage with settings",
			input: func(j Job) Job { j.Stage, j.Settings = RenderStage, &roughcut.Settings{MaxPause: 1}; return j },
		},
		{
			name: "render stage without settings", input: func(j Job) Job { j.Stage = RenderStage; return j },
			expectedErr: ErrInvalidJob,
		},
		{name: "unknown stage", input: func(j Job) Job { j.Stage = "music"; return j }, expectedErr: ErrInvalidJob},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			job := tt.input(valid)
			err := job.Validate()

			if tt.expectedErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tt.expectedErr)
		})
	}
}

// prefixKeys makes result keys like media.Store with the result prefix "edits".
type prefixKeys struct{}

func (prefixKeys) ResultKey(sourceKey, name, ext string) (string, error) {
	info, err := media.ParseKey(sourceKey)
	if err != nil {
		return "", err
	}

	return media.ResultKey("edits", info, name, ext)
}

func TestFiles(t *testing.T) {
	t.Parallel()

	files, err := Files(prefixKeys{}, testSourceKey, 2)
	require.NoError(t, err)

	assert.Equal(t, ResultFiles{
		Reel:       "edits/-1001234567890/42/AgADuniq-v2-reel.mp4",
		Plan:       "edits/-1001234567890/42/AgADuniq-v2-plan.json",
		Decisions:  "edits/-1001234567890/42/AgADuniq-v2-decisions.log",
		Transcript: "edits/-1001234567890/42/AgADuniq-transcript.json",
	}, files)

	_, err = Files(prefixKeys{}, "../etc/passwd", 1)
	require.ErrorIs(t, err, media.ErrInvalidKey)

	_, err = Files(failingKeys{}, testSourceKey, 1)
	require.ErrorIs(t, err, errNoKeys)
}

// TestFilesOfVersions: every version has its own reel, plan and log, so a revision never overwrites the draft before
// it; the transcript of the source is shared, so a revision needs no new transcription.
func TestFilesOfVersions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		version  int
		expected string
	}{
		{name: "first", version: 1, expected: "edits/-1001234567890/42/AgADuniq-v1-reel.mp4"},
		{name: "revision", version: 2, expected: "edits/-1001234567890/42/AgADuniq-v2-reel.mp4"},
		{name: "tenth", version: 10, expected: "edits/-1001234567890/42/AgADuniq-v10-reel.mp4"},
	}

	first, err := Files(prefixKeys{}, testSourceKey, 1)
	require.NoError(t, err)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			files, err := Files(prefixKeys{}, testSourceKey, tt.version)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, files.Reel)
			assert.Equal(t, first.Transcript, files.Transcript)

			if tt.version != 1 {
				assert.NotEqual(t, first.Reel, files.Reel)
				assert.NotEqual(t, first.Plan, files.Plan)
				assert.NotEqual(t, first.Decisions, files.Decisions)
			}
		})
	}
}

var errNoKeys = errors.New("no result prefix")

type failingKeys struct{}

func (failingKeys) ResultKey(string, string, string) (string, error) {
	return "", errNoKeys
}
