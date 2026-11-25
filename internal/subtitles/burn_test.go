package subtitles

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// requireFFmpegEnv makes the test fail instead of skipping when ffmpeg is missing (set in CI).
const requireFFmpegEnv = "FFMPEG_TESTS_REQUIRED"

// centerTolerance allows for glyph side bearings and the shadow offset, in pixels.
const centerTolerance = 40

const reelSize = "1080x1920"

// brightLevel: a pixel of white or yellow text with its outline, on a black frame.
const brightLevel = 128

func requireFFmpeg(t *testing.T) {
	t.Helper()

	_, ffmpegErr := exec.LookPath("ffmpeg")
	_, ffprobeErr := exec.LookPath("ffprobe")

	if ffmpegErr == nil && ffprobeErr == nil {
		return
	}

	if os.Getenv(requireFFmpegEnv) != "" {
		t.Fatalf("ffmpeg/ffprobe not found in PATH: %v, %v", ffmpegErr, ffprobeErr)
	}

	t.Skip("ffmpeg/ffprobe not found in PATH")
}

// testFontsDir is SUBTITLES_FONTS_DIR when set (the media image: /app/fonts), otherwise the repository fonts.
func testFontsDir() string {
	if dir := os.Getenv("SUBTITLES_FONTS_DIR"); dir != "" {
		return dir
	}

	return repoFontsDir
}

// runTool runs ffmpeg or ffprobe and returns its stdout; stderr goes into the failure message.
func runTool(ctx context.Context, t *testing.T, name string, args ...string) []byte {
	t.Helper()

	cmd := exec.CommandContext(ctx, name, args...)

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), stderr.String())

	return stdout.Bytes()
}

// makeClip generates a black clip with a tone, of the given size.
func makeClip(ctx context.Context, t *testing.T, path, size string, withAudio bool) {
	t.Helper()

	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=black:s=" + size + ":d=3:r=25"}
	if withAudio {
		args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:duration=3")
	}

	args = append(args, "-c:v", "libx264", "-pix_fmt", "yuv420p")
	if withAudio {
		args = append(args, "-c:a", "aac", "-shortest")
	}

	runTool(ctx, t, "ffmpeg", append(args, path)...)
}

type probedStream struct {
	CodecType string `json:"codec_type"`
	CodecName string `json:"codec_name"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	PixFmt    string `json:"pix_fmt"`
}

func probeStreams(ctx context.Context, t *testing.T, path string) []probedStream {
	t.Helper()

	out := runTool(ctx, t, "ffprobe", "-v", "error",
		"-show_entries", "stream=codec_type,codec_name,width,height,pix_fmt", "-of", "json", path)

	var probe struct {
		Streams []probedStream `json:"streams"`
	}

	require.NoError(t, json.Unmarshal(out, &probe))

	return probe.Streams
}

// grayFrame decodes one frame at the given second as 8-bit gray pixels, row by row.
func grayFrame(ctx context.Context, t *testing.T, path string, at float64) []byte {
	t.Helper()

	frame := runTool(ctx, t, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error",
		"-ss", strconv.FormatFloat(at, 'f', 2, 64), "-i", path, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "gray", "-")
	require.Len(t, frame, FrameWidth*FrameHeight)

	return frame
}

// brightBounds returns the box around all bright pixels of a frame.
func brightBounds(frame []byte) (top, bottom, left, right int, found bool) {
	top, left = FrameHeight, FrameWidth

	for y := range FrameHeight {
		for x := range FrameWidth {
			if frame[y*FrameWidth+x] < brightLevel {
				continue
			}

			found = true
			top, bottom = min(top, y), max(bottom, y)
			left, right = min(left, x), max(right, x)
		}
	}

	return top, bottom, left, right, found
}

func TestBurnSubtitlesWithFFmpeg(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	ctx := t.Context()
	runner := ffmpeg.NewRunner(&config.FFmpeg{Timeout: 2 * time.Minute})
	svc, err := NewService(runner, &config.Subtitles{FontsDir: testFontsDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	transcript := speech("Делаем 3 подхода по 10 повторений, держим спину!")
	// ASS markup in the speech must be shown as text: no top-left alignment, no line break.
	markup := &transcription.Result{
		Segments: []transcription.Segment{{Start: 0, End: 2, Text: "{\\an7}Держим \\N спину!"}},
		Words: []transcription.Word{
			{Word: "{\\an7}Держим", Start: 0.2, End: 0.8}, {Word: "\\N", Start: 0.8, End: 1}, {Word: "спину", Start: 1, End: 1.8},
		},
	}

	tests := []struct {
		name       string
		file       string
		size       string
		withAudio  bool
		fit        ffmpeg.FitMode
		karaoke    bool
		transcript *transcription.Result
	}{
		{name: "reels size with audio", file: "reel.mp4", size: reelSize, withAudio: true},
		{name: "landscape is padded", file: "landscape.mp4", size: "640x360", fit: ffmpeg.PadFitMode},
		{name: "smaller vertical is cropped", file: "small.mp4", size: "720x1280", fit: ffmpeg.CropFitMode, karaoke: true},
		{name: "hostile file name", file: "-x,[in];ass=f'1:fontsdir=.mp4", size: reelSize, withAudio: true},
		{name: "ASS markup in the speech", file: "markup.mp4", size: reelSize, transcript: markup},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			input := filepath.Join(dir, tt.file)
			output := filepath.Join(dir, "out-"+tt.file)
			makeClip(ctx, t, input, tt.size, tt.withAudio)

			speechInput := transcript
			if tt.transcript != nil {
				speechInput = tt.transcript
			}

			// The phrase layout: its lines are centered (the word layout has a test of its own).
			phrase := PhraseStyle()

			result, err := svc.BurnSubtitles(ctx, &BurnSubtitlesData{
				CreateSubtitlesData: CreateSubtitlesData{Transcript: speechInput, Karaoke: tt.karaoke, Style: &phrase},
				VideoPath:           input, OutputPath: output, Fit: tt.fit,
			})
			require.NoError(t, err)

			assert.Empty(t, result.FontFallbacks, "libass must use the brand fonts from the fonts dir")
			assert.Empty(t, result.MissingGlyphs)

			streams := probeStreams(ctx, t, output)
			expected := []probedStream{{CodecType: "video", CodecName: "h264", Width: FrameWidth, Height: FrameHeight, PixFmt: "yuv420p"}}

			if tt.withAudio {
				expected = append(expected, probedStream{CodecType: "audio", CodecName: "aac"})
			}

			assert.Equal(t, expected, streams)

			// The first subtitle is on screen at 1 s: all text is in the top band, outside the Instagram zones.
			top, bottom, left, right, found := brightBounds(grayFrame(ctx, t, output, 1))
			require.True(t, found, "subtitles are drawn")
			assert.GreaterOrEqual(t, top, SafeZoneTop)
			assert.Less(t, bottom, FrameHeight/2)
			assert.Greater(t, left, SafeZoneLeft)
			assert.Less(t, right, FrameWidth-SafeZoneRight)
			assert.InDelta(t, FrameWidth/2, (left+right)/2, centerTolerance, "centered, not moved by markup")
		})
	}
}
