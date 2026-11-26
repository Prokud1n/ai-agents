package editor

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Prokud1n/ai-agents/internal/config"
)

// TestWarnHiddenHandle: the app warns when REELS_HANDLE is set and the media worker draws the trainer's style (the
// default or its file), not with the words' or the phrase style, a style of its own or without a handle.
func TestWarnHiddenHandle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		handle string
		style  string
		warns  bool
	}{
		{name: "the default style", handle: testHandle, warns: true},
		{name: "the trainer's file", handle: testHandle, style: "/app/subtitles-style.yaml", warns: true},
		{name: "the words' style", handle: testHandle, style: "/app/subtitles-style-words.yaml"},
		{name: "the phrase style", handle: testHandle, style: "/app/subtitles-style-phrase.yaml"},
		{name: "a style of its own", handle: testHandle, style: "/brand/mine.yaml"},
		{name: "no handle", style: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			WarnHiddenHandle(&config.Editor{ReelsHandle: tt.handle, SubtitlesStyleFile: tt.style},
				slog.New(slog.NewTextHandler(&out, nil)))

			if tt.warns {
				assert.Contains(t, out.String(), "level=WARN")
				assert.Contains(t, out.String(), "SUBTITLES_STYLE_FILE=/app/subtitles-style-words.yaml")

				return
			}

			assert.Empty(t, out.String())
		})
	}
}
