package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

func TestParseFlags(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       []string
		expected    options
		expectedErr error
	}{
		{name: "input only", input: []string{"-in", "video.mp4"}, expected: options{in: "video.mp4"}},
		{
			name:     "all flags",
			input:    []string{"-in", "video.mp4", "-lang", "en", "-out", "result.json"},
			expected: options{in: "video.mp4", lang: "en", out: "result.json"},
		},
		{name: "help", input: []string{"-h"}, expectedErr: flag.ErrHelp},
		{name: "missing input", input: []string{"-lang", "ru"}, expectedErr: errNoInput},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer

			got, err := parseFlags(tt.input, &output)
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr)
				assert.Contains(t, output.String(), "usage: transcribe")

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestWriteResult(t *testing.T) {
	t.Parallel()

	result := &transcription.Result{
		Language: "ru",
		Duration: 1.5,
		Text:     "Привет!",
		Segments: []transcription.Segment{{Start: 0, End: 1.2, Text: "Привет!"}},
		Words:    []transcription.Word{{Word: "Привет", Start: 0.1, End: 0.9}},
	}

	expected := `{
  "language": "ru",
  "duration": 1.5,
  "text": "Привет!",
  "segments": [
    {
      "start": 0,
      "end": 1.2,
      "text": "Привет!"
    }
  ],
  "words": [
    {
      "word": "Привет",
      "start": 0.1,
      "end": 0.9
    }
  ]
}
`

	tests := []struct {
		name   string
		toFile bool
	}{
		{name: "stdout"},
		{name: "file", toFile: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer

			path := ""
			if tt.toFile {
				path = filepath.Join(t.TempDir(), "result.json")
			}

			require.NoError(t, writeResult(result, path, &stdout))

			if !tt.toFile {
				assert.Equal(t, expected, stdout.String())

				return
			}

			content, err := os.ReadFile(filepath.Clean(path))
			require.NoError(t, err)
			assert.Equal(t, expected, string(content))
			assert.Empty(t, stdout.String())
		})
	}
}
