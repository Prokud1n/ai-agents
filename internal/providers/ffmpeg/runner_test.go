package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requireFFmpegEnv makes the test fail instead of skipping when ffmpeg is missing (set in CI).
const requireFFmpegEnv = "FFMPEG_TESTS_REQUIRED"

const timeDelta = 0.1 // seconds: MP3 frames and encoder padding shift durations slightly

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
	return append([]string{"-f", "lavfi"}, inputArgs(graph)...)
}

// generateVideo makes a 5 s test clip: a 440 Hz tone with a pause in [2, 3) s and a test pattern picture.
func generateVideo(ctx context.Context, t *testing.T, r *Runner, path string) {
	t.Helper()

	args := concat(
		commonArgs(),
		lavfiInput("aevalsrc=if(between(t\\,2\\,3)\\,0\\,0.5*sin(2*PI*440*t)):s=44100:d=5"),
		lavfiInput("testsrc=duration=5:size=160x120:rate=10"),
		[]string{"-c:v", "mpeg4", "-c:a", "aac", "-shortest", path},
	)

	_, err := r.run(ctx, r.ffmpegPath, args)
	require.NoError(t, err)
}

func TestRunnerWithFFmpeg(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	ctx := t.Context()
	r := NewRunner()
	dir := t.TempDir()

	video := filepath.Join(dir, "video.mp4")
	generateVideo(ctx, t, r, video)

	audio := filepath.Join(dir, "audio"+AudioExtension)
	require.NoError(t, r.ExtractAudio(ctx, video, audio))

	duration, err := r.Duration(ctx, audio)
	require.NoError(t, err)
	assert.InDelta(t, 5.0, duration, timeDelta)

	silences, err := r.DetectSilences(ctx, audio)
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

func TestRunnerErrors(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	ctx := t.Context()
	r := NewRunner()
	dir := t.TempDir()

	// A video without an audio track: extraction must fail and carry ffmpeg's reason.
	silentVideo := filepath.Join(dir, "silent.mp4")
	_, err := r.run(ctx, r.ffmpegPath, concat(
		commonArgs(), lavfiInput("testsrc=duration=1:size=160x120:rate=10"), []string{"-c:v", "mpeg4", silentVideo},
	))
	require.NoError(t, err)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "missing file", input: filepath.Join(dir, "missing.mp4"), expected: "No such file or directory"},
		{name: "no audio stream", input: silentVideo, expected: "matches no streams"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := r.ExtractAudio(ctx, tt.input, filepath.Join(dir, tt.name+AudioExtension))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.expected)
		})
	}
}
