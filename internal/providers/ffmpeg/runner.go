package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Prokud1n/ai-agents/internal/config"
)

const (
	ffmpegBinary  = "ffmpeg"
	ffprobeBinary = "ffprobe"

	stderrTailSize  = 4 << 10 // bytes of ffmpeg stderr kept to explain a failure
	stderrTailLines = 15      // of them, lines put into the error
	stdoutLimit     = 4 << 10 // ffprobe prints one number

	silenceMarker   = "silence_"
	maxSilenceLines = 100_000 // two lines per pause: far more than any video has
	maxLogLine      = 4 << 10
)

var (
	errEmptyOutput = errors.New("empty output")
	errTimeout     = errors.New("ffmpeg timed out")
)

// Runner runs ffmpeg and ffprobe found in PATH. Arguments are always a list, never a shell string;
// inputs are local files only (see localFile and inputArgs).
type Runner struct {
	ffmpegPath  string
	ffprobePath string
	timeout     time.Duration
}

func NewRunner(cfg *config.FFmpeg) *Runner {
	return &Runner{ffmpegPath: ffmpegBinary, ffprobePath: ffprobeBinary, timeout: cfg.Timeout}
}

// ExtractAudio writes the first audio stream of src to dst as mono 16 kHz MP3.
func (r *Runner) ExtractAudio(ctx context.Context, src, dst string) error {
	in, out, err := localFiles(src, dst)
	if err != nil {
		return err
	}

	if _, err := r.run(ctx, r.ffmpegPath, extractAudioArgs(in, out), nil); err != nil {
		return fmt.Errorf("failed to extract audio: %w", err)
	}

	return nil
}

// CutAudio re-encodes the [start, start+duration) fragment of src (in seconds) to dst.
// Re-encoding instead of stream copy keeps the cut sample-accurate, so timestamps stay aligned.
func (r *Runner) CutAudio(ctx context.Context, src, dst string, start, duration float64) error {
	in, out, err := localFiles(src, dst)
	if err != nil {
		return err
	}

	if _, err := r.run(ctx, r.ffmpegPath, cutAudioArgs(in, out, start, duration), nil); err != nil {
		return fmt.Errorf("failed to cut audio: %w", err)
	}

	return nil
}

// Duration returns the media duration in seconds reported by ffprobe.
func (r *Runner) Duration(ctx context.Context, path string) (float64, error) {
	in, err := localFile(path)
	if err != nil {
		return 0, err
	}

	stdout, err := r.run(ctx, r.ffprobePath, durationArgs(in), nil)
	if err != nil {
		return 0, fmt.Errorf("failed to probe duration: %w", err)
	}

	duration, err := parseDuration(stdout)
	if err != nil {
		return 0, fmt.Errorf("failed to parse duration: %w", err)
	}

	return duration, nil
}

// DetectSilences returns pauses found by the silencedetect filter.
func (r *Runner) DetectSilences(ctx context.Context, path string, opts SilenceOptions) ([]Silence, error) {
	in, err := localFile(path)
	if err != nil {
		return nil, err
	}

	collector := &lineCollector{marker: silenceMarker, maxLines: maxSilenceLines, maxLine: maxLogLine}

	if _, err := r.run(ctx, r.ffmpegPath, silenceDetectArgs(in, opts), collector); err != nil {
		return nil, fmt.Errorf("failed to detect silences: %w", err)
	}

	return parseSilences(collector.String()), nil
}

func localFiles(src, dst string) (in, out string, err error) {
	in, err = localFile(src)
	if err != nil {
		return "", "", err
	}

	out, err = localFile(dst)
	if err != nil {
		return "", "", err
	}

	return in, out, nil
}

// run executes a binary with a timeout. stderr goes to a fixed-size tail buffer (and to stderrSink, if set).
func (r *Runner) run(parent context.Context, binary string, args []string, stderrSink io.Writer) (string, error) {
	return r.execute(parent, binary, args, stderrSink, newTailBuffer(stdoutLimit))
}

// runLimited is run whose stdout may be up to limit bytes (a JSON probe); longer output is cut from the start.
func (r *Runner) runLimited(parent context.Context, binary string, args []string, limit int) (string, error) {
	return r.execute(parent, binary, args, nil, newTailBuffer(limit))
}

func (r *Runner) execute(
	parent context.Context,
	binary string,
	args []string,
	stderrSink io.Writer,
	stdout outputBuffer,
) (string, error) {
	ctx := parent

	if r.timeout > 0 {
		var cancel context.CancelFunc

		ctx, cancel = context.WithTimeoutCause(parent, r.timeout, errTimeout)
		defer cancel()
	}

	stderr := newTailBuffer(stderrTailSize)

	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if stderrSink != nil {
		cmd.Stderr = io.MultiWriter(stderr, stderrSink)
	}

	if err := cmd.Run(); err != nil {
		err = explainStop(parent, ctx, r.timeout, err)

		return "", fmt.Errorf("failed to run %s: %w: %s", binary, err, tailLines(stderr.String(), stderrTailLines))
	}

	return stdout.String(), nil
}

// explainStop tells apart the runner's own timeout (FFMPEG_TIMEOUT) and the caller's context
// (its deadline or cancellation), so a slow file and an impatient caller are not reported the same way.
func explainStop(parent, ctx context.Context, timeout time.Duration, err error) error {
	switch {
	case parent.Err() != nil:
		return fmt.Errorf("stopped by the caller's context (%w): %w", context.Cause(parent), err)
	case errors.Is(context.Cause(ctx), errTimeout):
		return fmt.Errorf("%w after %s (FFMPEG_TIMEOUT): %w", errTimeout, timeout, err)
	default:
		return err
	}
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
