package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
)

// requireFFmpegEnv makes the test fail instead of skipping when ffmpeg is missing (set in CI).
const requireFFmpegEnv = "FFMPEG_TESTS_REQUIRED"

// MP3 encoder padding makes ffprobe report ~0.1 s more than the source; pauses found by silencedetect
// on decoded audio stay exact.
const timeDelta = 0.15 // seconds

func requireFFmpeg(t *testing.T) {
	t.Helper()

	_, ffmpegErr := exec.LookPath(ffmpegBinary)
	_, ffprobeErr := exec.LookPath(ffprobeBinary)

	if ffmpegErr == nil && ffprobeErr == nil {
		return
	}

	if os.Getenv(requireFFmpegEnv) != "" {
		t.Fatalf("ffmpeg/ffprobe not found in PATH: %v, %v", ffmpegErr, ffprobeErr)
	}

	t.Skip("ffmpeg/ffprobe not found in PATH")
}

func lavfiInput(graph string) []string {
	return []string{"-f", "lavfi", inputFlag, graph}
}

func newTestRunner(timeout time.Duration) *Runner {
	return NewRunner(&config.FFmpeg{Timeout: timeout})
}

// generateVideo makes a test clip of the given length with a picture and a 440 Hz tone that pauses in [2, 3) s.
func generateVideo(ctx context.Context, t *testing.T, r *Runner, path string, seconds int) {
	t.Helper()

	d := strconv.Itoa(seconds)
	args := concat(
		commonArgs(),
		lavfiInput("aevalsrc=if(between(t\\,2\\,3)\\,0\\,0.5*sin(2*PI*440*t)):s=44100:d="+d),
		lavfiInput("testsrc=duration="+d+":size=160x120:rate=10"),
		[]string{"-c:v", "mpeg4", "-c:a", "aac", "-shortest", path},
	)

	_, err := r.run(ctx, r.ffmpegPath, args, nil)
	require.NoError(t, err)
}

func TestRunnerWithFFmpeg(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	ctx := t.Context()
	r := newTestRunner(time.Minute)
	dir := t.TempDir()

	video := filepath.Join(dir, "video.mp4")
	generateVideo(ctx, t, r, video, 5)

	audio := filepath.Join(dir, "audio"+AudioExtension)
	require.NoError(t, r.ExtractAudio(ctx, video, audio))

	duration, err := r.Duration(ctx, audio)
	require.NoError(t, err)
	assert.InDelta(t, 5.0, duration, timeDelta)

	silences, err := r.DetectSilences(ctx, audio, SilenceOptions{NoiseDB: -30, MinDuration: 500 * time.Millisecond})
	require.NoError(t, err)
	require.Len(t, silences, 1)
	assert.InDelta(t, 2.0, silences[0].Start, timeDelta)
	assert.InDelta(t, 3.0, silences[0].End, timeDelta)

	chunk := filepath.Join(dir, "chunk"+AudioExtension)
	require.NoError(t, r.CutAudio(ctx, audio, chunk, 1.5, 2))

	chunkDuration, err := r.Duration(ctx, chunk)
	require.NoError(t, err)
	assert.InDelta(t, 2.0, chunkDuration, timeDelta)

	t.Logf("ffmpeg: extracted %.3f s of audio, silences %+v, cut chunk %.3f s", duration, silences, chunkDuration)
}

// Telegram file names are user input: names that look like options or protocols must stay plain local files.
// Not parallel: t.Chdir makes the names relative, exactly as a user would pass them.
func TestRunnerHostileFileNames(t *testing.T) {
	requireFFmpeg(t)

	ctx := t.Context()
	r := newTestRunner(time.Minute)
	dir := t.TempDir()
	t.Chdir(dir)

	generateVideo(ctx, t, r, "source.mp4", 1)

	content, err := os.ReadFile("source.mp4")
	require.NoError(t, err)

	tests := []struct {
		name  string
		input string
	}{
		{name: "leading dash", input: "-version.mp4"},
		{name: "http prefix", input: "http:evil.mp4"},
		{name: "concat prefix", input: "concat:source.mp4|source.mp4"},
		{name: "quotes and separators", input: "x';[0]afade.mp4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(tt.input, content, 0o600))

			out := "-out-" + strconv.Itoa(len(tt.name)) + AudioExtension
			require.NoError(t, r.ExtractAudio(ctx, tt.input, out))

			duration, err := r.Duration(ctx, out)
			require.NoError(t, err)
			// concat: would have joined two copies (2 s) if it were read as a protocol.
			assert.InDelta(t, 1.0, duration, timeDelta)
		})
	}
}

func TestRunnerErrors(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	ctx := t.Context()
	r := newTestRunner(time.Minute)
	dir := t.TempDir()

	// A video without an audio track: extraction must fail and carry ffmpeg's reason.
	silentVideo := filepath.Join(dir, "silent.mp4")
	_, err := r.run(ctx, r.ffmpegPath, concat(
		commonArgs(), lavfiInput("testsrc=duration=1:size=160x120:rate=10"), []string{"-c:v", "mpeg4", silentVideo},
	), nil)
	require.NoError(t, err)

	long := filepath.Join(dir, "long.mp4")
	generateVideo(ctx, t, r, long, 30)

	tests := []struct {
		name     string
		runner   *Runner
		input    string
		expected string
	}{
		{name: "missing file", runner: r, input: filepath.Join(dir, "missing.mp4"), expected: "No such file or directory"},
		{name: "no audio stream", runner: r, input: silentVideo, expected: "matches no streams"},
		{name: "timeout", runner: newTestRunner(time.Millisecond), input: long, expected: "ffmpeg timed out"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.runner.ExtractAudio(ctx, tt.input, filepath.Join(dir, tt.name+AudioExtension))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expected)
		})
	}
}
