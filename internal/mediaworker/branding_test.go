package mediaworker

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/subtitles"
)

// TestPipelineBranding: the handle of the job reaches the subtitles only when it passes the check again; the screen
// notes of the cut go as they are.
func TestPipelineBranding(t *testing.T) {
	t.Parallel()

	p := &Pipeline{log: slog.New(slog.DiscardHandler)}

	tests := []struct {
		name     string
		input    *editjob.Branding
		expected *subtitles.Branding
	}{
		{name: "none"},
		{
			name:     "a valid handle",
			input:    &editjob.Branding{Handle: "@prokudina.ana", Icon: true, EndCard: true},
			expected: &subtitles.Branding{Handle: "@prokudina.ana", Icon: true, EndCard: true},
		},
		{name: "an invalid handle is not drawn", input: &editjob.Branding{Handle: "@a{\\an7}", EndCard: true}},
		{
			name:     "the top line",
			input:    &editjob.Branding{TopbarLeft: trainerLeft, TopbarRight: trainerRight},
			expected: &subtitles.Branding{TopbarLeft: trainerLeft, TopbarRight: trainerRight},
		},
		{name: "an invalid top line is not drawn", input: &editjob.Branding{TopbarLeft: "tg {\\an7}"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, p.branding(t.Context(), &editjob.Job{ID: "task-1-v1", Branding: tt.input}))
		})
	}

	assert.Equal(t, []subtitles.Note{{Kind: subtitles.CalloutNote, Text: "6 лет", Start: 1, End: 2}},
		subtitleNotes([]roughcut.ScreenNote{{Kind: roughcut.CalloutNote, Text: "6 лет", Start: 1, End: 2}}))
}
