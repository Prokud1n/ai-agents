package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const (
	ffmpegBinary  = "ffmpeg"
	ffprobeBinary = "ffprobe"

	stderrTailLines = 15 // lines of ffmpeg stderr kept in an error
)

var errEmptyOutput = errors.New("empty output")

// Runner runs ffmpeg and ffprobe found in PATH. Arguments are always passed as a list, never through a shell.
type Runner struct {
	ffmpegPath  string
	ffprobePath string
}

func NewRunner() *Runner {
	return &Runner{ffmpegPath: ffmpegBinary, ffprobePath: ffprobeBinary}
}

// ExtractAudio writes the first audio stream of src to dst as mono 16 kHz MP3 (see AudioFormat).
func (r *Runner) ExtractAudio(ctx context.Context, src, dst string) error {
	if _, err := r.run(ctx, r.ffmpegPath, extractAudioArgs(src, dst)); err != nil {
		return fmt.Errorf("failed to extract audio: %w", err)
	}

	return nil
}

// CutAudio re-encodes the [start, start+duration) fragment of src (in seconds) to dst.
// Re-encoding instead of stream copy keeps the cut sample-accurate, so timestamps stay aligned.
func (r *Runner) CutAudio(ctx context.Context, src, dst string, start, duration float64) error {
	if _, err := r.run(ctx, r.ffmpegPath, cutAudioArgs(src, dst, start, duration)); err != nil {
		return fmt.Errorf("failed to cut audio: %w", err)
	}

	return nil
}

// Duration returns the media duration in seconds reported by ffprobe.
func (r *Runner) Duration(ctx context.Context, path string) (float64, error) {
	out, err := r.run(ctx, r.ffprobePath, durationArgs(path))
	if err != nil {
		return 0, fmt.Errorf("failed to probe duration: %w", err)
	}

	duration, err := parseDuration(out.stdout)
	if err != nil {
		return 0, fmt.Errorf("failed to parse duration: %w", err)
	}

	return duration, nil
}

// DetectSilences returns pauses found by the silencedetect filter.
func (r *Runner) DetectSilences(ctx context.Context, path string) ([]Silence, error) {
	out, err := r.run(ctx, r.ffmpegPath, silenceDetectArgs(path))
	if err != nil {
		return nil, fmt.Errorf("failed to detect silences: %w", err)
	}

	return parseSilences(out.stderr), nil
}

type output struct {
	stdout string
	stderr string
}

func (*Runner) run(ctx context.Context, binary string, args []string) (output, error) {
	var stdout, stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return output{}, fmt.Errorf("failed to run %s: %w: %s", binary, err, tailLines(stderr.String(), stderrTailLines))
	}

	return output{stdout: stdout.String(), stderr: stderr.String()}, nil
}

func parseDuration(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0, fmt.Errorf("failed to read ffprobe duration: %w", errEmptyOutput)
	}

	duration, err := strconv.ParseFloat(s, float64Bits)
	if err != nil {
		return 0, fmt.Errorf("failed to parse ffprobe duration %q: %w", s, err)
	}

	return duration, nil
}

// tailLines returns the last n non-empty lines of s joined with " | ": enough to see why ffmpeg failed
// without dumping its whole log into the error.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")

	kept := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0 && len(kept) < n; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			kept = append(kept, line)
		}
	}

	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}

	return strings.Join(kept, " | ")
}
