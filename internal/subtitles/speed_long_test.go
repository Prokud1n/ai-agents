package subtitles_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
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

// The long benchmark source is what the trainer records (the live check of 26.11): vertical 4K, 30 fps, 8-bit H.264
// SDR at the bitrate of a phone, about 4 minutes; a reel of about a minute is cut from it in ten stretches of phrases
// spread over the whole source, as the selection of the best phrases leaves it.
const (
	longSize = "2160x3840"
	longRate = 30
	// longChunk seconds are encoded once and repeated (stream copy) to longDuration: the decoding costs the same.
	longChunk    = 8
	longDuration = 240
	longBitrate  = "50M"
	// longGOP: a phone puts a keyframe about every second; seeking decodes from the keyframe before the cut.
	longGOP = "30"
	// Stretches of speech: the first at longFirst s, then every longStride s; each is longPhrases phrases of
	// longPhraseWords words with a pause of longPause s (cut) between them.
	longStretches   = 10
	longFirst       = 6.0
	longStride      = 23.0
	longPhrases     = 3
	longPhraseWords = 4
	longPause       = 0.6
	// maxLongRender (s) of the one-minute draft on 2 CPUs: the goal of T3.19b. Before it the whole source was decoded:
	// 96.5 s on the CI runner (the trainer's machine took 4.3 min for such an edit).
	maxLongRender = 100.0
)

var longWords = []string{"Делаем", "три", "подхода", "держим"}

// TestRenderSpeedLongSource measures the render of a one-minute draft (the rough cut, the sound, the zoom and the
// subtitles in one pass, as the media worker renders it) from a 4-minute 4K source on the machine it runs on: the
// time of an edit the trainer waits for. Only a log: the speed depends on the machine.
func TestRenderSpeedLongSource(t *testing.T) {
	if os.Getenv(renderBenchEnv) != "1" {
		t.Skip("render benchmark: run with " + renderBenchEnv + "=1 and ffmpeg with libass")
	}

	ctx, cancel := context.WithTimeout(t.Context(), benchTimeout)
	defer cancel()

	dir := t.TempDir()
	source := filepath.Join(dir, "long.mp4")

	started := time.Now()
	makeLongSource(ctx, t, dir, source)
	t.Logf("long bench source: %s %d fps H.264 8-bit SDR at %sbit/s, keyframe every %s frames, %d s, generated in %.1f s",
		longSize, longRate, longBitrate, longGOP, longDuration, time.Since(started).Seconds())

	log := slog.New(slog.DiscardHandler)
	runner := ffmpeg.NewRunner(&config.FFmpeg{Timeout: benchTimeout})

	cutter, err := roughcut.NewService(runner, &config.RoughCut{
		MaxPause: 400 * time.Millisecond, RetakeSimilarity: 0.7, Fillers: []string{"э"},
	}, log)
	require.NoError(t, err)

	transcript := longTranscript()

	cut, err := cutter.Cut(ctx, &roughcut.CutData{
		Source: source, Transcript: transcript, OutDir: filepath.Join(dir, "plan"), PlanOnly: true,
	})
	require.NoError(t, err)
	require.Greater(t, len(cut.Plan.Keep), longStretches, "several fragments in every stretch")

	fonts := os.Getenv(benchFontsEnv)
	if fonts == "" {
		fonts = benchFontsDir
	}

	burner, err := subtitles.NewService(runner, &config.Subtitles{FontsDir: fonts}, log)
	require.NoError(t, err)

	draft := benchDraft(ctx, t, runner, cut, source, filepath.Join(dir, "audio.mp3"))
	reel := filepath.Join(dir, "reel.mp4")
	elapsed := burnBench(ctx, t, burner, reel, &subtitles.BurnSubtitlesData{
		CreateSubtitlesData: subtitles.CreateSubtitlesData{Transcript: &cut.Transcript},
		VideoPath:           source, Fit: ffmpeg.CropFitMode, Cut: draft,
	})
	logReel(ctx, t, "talk from a 4-minute 4K source", reel, elapsed, len(cut.Plan.Keep))
	assert.Less(t, elapsed, maxLongRender, "an edit of a one-minute reel decodes only what it keeps")
	t.Logf("render speed (long source): a %.1f s reel from a %d s 4K source rendered in %.1f s",
		cut.Plan.Duration, longDuration, elapsed)
}

// makeLongSource encodes longChunk seconds of moving test pictures with grain and a tone, then repeats them by stream
// copy to longDuration seconds.
func makeLongSource(ctx context.Context, t *testing.T, dir, path string) {
	t.Helper()

	chunk := filepath.Join(dir, "chunk.mp4")
	d := strconv.Itoa(longChunk)

	runBench(ctx, t, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size="+longSize+":rate="+strconv.Itoa(longRate)+":duration="+d,
		"-f", "lavfi", "-i", "sine=frequency=220:sample_rate=48000:duration="+d,
		"-vf", "noise=alls=6:allf=t,format=yuv420p",
		"-c:v", "libx264", "-preset", "ultrafast", "-b:v", longBitrate, "-g", longGOP, "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", chunk)

	runBench(ctx, t, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-stream_loop", strconv.Itoa(longDuration/longChunk-1), "-i", chunk, "-c", "copy", path)
}

// longTranscript: longStretches stretches of speech spread over the source, the rest silent (cut).
func longTranscript() *transcription.Result {
	result := &transcription.Result{Language: langRu, Duration: longDuration}

	for k := range longStretches {
		at := longFirst + float64(k)*longStride

		for range longPhrases {
			start := at
			words := make([]string, 0, len(longWords))

			for _, w := range longWords {
				result.Words = append(result.Words, transcription.Word{Word: w, Start: at, End: at + benchWordLen})
				words = append(words, w)
				at += benchWordLen + benchWordGap
			}

			result.Segments = append(result.Segments, transcription.Segment{
				Start: start, End: at, Text: strings.Join(words, " "),
			})
			at += longPause
		}
	}

	return result
}
