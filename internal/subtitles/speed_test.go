package subtitles_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/subtitles"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// renderBenchEnv turns the render speed benchmark on (job media-image): it encodes a 4K HEVC source first and takes
// minutes, so the ordinary test run skips it.
const renderBenchEnv = "RENDER_BENCH"

// The benchmark source is what an iPhone records: vertical 4K, 60 fps, 10-bit HEVC HLG (bt2020, arib-std-b67).
const (
	benchSize     = "2160x3840"
	benchRate     = 60
	benchDuration = 15 // seconds
	benchHLG      = "arib-std-b67"
	// benchBitrate of the source: an iPhone records 4K60 HDR at 50-80 Mbit/s, and decoding the HEVC is the floor of
	// the render; at the default ultrafast rate (~13 Mbit/s) it would look cheaper than it is.
	benchBitrate = "50M"
	// benchPhrase: a phrase starts every benchPhrase seconds, its words take about half of it, the pause is cut.
	benchPhrase   = 3.5
	benchWordLen  = 0.35
	benchWordGap  = 0.1
	benchFirstPad = 0.3
	benchTimeout  = 30 * time.Minute
	bitsPerByte   = 8
	bitsPerKbit   = 1000
	// maxRenderRatio is a coarse guard against a regression: before T3.11 the draft took about 7.5 s of render per
	// second on 2 CPUs (ffmpeg 6.1, 50 Mbit/s source), after it about 2.7 s.
	maxRenderRatio = 5.5
	// The sound and the zoom of the worker's defaults (MEDIA_WORKER_LOUDNESS -14, MEDIA_WORKER_ZOOM 9).
	benchLoudness    = -14.0
	benchTruePeak    = -2.0
	benchRange       = 11.0
	benchZoom        = 1.09
	benchZoomSegment = 1.2
	benchPercent     = 100
	estimateMinutes  = 10
	benchFontsEnv    = "SUBTITLES_FONTS_DIR"
	benchFontsDir    = "../../assets/fonts"
)

var benchWords = []string{"Делаем", "три", "подхода", "эээ", "держим", "спину"}

// TestRenderSpeed measures the editor's render of a draft (the rough cut, the subtitles and the export in one pass,
// as the media worker does it) on a synthetic iPhone-like source and logs the seconds of rendering per second of the
// reel with the size and bitrate of the result. Only a log: the speed depends on the machine.
func TestRenderSpeed(t *testing.T) {
	if os.Getenv(renderBenchEnv) != "1" {
		t.Skip("render benchmark: run with " + renderBenchEnv + "=1 and ffmpeg with libass and zimg")
	}

	ctx, cancel := context.WithTimeout(t.Context(), benchTimeout)
	defer cancel()

	dir := t.TempDir()
	source := filepath.Join(dir, "source.mp4")
	codec := benchSourceCodec(ctx, t)

	started := time.Now()
	makeBenchSource(ctx, t, source, codec)
	t.Logf("bench source: %s %d fps %s 10-bit HLG at %sbit/s, %d s, generated in %.1f s",
		benchSize, benchRate, codec, benchBitrate, benchDuration, time.Since(started).Seconds())

	log := slog.New(slog.DiscardHandler)
	runner := ffmpeg.NewRunner(&config.FFmpeg{Timeout: benchTimeout})

	info, err := runner.Probe(ctx, source)
	require.NoError(t, err)
	require.True(t, info.HDR(), "the source is HLG")

	cutter, err := roughcut.NewService(runner, &config.RoughCut{
		MaxPause: 400 * time.Millisecond, RetakeSimilarity: 0.7, Fillers: []string{"э"},
	}, log)
	require.NoError(t, err)

	transcript := benchTranscript()

	cut, err := cutter.Cut(ctx, &roughcut.CutData{
		Source: source, Transcript: transcript, OutDir: filepath.Join(dir, "plan"), PlanOnly: true,
	})
	require.NoError(t, err)
	require.Greater(t, len(cut.Plan.Keep), 3, "several fragments")

	fonts := os.Getenv(benchFontsEnv)
	if fonts == "" {
		fonts = benchFontsDir
	}

	burner, err := subtitles.NewService(runner, &config.Subtitles{FontsDir: fonts}, log)
	require.NoError(t, err)

	// The same talk as before T3.15 (no sound processing, no zoom), on the same machine: the cost of T3.15 is the
	// difference with the draft below.
	plain := filepath.Join(dir, "plain.mp4")
	elapsed := burnBench(ctx, t, burner, plain, &subtitles.BurnSubtitlesData{
		CreateSubtitlesData: subtitles.CreateSubtitlesData{Transcript: &cut.Transcript},
		VideoPath:           source, Fit: ffmpeg.CropFitMode, Cut: &ffmpeg.Cut{Segments: cut.Segments(), Options: cut.Render},
		ToneMap: info.HDR(),
	})
	plainRatio := logReel(ctx, t, "talk without the sound and the zoom", plain, elapsed, len(cut.Plan.Keep))

	draft := benchDraft(ctx, t, runner, cut, source, filepath.Join(dir, "audio.mp3"))
	reel := filepath.Join(dir, "reel.mp4")
	elapsed = burnBench(ctx, t, burner, reel, &subtitles.BurnSubtitlesData{
		CreateSubtitlesData: subtitles.CreateSubtitlesData{Transcript: &cut.Transcript},
		VideoPath:           source, Fit: ffmpeg.CropFitMode, Cut: draft, ToneMap: info.HDR(),
	})
	ratio := logReel(ctx, t, "talk", reel, elapsed, len(cut.Plan.Keep))
	assert.Less(t, ratio, maxRenderRatio, "the render is as slow as before T3.11 (7.5 s per second on 2 CPUs)")
	t.Logf("render speed: the sound and the zoom of T3.15 cost %+.1f %% of the talk render", (ratio/plainRatio-1)*benchPercent)

	referenceStyle(ctx, t, burner, &subtitles.BurnSubtitlesData{
		CreateSubtitlesData: subtitles.CreateSubtitlesData{Transcript: &cut.Transcript, Hook: benchHook},
		VideoPath:           source, Fit: ffmpeg.CropFitMode, Cut: draft, ToneMap: info.HDR(),
	}, dir, len(cut.Plan.Keep))

	// A workout keeps the whole source: the time per second of the source shows how long a 10-minute one takes.
	rate := cut.Render.FrameRate.Float()
	// A workout: the loudness only, no noise reduction or zoom.
	workoutSound := cut.Render
	workoutSound.Loudness = draft.Options.Loudness
	whole := &ffmpeg.Cut{
		Segments: []ffmpeg.Segment{{Start: 0, End: math.Floor(cut.Plan.SourceDuration*rate) / rate}}, Options: workoutSound,
	}
	workout := filepath.Join(dir, "workout.mp4")
	elapsed = burnBench(ctx, t, burner, workout, &subtitles.BurnSubtitlesData{
		CreateSubtitlesData: subtitles.CreateSubtitlesData{Transcript: transcript},
		VideoPath:           source, Fit: ffmpeg.CropFitMode, Cut: whole, ToneMap: info.HDR(),
	})
	ratio = logReel(ctx, t, "workout", workout, elapsed, 1)
	t.Logf("workout estimate: a %d-minute source renders in about %.1f min here (MEDIA_WORKER_FFMPEG_TIMEOUT 45m)",
		estimateMinutes, ratio*estimateMinutes)
}

// benchHook is the hook title of the style comparison.
const benchHook = "три вещи, которые я делаю каждое утро"

// referenceStyle measures the talk draft in the phrase style of before T3.17 and in the reference style (the words on
// their plate, the screen notes, the handle and the end card) and logs what the reference style costs.
func referenceStyle(
	ctx context.Context,
	t *testing.T,
	burner *subtitles.Service,
	draft *subtitles.BurnSubtitlesData,
	dir string,
	fragments int,
) {
	t.Helper()

	phrase := subtitles.PhraseStyle()
	before := *draft
	before.Style = &phrase
	elapsed := burnBench(ctx, t, burner, filepath.Join(dir, "phrase.mp4"), &before)
	phraseRatio := logReel(ctx, t, "talk in the phrase style", filepath.Join(dir, "phrase.mp4"), elapsed, fragments)

	words := subtitles.WordsStyle()
	after := *draft
	after.Style = &words
	after.Notes = []subtitles.Note{
		{Kind: subtitles.ListTitleNote, Text: "3 вещи", Start: 1},
		{Kind: subtitles.ListItemNote, Text: "1/ стакан воды", Start: 3},
		{Kind: subtitles.ListItemNote, Text: "2/ 10 приседаний", Start: 5},
		{Kind: subtitles.CalloutNote, Text: "6 лет", Start: 7},
	}
	after.Branding = &subtitles.Branding{Handle: "@prokudina.ana", Icon: true, EndCard: true}
	elapsed = burnBench(ctx, t, burner, filepath.Join(dir, "reference.mp4"), &after)
	ratio := logReel(ctx, t, "talk in the reference style", filepath.Join(dir, "reference.mp4"), elapsed, fragments)

	t.Logf("render speed: the reference style of T3.17 (words on a plate, notes, handle, end card) costs %+.1f %% of "+
		"the phrase style", (ratio/phraseRatio-1)*benchPercent)
}

// benchDraft is the cut with the sound and the framing the worker renders a talking head with (T3.15, the defaults of
// MEDIA_WORKER_*): the loudness by the measurement taken while the audio is extracted for the speech recognition (not
// timed: it is that pass), the noise reduction and the jump-cut zoom.
func benchDraft(
	ctx context.Context,
	t *testing.T,
	runner *ffmpeg.Runner,
	cut *roughcut.Result,
	source, audio string,
) *ffmpeg.Cut {
	t.Helper()

	measured, err := runner.ExtractAudioMeasured(ctx, source, audio)
	require.NoError(t, err)
	require.NotNil(t, measured)

	talk := cut.Render
	talk.Denoise = &ffmpeg.Denoise{NoiseFloor: ffmpeg.NoiseFloorFor(measured)}
	talk.Loudness = &ffmpeg.LoudnessTarget{Integrated: benchLoudness, TruePeak: benchTruePeak, Range: benchRange, Measured: measured}
	draft := &ffmpeg.Cut{
		Segments: cut.Segments(), Options: talk, Zoom: &ffmpeg.Zoom{Factor: benchZoom, MinSegment: benchZoomSegment},
	}

	t.Logf("bench sound: source %.1f LUFS, true peak %.1f dBTP; zooms %d of %d fragments",
		measured.Integrated, measured.TruePeak, draft.Zooms(), len(cut.Plan.Keep))

	return draft
}

// burnBench renders data into out and returns the seconds it took.
func burnBench(
	ctx context.Context,
	t *testing.T,
	burner *subtitles.Service,
	out string,
	data *subtitles.BurnSubtitlesData,
) float64 {
	t.Helper()

	data.OutputPath = out
	started := time.Now()

	_, err := burner.BurnSubtitles(ctx, data)
	require.NoError(t, err)

	return time.Since(started).Seconds()
}

// benchSourceCodec is libx265 (what an iPhone records) when the ffmpeg has it, else 10-bit H.264.
func benchSourceCodec(ctx context.Context, t *testing.T) string {
	t.Helper()

	if strings.Contains(string(runBench(ctx, t, "ffmpeg", "-hide_banner", "-encoders")), " libx265 ") {
		return "libx265"
	}

	return "libx264"
}

// makeBenchSource encodes moving test pictures with a little grain (a camera is never clean) and a tone, at the bitrate
// of a phone.
func makeBenchSource(ctx context.Context, t *testing.T, path, codec string) {
	t.Helper()

	makeHLGSource(ctx, t, path, codec, benchRate, benchDuration)
}

// makeHLGSource encodes a vertical 4K 10-bit HLG source of the given frame rate and seconds (makeBenchSource).
func makeHLGSource(ctx context.Context, t *testing.T, path, codec string, rate, duration int) {
	t.Helper()

	d := strconv.Itoa(duration)
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=" + benchSize + ":rate=" + strconv.Itoa(rate) + ":duration=" + d,
		"-f", "lavfi", "-i", "sine=frequency=220:sample_rate=48000:duration=" + d,
		"-vf", "noise=alls=6:allf=t,format=yuv420p10le," +
			"setparams=color_primaries=bt2020:color_trc=" + benchHLG + ":colorspace=bt2020nc",
		"-c:v", codec, "-preset", "ultrafast", "-b:v", benchBitrate, "-pix_fmt", "yuv420p10le",
		"-color_primaries", "bt2020", "-color_trc", benchHLG, "-colorspace", "bt2020nc",
	}

	if codec == "libx265" {
		args = append(args, "-x265-params", "log-level=error", "-tag:v", "hvc1")
	} else {
		args = append(args, "-profile:v", "high10")
	}

	runBench(ctx, t, "ffmpeg", append(args, "-c:a", "aac", "-shortest", path)...)
}

// benchTranscript: a phrase of benchWords every benchPhrase seconds; the filler and the pauses between phrases are
// cut, so the render has several fragments.
func benchTranscript() *transcription.Result {
	result := &transcription.Result{Language: "ru", Duration: benchDuration}

	for start := benchFirstPad; start+benchPhrase <= benchDuration; start += benchPhrase {
		at := start
		words := make([]string, 0, len(benchWords))

		for _, w := range benchWords {
			result.Words = append(result.Words, transcription.Word{Word: w, Start: at, End: at + benchWordLen})
			words = append(words, w)
			at += benchWordLen + benchWordGap
		}

		result.Segments = append(result.Segments, transcription.Segment{
			Start: start, End: at, Text: strings.Join(words, " "),
		})
	}

	return result
}

type benchProbe struct {
	Streams []struct {
		CodecType  string `json:"codec_type"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
		RFrameRate string `json:"r_frame_rate"`
		NbFrames   string `json:"nb_frames"`
		ColorTrc   string `json:"color_transfer"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
		Size     string `json:"size"`
		BitRate  string `json:"bit_rate"`
	} `json:"format"`
}

// logReel logs the speed of the render and what came out, and returns the seconds of render per second of the reel.
func logReel(ctx context.Context, t *testing.T, kind, reel string, elapsed float64, fragments int) float64 {
	t.Helper()

	var probe benchProbe

	require.NoError(t, json.Unmarshal(runBench(ctx, t, "ffprobe", "-v", "error", "-show_entries",
		"stream=codec_type,width,height,r_frame_rate,nb_frames,color_transfer:format=duration,size,bit_rate",
		"-of", "json", reel), &probe))

	duration, err := strconv.ParseFloat(probe.Format.Duration, 64)
	require.NoError(t, err)
	require.Positive(t, duration)

	size, err := strconv.ParseInt(probe.Format.Size, 10, 64)
	require.NoError(t, err)

	video := ""

	for i := range probe.Streams {
		s := &probe.Streams[i]
		if s.CodecType == "video" {
			video = strconv.Itoa(s.Width) + "x" + strconv.Itoa(s.Height) + " " + s.RFrameRate + " fps, " +
				s.NbFrames + " frames, " + s.ColorTrc
		}
	}

	bitrate := float64(size) * bitsPerByte / duration / bitsPerKbit

	t.Logf("render speed (%s): %.2f s of render per 1 s of reel (render %.1f s, reel %.2f s, %d fragments); "+
		"reel %s, %d bytes, %.0f kbit/s (format bit_rate %s); GOMAXPROCS %d, NumCPU %d",
		kind, elapsed/duration, elapsed, duration, fragments, video, size, bitrate, probe.Format.BitRate,
		runtime.GOMAXPROCS(0), runtime.NumCPU())

	return elapsed / duration
}

func runBench(ctx context.Context, t *testing.T, name string, args ...string) []byte {
	t.Helper()

	cmd := exec.CommandContext(ctx, name, args...)

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), stderr.String())

	return stdout.Bytes()
}
