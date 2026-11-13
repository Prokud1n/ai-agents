package ffmpeg

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func concat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}

	return out
}

func TestBuildArgs(t *testing.T) {
	t.Parallel()

	common := []string{"-hide_banner", "-nostdin", "-nostats", "-y"}
	encode := []string{"-vn", "-ac", "1", "-ar", "16000", "-c:a", "libmp3lame", "-b:a", "32k"}

	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:  "extract audio",
			input: extractAudioArgs("/in/video one.mp4", "/tmp/audio.mp3"),
			expected: concat(
				common, []string{inputFlag, "/in/video one.mp4", "-map", "0:a:0"}, encode, []string{"/tmp/audio.mp3"},
			),
		},
		{
			name:  "cut audio",
			input: cutAudioArgs("/tmp/audio.mp3", "/tmp/chunk-001.mp3", 598.25, 601.5),
			expected: concat(
				common, []string{"-ss", "598.250", "-t", "601.500", inputFlag, "/tmp/audio.mp3"},
				encode, []string{"/tmp/chunk-001.mp3"},
			),
		},
		{
			name:  "probe duration",
			input: durationArgs("/tmp/audio.mp3"),
			expected: []string{
				"-v", "error", "-show_entries", "format=duration",
				"-of", "default=noprint_wrappers=1:nokey=1", "/tmp/audio.mp3",
			},
		},
		{
			name:  "detect silences",
			input: silenceDetectArgs("/tmp/audio.mp3"),
			expected: concat(
				common, []string{inputFlag, "/tmp/audio.mp3", "-af", "silencedetect=noise=-30dB:d=0.5", "-f", "null", "-"},
			),
		},
		{
			name:  "shell metacharacters stay one argument",
			input: extractAudioArgs("/in/$(rm -rf ~); echo `id`.mp4", "/tmp/a b.mp3"),
			expected: concat(
				common, []string{inputFlag, "/in/$(rm -rf ~); echo `id`.mp4", "-map", "0:a:0"}, encode, []string{"/tmp/a b.mp3"},
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, tt.input)
		})
	}
}

func TestParseDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expected  float64
		expectErr bool
	}{
		{name: "seconds with fraction", input: "12.345000\n", expected: 12.345},
		{name: "integer", input: "60", expected: 60},
		{name: "empty", input: " \n", expectErr: true},
		{name: "not available", input: "N/A\n", expectErr: true},
		{name: "garbage", input: "abc", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseDuration(tt.input)
			if tt.expectErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.InDelta(t, tt.expected, got, 1e-9)
		})
	}
}

func TestParseSilences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []Silence
	}{
		{name: "no silences", input: "Input #0, mp3, from 'a.mp3':\n", expected: []Silence{}},
		{
			name: "two silences",
			input: "Input #0, mp3, from 'a.mp3':\n" +
				"[silencedetect @ 0x55d0c8] silence_start: 1.00475\n" +
				"[silencedetect @ 0x55d0c8] silence_end: 2.0055 | silence_duration: 1.00075\n" +
				"size=N/A time=00:00:05.00 bitrate=N/A speed= 500x\n" +
				"[silencedetect @ 0x55d0c8] silence_start: 3.5\n" +
				"[silencedetect @ 0x55d0c8] silence_end: 4.25 | silence_duration: 0.75\n",
			expected: []Silence{{Start: 1.00475, End: 2.0055}, {Start: 3.5, End: 4.25}},
		},
		{
			name: "silence at the very start has a negative start",
			input: "[silencedetect @ 0x1] silence_start: -0.0123\n" +
				"[silencedetect @ 0x1] silence_end: 0.8 | silence_duration: 0.81\n",
			expected: []Silence{{Start: 0, End: 0.8}},
		},
		{
			name: "silence open at the end is skipped",
			input: "[silencedetect @ 0x1] silence_start: 1\n" +
				"[silencedetect @ 0x1] silence_end: 2 | silence_duration: 1\n" +
				"[silencedetect @ 0x1] silence_start: 9.5\n",
			expected: []Silence{{Start: 1, End: 2}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, parseSilences(tt.input))
		})
	}
}

func TestTailLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		n        int
		expected string
	}{
		{name: "empty", input: "", n: 3, expected: ""},
		{name: "fewer lines than limit", input: "a\nb\n", n: 3, expected: "a | b"},
		{name: "keeps the last lines", input: "a\nb\n\nc\nd\n", n: 2, expected: "c | d"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, tailLines(tt.input, tt.n))
		})
	}
}
