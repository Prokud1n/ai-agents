package mediaworker

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/subtitles"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// repoFontsDir is the brand fonts of the repository; the media image has them in SUBTITLES_FONTS_DIR (/app/fonts).
const repoFontsDir = "../../assets/fonts"

// clipDuration of the synthetic source: four tone "words", one of them a filler, with long pauses between.
const clipDuration = 6

var clipWords = []transcription.Word{
	{Word: "Привет", Start: 0.5, End: 1},
	{Word: "эээ", Start: 1.1, End: 1.6},
	{Word: "это", Start: 3, End: 3.5},
	{Word: "тест", Start: 3.6, End: 4.2},
}

// TestIntegrationPipeline runs the whole pipeline with the real ffmpeg, libass and brand fonts on synthetic clips
// (the speech recognition is faked): the reel is 1080×1920 H.264 High yuv420p with AAC 48 kHz, the moov atom first,
// the frame rate of the source, as long as the plan (pauses and the filler cut) and rendered with the brand fonts.
// A horizontal source is padded into the frame, not stretched.
func TestIntegrationPipeline(t *testing.T) {
	t.Parallel()
	requireIntegrationFFmpeg(t)

	tests := []struct {
		name   string
		size   string
		rate   int
		padded bool
		hdr    bool
	}{
		{name: "vertical 720p at 30 fps fills the frame", size: "720x1280", rate: 30},
		{name: "horizontal at 25 fps is padded", size: "640x360", rate: 25, padded: true},
		// An iPhone-like HDR source: 10-bit HLG (bt2020, arib-std-b67) is tone mapped to an 8-bit SDR bt709 reel.
		{name: "10-bit HLG is tone mapped to SDR bt709", size: "720x1280", rate: 30, hdr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			source := filepath.Join(t.TempDir(), "source.mkv")
			makeSpeechClip(t, source, tt.size, tt.rate, tt.hdr)

			store := newLocalStore(map[string]string{testSourceKey: source})
			started := time.Now()

			result, err := realPipeline(t, store).Run(t.Context(), testJob())
			require.NoError(t, err)

			t.Logf("pipeline: %.1f s for a %d s source, reel %.2f s, cuts %d", time.Since(started).Seconds(),
				clipDuration, result.Duration, result.Cuts)

			assert.Equal(t, editjob.DoneStatus, result.Status)
			assert.Less(t, result.Duration, 3.0, "pauses and the filler are cut")

			files, err := editjob.Files(store, testSourceKey, 1)
			require.NoError(t, err)

			reel := filepath.Join(t.TempDir(), "reel.mp4")
			require.NoError(t, os.WriteFile(reel, store.results[files.Reel].data, 0o600))

			checkReel(t, reel, tt.rate, result.Duration)

			// The bottom of the frame: the picture of a vertical source, black bars under a horizontal one.
			bottom := rowMean(t, reel, bottomRow)
			if tt.padded {
				assert.Less(t, bottom, darkLevel, "a horizontal source is padded, not stretched or cropped")
			} else {
				assert.Greater(t, bottom, darkLevel, "a vertical source fills the frame")
			}
			assert.Contains(t, string(store.results[files.Plan].data), `"keep"`)
			assert.Contains(t, string(store.results[files.Transcript].data), "эээ", "the source transcript, before the cut")
		})
	}
}

func requireIntegrationFFmpeg(t *testing.T) {
	t.Helper()

	if os.Getenv("INTEGRATION") != "1" {
		t.Skip("integration test: run with INTEGRATION=1 and ffmpeg with libass (make test-integration)")
	}

	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		_, err := exec.LookPath(tool)
		require.NoError(t, err, "%s is required with INTEGRATION=1", tool)
	}
}

func realPipeline(t *testing.T, store *localStore) *Pipeline {
	t.Helper()

	log := slog.New(slog.DiscardHandler)
	runner := ffmpeg.NewRunner(&config.FFmpeg{Timeout: 2 * time.Minute})

	cutter, err := roughcut.NewService(runner, &config.RoughCut{
		MaxPause: 400 * time.Millisecond, RetakeSimilarity: 0.7, Fillers: []string{"э", "эм"},
	}, log)
	require.NoError(t, err)

	fonts := os.Getenv("SUBTITLES_FONTS_DIR")
	if fonts == "" {
		fonts = repoFontsDir
	}

	burner, err := subtitles.NewService(runner, &config.Subtitles{FontsDir: fonts}, log)
	require.NoError(t, err)

	p, err := NewPipeline(&PipelineParams{
		Store: store, Prober: runner, Keys: fakeKeys{},
		Transcriber: &fakeTranscriber{result: &transcription.Result{
			Language: "ru", Duration: clipDuration, Text: "Привет! эээ это тест", Words: clipWords,
			Segments: []transcription.Segment{{Start: 0.5, End: 4.2, Text: "Привет! эээ это тест"}},
		}},
		Cutter: cutter, Burner: burner,
		Config: &config.MediaWorker{
			WorkDir: t.TempDir(), MaxSourceDuration: time.Minute, MaxSourceSize: 1 << 30, Fit: string(ffmpeg.PadFitMode),
		},
		Log: log,
	})
	require.NoError(t, err)

	return p
}

// makeSpeechClip writes a clip where a tone sounds during every word of clipWords and the rest is silence.
// With hdr the picture is 10-bit FFV1 tagged HLG bt2020, as a phone's HDR video (in Matroska, which keeps the tags).
func makeSpeechClip(t *testing.T, path, size string, rate int, hdr bool) {
	t.Helper()

	spans := make([]string, 0, len(clipWords))
	for _, w := range clipWords {
		spans = append(spans, "between(t\\,"+strconv.FormatFloat(w.Start, 'f', 3, 64)+"\\,"+
			strconv.FormatFloat(w.End, 'f', 3, 64)+")")
	}

	d := strconv.Itoa(clipDuration)
	audio := "aevalsrc=if(" + strings.Join(spans, "+") + "\\,0.5*sin(2*PI*440*t)\\,0):s=48000:d=" + d
	video := "testsrc=duration=" + d + ":size=" + size + ":rate=" + strconv.Itoa(rate)

	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", audio, "-f", "lavfi", "-i", video, "-c:a", "aac", "-shortest"}

	if hdr {
		// The tags go on the frames (setparams): ffmpeg 8+ no longer writes -color_primaries/-color_trc of the output
		// into the stream, the output options stay for older versions.
		args = append(args, "-vf", "setparams=color_primaries=bt2020:color_trc="+testHLG+":colorspace=bt2020nc",
			"-c:v", "ffv1", "-pix_fmt", "yuv420p10le",
			"-color_primaries", "bt2020", "-color_trc", testHLG, "-colorspace", "bt2020nc")
	} else {
		args = append(args, "-c:v", "mpeg4")
	}

	run(t, "ffmpeg", append(args, path)...)

	if hdr { // the source really is HLG, so the test checks the tone mapping and not an untagged clip
		var probe probedReel

		require.NoError(t, json.Unmarshal(run(t, "ffprobe", "-v", "error", "-select_streams", "v:0",
			"-show_entries", "stream=color_transfer,color_primaries", "-of", "json", path), &probe))
		require.Len(t, probe.Streams, 1)
		require.Equal(t, testHLG, probe.Streams[0].ColorTransfer)
	}
}

type probedReel struct {
	Streams []struct {
		CodecType  string `json:"codec_type"`
		CodecName  string `json:"codec_name"`
		Profile    string `json:"profile"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		PixFmt     string `json:"pix_fmt"`
		SampleRate string `json:"sample_rate"`
		Channels   int    `json:"channels"`
		RFrameRate string `json:"r_frame_rate"`
		// ColorTransfer and ColorPrimaries: the export is always SDR bt709.
		ColorTransfer  string `json:"color_transfer"`
		ColorPrimaries string `json:"color_primaries"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// checkReel checks the export against the Instagram Reels requirements.
func checkReel(t *testing.T, path string, rate int, planned float64) {
	t.Helper()

	var probe probedReel

	require.NoError(t, json.Unmarshal(run(t, "ffprobe", "-v", "error", "-show_entries",
		"stream=codec_type,codec_name,profile,width,height,pix_fmt,sample_rate,r_frame_rate,channels,"+
			"color_transfer,color_primaries:format=duration",
		"-of", "json", path), &probe))
	require.Len(t, probe.Streams, 2)

	for i := range probe.Streams {
		s := &probe.Streams[i] // read only: a stream is too big to copy per iteration

		switch s.CodecType {
		case "video":
			assert.Equal(t, "h264", s.CodecName)
			assert.Equal(t, "High", s.Profile)
			assert.Equal(t, 1080, s.Width)
			assert.Equal(t, 1920, s.Height)
			assert.Equal(t, "yuv420p", s.PixFmt)
			assert.Equal(t, strconv.Itoa(rate)+"/1", s.RFrameRate, "the frame rate of the source")
			assert.Equal(t, testSDR, s.ColorTransfer, "SDR bt709, an HDR source is tone mapped")
			assert.Equal(t, testSDR, s.ColorPrimaries)
		case "audio":
			assert.Equal(t, "aac", s.CodecName)
			assert.Equal(t, "48000", s.SampleRate)
			assert.Equal(t, 2, s.Channels, "stereo")
		}
	}

	duration, err := strconv.ParseFloat(probe.Format.Duration, 64)
	require.NoError(t, err)
	assert.InDelta(t, planned, duration, 0.1, "as long as the plan")

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	moov, mdat := bytes.Index(data, []byte("moov")), bytes.Index(data, []byte("mdat"))
	require.Positive(t, moov)
	require.Positive(t, mdat)
	assert.Less(t, moov, mdat, "+faststart: the index is before the media")
}

const (
	bottomRow = 1800 // of 1920: below the picture of a padded 16:9 source, far from the subtitles at the top
	darkLevel = 20.0
)

// rowMean is the mean brightness (0-255) of a row of the first frame after 0.3 s.
func rowMean(t *testing.T, path string, row int) float64 {
	t.Helper()

	frame := run(t, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-ss", "0.3", "-i", path,
		"-frames:v", "1", "-vf", "format=gray", "-f", "rawvideo", "-")

	const width = 1080

	require.GreaterOrEqual(t, len(frame), (row+1)*width)

	sum := 0
	for _, v := range frame[row*width : (row+1)*width] {
		sum += int(v)
	}

	return float64(sum) / width
}

func run(t *testing.T, name string, args ...string) []byte {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), stderr.String())

	return stdout.Bytes()
}
