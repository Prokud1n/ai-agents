package ffmpeg

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testAudio = "file:/tmp/audio.mp3"
	testVideo = "file:/in/video one.mp4"
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
	input := func(path string) []string {
		return []string{"-protocol_whitelist", "file", inputFlag, path}
	}

	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "extract audio",
			input:    extractAudioArgs(testVideo, testAudio),
			expected: concat(common, input(testVideo), []string{"-map", "0:a:0"}, encode, []string{testAudio}),
		},
		{
			name:  "cut audio",
			input: cutAudioArgs(testAudio, "file:/tmp/chunk-1.mp3", 598.25, 601.5),
			expected: concat(
				common, []string{"-ss", "598.250", "-t", "601.500"}, input(testAudio), encode, []string{"file:/tmp/chunk-1.mp3"},
			),
		},
		{
			name:  "probe duration reads the input through -i",
			input: durationArgs(testAudio),
			expected: concat(
				[]string{"-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1"},
				input(testAudio),
			),
		},
		{
			name:  "detect silences with configured level",
			input: silenceDetectArgs(testAudio, SilenceOptions{NoiseDB: -25, MinDuration: 300 * time.Millisecond}),
			expected: concat(
				common, input(testAudio), []string{"-af", "silencedetect=noise=-25.000dB:d=0.300", "-f", "null", "-"},
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

func TestLocalFile(t *testing.T) {
	t.Parallel()

	cwd, err := os.Getwd()
	require.NoError(t, err)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "absolute path", input: "/data/video.mp4", expected: "file:/data/video.mp4"},
		{name: "relative path", input: "video.mp4", expected: "file:" + filepath.Join(cwd, "video.mp4")},
		{name: "leading dash is not an option", input: "-version", expected: "file:" + filepath.Join(cwd, "-version")},
		{name: "url is a file name", input: "http://example.com/x.mp4", expected: "file:" + filepath.Join(cwd, "http:/example.com/x.mp4")},
		{name: "concat protocol is a file name", input: "concat:/etc/passwd|b.mp4", expected: "file:" + filepath.Join(cwd, "concat:/etc/passwd|b.mp4")},
		{name: "file prefix is a file name", input: "file:/etc/passwd", expected: "file:" + filepath.Join(cwd, "file:/etc/passwd")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := localFile(tt.input)

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
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
