package subtitles_test

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
// SDR at the bitrate of a phone, about 4 minutes. Two layouts of a one-minute reel are cut from it: sparse — ten
// stretches of phrases spread over the whole source, as the selection of the best phrases leaves it; dense — one
// continuous take of 2.5 minutes whose pauses (under 3 s) are cut, so it is decoded as one piece.
const (
	longSize = "2160x3840"
	longRate = 30
	// longChunk seconds are encoded once and repeated (stream copy) to longDuration: the decoding costs the same.
	longChunk    = 8
	longDuration = 240
	longBitrate  = "50M"
	// longGOP: a phone puts a keyframe about every second; seeking decodes from the keyframe before the cut.
	longGOP = "30"
	// Sparse: the first stretch at longFirst s, then every longStride s; each is longPhrases phrases of longWords with a
	// pause of longPause s (cut) between them.
	longStretches = 10
	longFirst     = 6.0
	longStride    = 23.0
	longPhrases   = 3
	longPause     = 0.6
	// Dense: densePhrases phrases every denseStride s (their pauses under 3 s) from longFirst: a take of 2.5 minutes.
	denseStride  = 4.5
	densePhrases = 33
	// maxLongRender (s) of the one-minute draft on 2 CPUs: the goal of T3.19b. Before it the whole source was decoded:
	// 96.5 s on the CI runner for the sparse layout (the trainer's machine took 4.3 min for such an edit).
	maxLongRender = 100.0
	// The memory bench: a 4K 10-bit HEVC HLG source of hdrDuration s (hdrChunk s repeated), cut into hdrPieces far
	// apart (hdrStride s), so the decoders of the inputs (at most 4 for this frame) are open together.
	hdrChunk    = 4
	hdrDuration = 120
	hdrStride   = 10.0
	hdrPieces   = 11
	// rssInterval of sampling the memory of ffmpeg.
	rssInterval = 50 * time.Millisecond
	kib         = 1 << 10
)

var longWords = []string{"Делаем", "три", "подхода", "держим"}

// TestRenderSpeedLongSource measures the render of one-minute drafts (the rough cut, the sound, the zoom and the
// subtitles in one pass, as the media worker renders them) from a 4-minute 4K source on the machine it runs on, with
// the peak memory of ffmpeg: the time of an edit the trainer waits for. The sparse layout must take under
// maxLongRender; the rest is a log, the speed depends on the machine.
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

	bench := newLongBench(t, dir)

	elapsed, reel := bench.render(ctx, t, source, spreadTranscript(longStride, longStretches, longPhrases), "sparse")
	assert.Less(t, elapsed, maxLongRender, "an edit of a one-minute reel decodes only what it keeps")
	t.Logf("render speed (long source, sparse): a %.1f s reel from a %d s 4K source rendered in %.1f s",
		reel, longDuration, elapsed)

	elapsed, reel = bench.render(ctx, t, source, spreadTranscript(denseStride, densePhrases, 1), "dense")
	t.Logf("render speed (long source, dense): a %.1f s reel from a %.1f s take of a 4K source rendered in %.1f s",
		reel, denseStride*densePhrases, elapsed)
}

// TestRenderSpeedMemoryHDR measures the peak memory of ffmpeg rendering a draft from far apart pieces of a 4K 10-bit
// HEVC HLG source (an iPhone): every input keeps its decoder, their number is limited by the frame (4 here).
func TestRenderSpeedMemoryHDR(t *testing.T) {
	if os.Getenv(renderBenchEnv) != "1" {
		t.Skip("render benchmark: run with " + renderBenchEnv + "=1 and ffmpeg with libass and zimg")
	}

	ctx, cancel := context.WithTimeout(t.Context(), benchTimeout)
	defer cancel()

	dir := t.TempDir()
	chunk := filepath.Join(dir, "chunk.mp4")
	source := filepath.Join(dir, "hdr.mp4")
	codec := benchSourceCodec(ctx, t)

	started := time.Now()
	makeHLGSource(ctx, t, chunk, codec, longRate, hdrChunk)
	runBench(ctx, t, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-y",
		"-stream_loop", strconv.Itoa(hdrDuration/hdrChunk-1), "-i", chunk, "-c", "copy", source)
	t.Logf("memory bench source: %s %d fps %s 10-bit HLG, %d s, generated in %.1f s", benchSize, longRate, codec, hdrDuration,
		time.Since(started).Seconds())

	transcript := spreadTranscript(hdrStride, hdrPieces, 1)
	transcript.Duration = hdrDuration

	elapsed, reel := newLongBench(t, dir).render(ctx, t, source, transcript, "4K 10-bit HEVC, far pieces")
	t.Logf("render speed (memory bench): a %.1f s reel from %d far pieces rendered in %.1f s", reel, hdrPieces, elapsed)
}

// longBench renders drafts of the long sources as the media worker does.
type longBench struct {
	runner *ffmpeg.Runner
	cutter *roughcut.Service
	burner *subtitles.Service
	dir    string
}

func newLongBench(t *testing.T, dir string) *longBench {
	t.Helper()

	log := slog.New(slog.DiscardHandler)
	runner := ffmpeg.NewRunner(&config.FFmpeg{Timeout: benchTimeout})

	cutter, err := roughcut.NewService(runner, &config.RoughCut{
		MaxPause: 300 * time.Millisecond, RetakeSimilarity: 0.7, Fillers: []string{"э"},
	}, log)
	require.NoError(t, err)

	fonts := os.Getenv(benchFontsEnv)
	if fonts == "" {
		fonts = benchFontsDir
	}

	burner, err := subtitles.NewService(runner, &config.Subtitles{FontsDir: fonts}, log)
	require.NoError(t, err)

	return &longBench{runner: runner, cutter: cutter, burner: burner, dir: dir}
}

// render cuts the source by the transcript and renders the draft, logging the speed and the peak memory of ffmpeg;
// it returns the seconds of the render and of the reel.
func (b *longBench) render(
	ctx context.Context,
	t *testing.T,
	source string,
	transcript *transcription.Result,
	layout string,
) (elapsed, reel float64) {
	t.Helper()

	out := filepath.Join(b.dir, strings.ReplaceAll(strings.ReplaceAll(layout, " ", "-"), ",", ""))

	cut, err := b.cutter.Cut(ctx, &roughcut.CutData{
		Source: source, Transcript: transcript, OutDir: out, PlanOnly: true,
	})
	require.NoError(t, err)

	info, err := b.runner.Probe(ctx, source)
	require.NoError(t, err)

	draft := benchDraft(ctx, t, b.runner, cut, source, filepath.Join(out, "audio.mp3"))
	video := filepath.Join(out, "reel.mp4")
	stop := sampleFFmpegRSS(ctx)
	elapsed = burnBench(ctx, t, b.burner, video, &subtitles.BurnSubtitlesData{
		CreateSubtitlesData: subtitles.CreateSubtitlesData{Transcript: &cut.Transcript},
		VideoPath:           source, Fit: ffmpeg.CropFitMode, Cut: draft, ToneMap: info.HDR(),
	})
	peak := stop()

	logReel(ctx, t, "talk from a 4K source, "+layout, video, elapsed, len(cut.Plan.Keep))
	t.Logf("decode memory (%s): the peak RSS of ffmpeg %d MB; frame %dx%d %s; %d fragments", layout, peak/kib,
		info.Width, info.Height, info.PixelFormat, len(cut.Plan.Keep))

	return elapsed, cut.Plan.Duration
}

// sampleFFmpegRSS samples the resident memory of the ffmpeg processes of the machine (the container) until the
// returned function is called; it returns the largest one seen, KiB.
func sampleFFmpegRSS(ctx context.Context) func() int64 {
	done := make(chan struct{})

	var (
		wg   sync.WaitGroup
		peak int64
	)

	wg.Go(func() {
		ticker := time.NewTicker(rssInterval)
		defer ticker.Stop()

		for {
			peak = max(peak, ffmpegRSS())

			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})

	return func() int64 {
		close(done)
		wg.Wait()

		return peak
	}
}

// ffmpegRSS is the largest VmRSS of a process named ffmpeg, KiB; 0 without /proc.
func ffmpegRSS() int64 {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}

	largest := int64(0)

	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}

		largest = max(largest, processRSS("/proc/"+e.Name()+"/status"))
	}

	return largest
}

// processRSS reads VmRSS of an ffmpeg process from its status file, KiB; 0 for another process.
func processRSS(path string) int64 {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return 0
	}
	defer f.Close()

	ffmpegProcess, rss := false, int64(0)
	scanner := bufio.NewScanner(f)

	for scanner.Scan() {
		key, value, _ := strings.Cut(scanner.Text(), ":")
		fields := strings.Fields(value)

		switch {
		case key == "Name" && len(fields) > 0:
			ffmpegProcess = fields[0] == "ffmpeg"
		case key == "VmRSS" && len(fields) > 0:
			rss, _ = strconv.ParseInt(fields[0], 10, 64)
		}
	}

	if !ffmpegProcess {
		return 0
	}

	return rss
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

// spreadTranscript: count stretches from longFirst every stride seconds, each of phrases phrases of longWords with a
// pause of longPause between them; the source lasts longDuration.
func spreadTranscript(stride float64, count, phrases int) *transcription.Result {
	result := &transcription.Result{Language: langRu, Duration: longDuration}

	for k := range count {
		at := longFirst + float64(k)*stride

		for range phrases {
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
