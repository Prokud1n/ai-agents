package editor

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/subtitles"
)

// TestCheckBranding: REELS_HANDLE is checked strictly at the start of the app: empty is no handle, anything but «@»
// and 1–30 of a-z, 0-9, «.», «_» fails the start.
func TestCheckBranding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		handle   string
		expected *editjob.Branding
		invalid  bool
	}{
		{name: "none"},
		{name: "spaces only", handle: "  "},
		{
			name: "the trainer's handle", handle: " @prokudina.ana ",
			expected: &editjob.Branding{Handle: "@prokudina.ana", Icon: true, EndCard: true},
		},
		{name: "no @", handle: "prokudina.ana", invalid: true},
		{name: "capitals", handle: "@Prokudina", invalid: true},
		{name: "markup", handle: "@a{\\an7}", invalid: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Editor{ReelsHandle: tt.handle, ReelsHandleIcon: true, ReelsEndCard: true}

			err := checkBranding(cfg)
			if tt.invalid {
				require.ErrorIs(t, err, subtitles.ErrInvalidHandle)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, brandingOf(cfg))
		})
	}
}

// TestJobsCarryBranding: every job of the app carries its own copy of the handle and the card settings.
func TestJobsCarryBranding(t *testing.T) {
	t.Parallel()

	b := &fakeBoard{}
	e := NewExecutor(b, testStore{}, &fakeDecider{}, &fakeTech{}, &config.Editor{
		JobTimeout: testTimeout, QueueTimeout: testQueue, WorkerWait: testWorker, PollInterval: testPoll,
		ReelsHandle: "@prokudina.ana", ReelsHandleIcon: false, ReelsEndCard: true,
	}, slog.New(slog.DiscardHandler))

	job := e.newJob(editData())
	require.NotNil(t, job.Branding)
	assert.Equal(t, editjob.Branding{Handle: "@prokudina.ana", EndCard: true}, *job.Branding)
	assert.NotSame(t, e.branding, job.Branding, "a copy")

	assert.Nil(t, newTestExecutor(b).newJob(editData()).Branding, "no handle, no branding")
}
