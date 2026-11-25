package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/subtitles"
)

const (
	testVideo      = "video.mp4"
	testTranscript = "result.json"
	testOut        = "reel.mp4"

	flagIn         = "-in"
	flagTranscript = "-transcript"
	flagOut        = "-out"
)

func TestParseFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       []string
		expected    options
		expectedErr error
	}{
		{
			name:     "render",
			input:    []string{flagIn, testVideo, flagTranscript, testTranscript, flagOut, testOut},
			expected: options{in: testVideo, transcript: testTranscript, out: testOut, fit: ffmpeg.PadFitMode},
		},
		{
			name: "all flags",
			input: []string{
				flagIn, testVideo, flagTranscript, testTranscript, flagOut, testOut, "-style", "brand.yaml",
				"-accents", " присед, 3 подхода ,,", "-karaoke", "-fit", "crop", "-hook", "Три ошибки в планке",
			},
			expected: options{
				in: testVideo, transcript: testTranscript, out: testOut, style: "brand.yaml",
				accents: []string{"присед", "3 подхода"}, karaoke: true, fit: ffmpeg.CropFitMode,
				hook: "Три ошибки в планке",
			},
		},
		{
			name:     "ass only needs no video",
			input:    []string{flagTranscript, testTranscript, flagOut, "subtitles.ass", "-ass-only"},
			expected: options{transcript: testTranscript, out: "subtitles.ass", assOnly: true, fit: ffmpeg.PadFitMode},
		},
		{name: "help", input: []string{"-h"}, expectedErr: flag.ErrHelp},
		{name: "missing transcript", input: []string{flagIn, testVideo, flagOut, testOut}, expectedErr: errNoTranscript},
		{name: "missing output", input: []string{flagIn, testVideo, flagTranscript, testTranscript}, expectedErr: errNoOutput},
		{name: "missing video", input: []string{flagTranscript, testTranscript, flagOut, testOut}, expectedErr: errNoInput},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer

			got, err := parseFlags(tt.input, &output)
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr)
				assert.Contains(t, output.String(), "usage: subtitles")

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestLoadInput(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	transcript := filepath.Join(dir, "result.json")
	broken := filepath.Join(dir, "broken.json")
	style := filepath.Join(dir, "style.yaml")

	require.NoError(t, os.WriteFile(transcript, []byte(`{"language":"ru","duration":1.5,"text":"Привет!",
		"segments":[{"start":0,"end":1.2,"text":"Привет!"}],"words":[{"word":"Привет","start":0.1,"end":0.9}]}`), 0o600))
	require.NoError(t, os.WriteFile(broken, []byte(`{"words":`), 0o600))
	require.NoError(t, os.WriteFile(style, []byte("layout:\n  position: bottom\n"), 0o600))

	tests := []struct {
		name        string
		input       options
		expectedPos subtitles.Position
		expectErr   bool
	}{
		{name: "transcript with the built-in style", input: options{transcript: transcript, karaoke: true}},
		{name: "style file", input: options{transcript: transcript, style: style}, expectedPos: subtitles.BottomPosition},
		{name: "broken transcript", input: options{transcript: broken}, expectErr: true},
		{name: "missing transcript", input: options{transcript: filepath.Join(dir, "none.json")}, expectErr: true},
		{name: "missing style", input: options{transcript: transcript, style: filepath.Join(dir, "none.yaml")}, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := loadInput(&tt.input)
			if tt.expectErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Len(t, got.Transcript.Words, 1)
			assert.Equal(t, "Привет", got.Transcript.Words[0].Word)
			assert.Equal(t, tt.input.karaoke, got.Karaoke)

			if tt.expectedPos == "" {
				assert.Nil(t, got.Style)

				return
			}

			require.NotNil(t, got.Style)
			assert.Equal(t, tt.expectedPos, got.Style.Layout.Position)
		})
	}
}
