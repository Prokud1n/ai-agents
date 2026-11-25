package subtitles

import (
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// referenceToneMap is the order of the graph before T3.11: tone map the full-size HDR frame, then scale it with the
// conversion to the bt709 matrix. The render now scales first; the colours must stay the same.
const referenceToneMap = "zscale=t=linear:npl=100,format=gbrpf32le,zscale=p=bt709,tonemap=hable:desat=0," +
	"zscale=t=bt709:m=bt709:r=tv,format=yuv420p," +
	"scale=1080:1920:force_original_aspect_ratio=increase:force_divisible_by=2:out_color_matrix=bt709:out_range=tv," +
	"crop=1080:1920,format=rgb24"

const (
	// compareFromRow: the bottom of the frame, away from the shade and the subtitles at the top.
	compareFromRow = FrameHeight * 5 / 8
	rgbChannels    = 3
	// maxMeanAbsDiff and maxMeanShift are in levels of 0..255: rounding of a different order of filters and the
	// H.264 encode, far below a visible change of colour or brightness.
	maxMeanAbsDiff = 4.0
	maxMeanShift   = 1.5
)

// TestBurnSubtitlesWithFFmpegToneMap: an HDR picture (HLG of an iPhone, PQ of HDR10) is scaled to 1080×1920 first and
// tone mapped after; the reel looks the same as the full-size tone mapping that came before it.
func TestBurnSubtitlesWithFFmpegToneMap(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	ctx := t.Context()
	runner := ffmpeg.NewRunner(&config.FFmpeg{Timeout: 2 * time.Minute})
	svc, err := NewService(runner, &config.Subtitles{FontsDir: testFontsDir()}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	tests := []struct {
		name     string
		transfer string
	}{
		{name: "HLG", transfer: "arib-std-b67"},
		{name: "PQ", transfer: "smpte2084"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			source := filepath.Join(dir, "source.mkv")
			reel := filepath.Join(dir, "reel.mp4")

			// Larger than the reel and of another aspect, so the frame is really scaled and cropped.
			runTool(ctx, t, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-y",
				"-f", "lavfi", "-i", "testsrc2=size=1600x2560:rate=30:duration=1",
				"-vf", "format=yuv420p10le,setparams=color_primaries=bt2020:color_trc="+tt.transfer+":colorspace=bt2020nc",
				"-c:v", "ffv1", "-pix_fmt", "yuv420p10le",
				"-color_primaries", "bt2020", "-color_trc", tt.transfer, "-colorspace", "bt2020nc", source)

			_, err := svc.BurnSubtitles(ctx, &BurnSubtitlesData{
				CreateSubtitlesData: CreateSubtitlesData{Transcript: &transcription.Result{Language: "ru"}},
				VideoPath:           source, OutputPath: reel, Fit: ffmpeg.CropFitMode, ToneMap: true,
			})
			require.NoError(t, err)

			expected := runTool(ctx, t, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-i", source,
				"-frames:v", "1", "-vf", referenceToneMap, "-f", "rawvideo", "-")
			got := runTool(ctx, t, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-i", reel,
				"-frames:v", "1", "-vf", "format=rgb24", "-f", "rawvideo", "-")

			size := FrameWidth * FrameHeight * rgbChannels
			require.Len(t, expected, size)
			require.Len(t, got, size)

			from := compareFromRow * FrameWidth * rgbChannels
			absDiff, shift := frameDiff(expected[from:], got[from:])

			t.Logf("%s: mean |difference| %.2f, mean shift %.2f levels against tone mapping before scaling",
				tt.name, absDiff, shift)
			assert.Less(t, absDiff, maxMeanAbsDiff)
			assert.Less(t, shift, maxMeanShift)
			assert.Greater(t, shift, -maxMeanShift)
		})
	}
}

// frameDiff returns the mean absolute difference of two frames and the mean of got minus expected.
func frameDiff(expected, got []byte) (absDiff, shift float64) {
	var sumAbs, sum int

	for i := range expected {
		d := int(got[i]) - int(expected[i])
		sum += d
		sumAbs += max(d, -d)
	}

	n := float64(len(expected))

	return float64(sumAbs) / n, float64(sum) / n
}
