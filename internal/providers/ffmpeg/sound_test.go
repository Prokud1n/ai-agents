package ffmpeg

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ebur128Summary = `[out#0/mp3 @ 0x5615ad29b640] video:0kB audio:32kB subtitle:0kB other streams:0kB
size=      32kB time=00:00:07.99 bitrate=  32.7kbits/s speed=95.5x
[Parsed_ebur128_0 @ 0x5615ad387b40] Summary:

  Integrated loudness:
    I:         -27.6 LUFS
    Threshold: -38.1 LUFS

  Loudness range:
    LRA:         6.4 LU
    Threshold: -48.2 LUFS
    LRA low:   -31.0 LUFS
    LRA high:  -24.6 LUFS

  True peak:
    Peak:       -9.3 dBFS
`

func TestParseLoudness(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected *Loudness
	}{
		{
			name:     "the summary of ebur128",
			input:    ebur128Summary,
			expected: &Loudness{Integrated: -27.6, Threshold: -38.1, Range: 6.4, TruePeak: -9.3},
		},
		{
			// -inf would break loudnorm's options and JSON: the lowest level they take.
			name: "silence",
			input: "[Parsed_ebur128_0 @ 0x1] Summary:\n\n  Integrated loudness:\n    I:         -70.0 LUFS\n" +
				"    Threshold:   0.0 LUFS\n\n  Loudness range:\n    LRA:         0.0 LU\n    Threshold:   0.0 LUFS\n\n" +
				"  True peak:\n    Peak:       -inf dBFS\n",
			expected: &Loudness{Integrated: -70, Threshold: 0, Range: 0, TruePeak: -99},
		},
		{name: "no summary", input: "size=      32kB time=00:00:07.99\n", expected: nil},
		{name: "a cut summary", input: "[Parsed_ebur128_0 @ 0x1] Summary:\n\n  Integrated loudness:\n    I:  -23.0 LUFS\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, parseLoudness(tt.input))
		})
	}
}

func TestSoundChain(t *testing.T) {
	t.Parallel()

	measured := &Loudness{Integrated: -27.6, Threshold: -38.1, Range: 6.4, TruePeak: -9.3}
	target := LoudnessTarget{Integrated: -14, TruePeak: -2, Range: 11}

	tests := []struct {
		name     string
		input    RenderOptions
		expected string
	}{
		{name: "as recorded", input: RenderOptions{}, expected: ""},
		{
			name: "speech: the noise reduction (its delay compensated), then one gain and the limiter",
			input: RenderOptions{
				Denoise:  &Denoise{NoiseFloor: -64},
				Loudness: &LoudnessTarget{Integrated: -14, TruePeak: -2, Range: 11, Measured: measured},
			},
			expected: "apad=pad_dur=0.025,highpass=f=80,afftdn=nr=10:nf=-64.0,atrim=start=0.025,asetpts=PTS-STARTPTS," +
				"volume=13.6dB,aresample=192000,alimiter=limit=0.794328:attack=5:release=50:level=false:latency=1,aresample=48000",
		},
		{
			name:     "no measurement (a transcript stored before it): loudnorm in one pass",
			input:    RenderOptions{Loudness: &target},
			expected: "loudnorm=I=-14.0:TP=-2.0:LRA=11.0,aresample=48000",
		},
		{
			name:     "muted: silence is neither denoised nor raised",
			input:    RenderOptions{Mute: true, Denoise: &Denoise{NoiseFloor: -50}, Loudness: &target},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, soundChain(tt.input))
		})
	}
}

func TestNoiseFloorFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *Loudness
		expected float64
	}{
		{name: "no measurement", input: nil, expected: -50},
		{name: "a quiet recording: 36 dB under its speech", input: &Loudness{Integrated: -30.4}, expected: -66},
		{name: "a loud one", input: &Loudness{Integrated: -12.6}, expected: -49},
		{name: "very quiet: at most -80", input: &Loudness{Integrated: -48}, expected: -80},
		{name: "very loud", input: &Loudness{Integrated: -5}, expected: -41},
		{name: "at most -35 dBFS", input: &Loudness{Integrated: 2}, expected: -35},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.InDelta(t, tt.expected, NoiseFloorFor(tt.input), 1e-9)
		})
	}
}

// TestSegmentsFilterSound: the joined sound is processed once, after concat.
func TestSegmentsFilterSound(t *testing.T) {
	t.Parallel()

	opts := RenderOptions{Denoise: &Denoise{NoiseFloor: -50}, Loudness: &LoudnessTarget{Integrated: -14, TruePeak: -2, Range: 11}}
	got := segmentsFilter([]Segment{{Start: 1, End: 2}, {Start: 3, End: 4}}, opts)

	assert.Contains(t, got, "concat=n=2:v=1:a=1[v][j];[j]apad=pad_dur=0.025,highpass=f=80,afftdn=nr=10:nf=-50.0,"+
		"atrim=start=0.025,asetpts=PTS-STARTPTS,loudnorm=I=-14.0:TP=-2.0:LRA=11.0,aresample=48000[a]")
	assert.Equal(t, 1, strings.Count(got, "afftdn="), "one noise reduction for the whole reel")
}

// speechLike is a voice-like sound of the given amplitude for aevalsrc: a 150 Hz voice with harmonics in syllables
// (4 a second) and a plosive burst every 1.3 s, so its peaks are 16–18 dB above its loudness, as in phone speech.
func speechLike(amplitude string) string {
	return "aevalsrc='" + amplitude + "*(pow(abs(sin(2*PI*2*t)),3)*(0.35*sin(2*PI*150*t)+0.2*sin(2*PI*300*t)+" +
		"0.12*sin(2*PI*450*t)+0.08*sin(2*PI*1200*t))+if(lt(mod(t,1.3),0.004),0.9*sin(2*PI*900*t),0))':s=48000:d=8"
}

// TestRunnerLoudness: a quiet (about -35 LUFS) and a loud (about -16 LUFS, peaks near full scale) speech-like source
// (peaks 16–18 dB above its loudness) come out at the target with the true peak at most -1.5 dBTP after the AAC encode:
// by the measurement taken while the audio is extracted (one gain corrected by the passes over the sound of the cut,
// and the limiter: the worker) at -14 ± 0.5 LUFS, and without it (loudnorm in one pass) at -14 ± 1; the cut and the
// subtitles are rendered in the same pass, as the worker does it. The render reports what it measured.
func TestRunnerLoudness(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	const (
		quiet     = "0.12" // -34.6 LUFS, peaks -16.8 dBFS
		loud      = "1.0"  // -16.3 LUFS, peaks -0.4 dBFS: the gain alone would clip them
		target    = -14.0
		maxPeak   = -1.5
		tolerance = 1.0
		// fitted: the tolerance of the gain fitted by the passes over the sound of the cut (T3.19b).
		fitted = 0.5
	)

	tests := []struct {
		name      string
		amplitude string
		source    float64
		measure   bool
		denoise   bool
	}{
		{name: "quiet speech, measured", amplitude: quiet, source: -34.6, measure: true, denoise: true},
		{name: "loud speech, measured", amplitude: loud, source: -16.3, measure: true, denoise: true},
		{name: "quiet, one pass without a measurement", amplitude: quiet, source: -34.6},
		{name: "loud workout, one pass, no noise reduction", amplitude: loud, source: -16.3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			r := newTestRunner(2 * time.Minute)
			dir := t.TempDir()
			src := filepath.Join(dir, "speech.mp4")

			_, err := r.run(ctx, r.ffmpegPath, concat(commonArgs(),
				lavfiInput(speechLike(tt.amplitude)),
				lavfiInput("testsrc=duration=8:size=180x320:rate=30"),
				[]string{videoCodecFlag, videoCodec, audioCodecFlag, renderACodec, shortestFlag, src}), nil)
			require.NoError(t, err)

			measured, err := r.ExtractAudioMeasured(ctx, src, filepath.Join(dir, "speech"+AudioExtension))
			require.NoError(t, err)
			require.NotNil(t, measured, "ebur128 reported its summary")
			assert.InDelta(t, tt.source, measured.Integrated, tolerance, "the source as generated")

			loudness := &LoudnessTarget{Integrated: target, TruePeak: -2, Range: 11}
			if tt.measure {
				loudness.Measured = measured
			}

			opts := RenderOptions{Fade: 0.01, FrameRate: Rational{Num: 30, Den: 1}, Loudness: loudness}
			if tt.denoise {
				opts.Denoise = &Denoise{NoiseFloor: NoiseFloorFor(measured)}
			}

			reel, report := burnTestReelReport(ctx, t, r, dir, src, &Cut{
				Segments: []Segment{{Start: 0.5, End: 3.5}, {Start: 4, End: 7.5}}, Options: opts,
			})

			want := tolerance
			if tt.measure {
				want = fitted
			}

			out := measureLoudness(ctx, t, r, reel)
			assert.InDelta(t, target, out.Integrated, want, "the reel at the target")
			assert.LessOrEqual(t, out.TruePeak, maxPeak, "the true peak after AAC at most -1.5 dBTP")
			require.NotNil(t, report.Sound)
			require.NotNil(t, report.Sound.Output, "the render measured its sound")
			assert.InDelta(t, out.Integrated, report.Sound.Output.Integrated, fitted, "the render's own measurement")

			if tt.measure {
				assert.Positive(t, report.Sound.Passes, "the gain is fitted by the sound of the cut")
			}

			t.Logf("loudness: source %.1f LUFS (peak %.1f) → reel %.1f LUFS (peak %.1f dBTP); render: gain %+.1f dB "+
				"(correction %+.1f dB, %d passes), measured %.1f LUFS", measured.Integrated, measured.TruePeak,
				out.Integrated, out.TruePeak, report.Sound.Gain, report.Sound.Correction, report.Sound.Passes,
				report.Sound.Output.Integrated)
		})
	}
}

// TestRunnerSoundSync: a white flash and a click at the same moments in three pieces of the source; after the cut
// with the noise reduction and the loudness every click is where its flash is (within 8 ms; afftdn alone would put it
// 25 ms late), and the sound is as long as the picture.
func TestRunnerSoundSync(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	const (
		rate       = 30
		sampleRate = 48000
		maxShift   = 0.008 // s
		bright     = 128   // of 255: a flash frame
		clickLevel = 0.3   // of the loudest sample: the onset of a click
		minGap     = 0.1   // s between two clicks
		sampleSize = 16    // a 4×4 gray crop of a frame
		reelLength = 3.0   // s: three pieces of 1 s
	)

	tests := []struct {
		name    string
		measure bool
	}{
		{name: "one gain and the limiter", measure: true},
		{name: "loudnorm in one pass"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			r := newTestRunner(2 * time.Minute)
			dir := t.TempDir()
			src := filepath.Join(dir, "flashes.mp4")

			// Flashes at the frames 18, 66 and 132 (0.6, 2.2 and 4.4 s), clicks of 20 ms at the same times over a quiet hum.
			_, err := r.run(ctx, r.ffmpegPath, concat(commonArgs(),
				lavfiInput("aevalsrc='if(between(t,0.6,0.62)+between(t,2.2,2.22)+between(t,4.4,4.42),"+
					"0.8*sin(2*PI*1000*t),0.01*sin(2*PI*200*t))':s=48000:d=6"),
				lavfiInput("color=c=black:s=180x320:r=30:d=6,drawbox=x=0:y=0:w=iw:h=ih:color=white:t=fill:"+
					"enable='between(t,0.59,0.62)+between(t,2.19,2.22)+between(t,4.39,4.42)'"),
				[]string{videoCodecFlag, videoCodec, audioCodecFlag, renderACodec, shortestFlag, src}), nil)
			require.NoError(t, err)

			measured, err := r.ExtractAudioMeasured(ctx, src, filepath.Join(dir, "clicks"+AudioExtension))
			require.NoError(t, err)

			loudness := &LoudnessTarget{Integrated: -14, TruePeak: -2, Range: 11}
			if tt.measure {
				loudness.Measured = measured
			}

			// Pieces 0.3–1.3, 2.0–3.0, 4.0–5.0 s: the flashes come at 0.3, 1.2 and 2.4 s of the reel.
			reel := burnTestReel(ctx, t, r, dir, src, &Cut{
				Segments: []Segment{{Start: 0.3, End: 1.3}, {Start: 2, End: 3}, {Start: 4, End: 5}},
				Options: RenderOptions{
					Fade: 0.01, FrameRate: Rational{Num: rate, Den: 1},
					Denoise: &Denoise{NoiseFloor: NoiseFloorFor(measured)}, Loudness: loudness,
				},
			})

			in, err := localFile(reel)
			require.NoError(t, err)

			frames, err := r.runLimited(ctx, r.ffmpegPath, concat(commonArgs(), inputArgs(in),
				[]string{videoFilterFlag, "crop=4:4,format=gray", formatFlag, rawVideoFormat, "-"}), 1<<20)
			require.NoError(t, err)

			var flashes []float64

			for i := 0; i+sampleSize <= len(frames); i += sampleSize {
				k := i / sampleSize
				if frames[i] > bright && (k == 0 || frames[i-sampleSize] <= bright) {
					flashes = append(flashes, float64(k)/rate)
				}
			}

			pcm, err := r.runLimited(ctx, r.ffmpegPath, concat(commonArgs(), inputArgs(in),
				[]string{noVideoFlag, channelsFlag, "1", sampleRateFlag, "48000", formatFlag, "s16le", "-"}), 1<<21)
			require.NoError(t, err)

			samples := make([]int, 0, len(pcm)/2)
			loudest := 0

			for i := 0; i+1 < len(pcm); i += 2 {
				v := int(pcm[i]) | int(pcm[i+1])<<8
				if v >= 1<<15 { // signed 16-bit little-endian
					v -= 1 << 16
				}

				samples = append(samples, v)
				loudest = max(loudest, v, -v)
			}

			var clicks []float64

			last := -1.0

			for i, v := range samples {
				at := float64(i) / sampleRate
				if float64(max(v, -v)) > clickLevel*float64(loudest) && at-last > minGap {
					clicks = append(clicks, at)
				}

				if float64(max(v, -v)) > clickLevel*float64(loudest) {
					last = at
				}
			}

			require.Equal(t, []float64{0.3, 1.2, 2.4}, roundAll(flashes), "the flashes of the reel")
			require.Len(t, clicks, len(flashes), "a click for every flash: %v", clicks)

			for i := range clicks {
				assert.InDelta(t, flashes[i], clicks[i], maxShift, "click %d is with its flash", i)
			}

			assert.InDelta(t, reelLength, float64(len(samples))/sampleRate, 0.03, "the sound is as long as the picture")
			t.Logf("sync: flashes %v, clicks %v", flashes, clicks)
		})
	}
}

func roundAll(values []float64) []float64 {
	out := make([]float64, 0, len(values))
	for _, v := range values {
		out = append(out, math.Round(v*1000)/1000)
	}

	return out
}

// burnTestReel renders the cut of src at 1080×1920 with an empty ASS (no events) into dir and returns the reel.
func burnTestReel(ctx context.Context, t *testing.T, r *Runner, dir, src string, cut *Cut) string {
	t.Helper()

	reel, _ := burnTestReelReport(ctx, t, r, dir, src, cut)

	return reel
}

// burnTestReelReport is burnTestReel with the report of the render.
func burnTestReelReport(ctx context.Context, t *testing.T, r *Runner, dir, src string, cut *Cut) (string, *BurnReport) {
	t.Helper()

	const emptyASS = "[Script Info]\nScriptType: v4.00+\nPlayResX: 1080\nPlayResY: 1920\n\n[Events]\n" +
		"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"

	require.NoError(t, os.WriteFile(filepath.Join(dir, testSubtitlesName), []byte(emptyASS), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, testFontsName), 0o700))

	reel := filepath.Join(dir, "reel.mp4")
	report, err := r.BurnSubtitles(ctx, &BurnSubtitlesData{
		Input: src, Output: reel, Dir: dir, SubtitlesName: testSubtitlesName, FontsName: testFontsName,
		Width: 1080, Height: 1920, Fit: CropFitMode, Cut: cut,
	})
	require.NoError(t, err)

	return reel, report
}

// measureLoudness measures the sound of a file as the worker does it (ebur128 with the true peak).
func measureLoudness(ctx context.Context, t *testing.T, r *Runner, path string) *Loudness {
	t.Helper()

	in, err := localFile(path)
	require.NoError(t, err)

	log := newTailBuffer(measureLogTail)
	_, err = r.run(ctx, r.ffmpegPath, concat(commonArgs(), inputArgs(in),
		[]string{noVideoFlag, audioFilterFlag, measureFilter, formatFlag, "null", "-"}), log)
	require.NoError(t, err)

	loudness := parseLoudness(log.String())
	require.NotNil(t, loudness, log.String())

	return loudness
}

// TestRunnerTruePeakBright: quiet speech with bright bursts (sibilant-like tones at 7.35 and 11 kHz, whose peaks fall
// between the samples) pushed into the limiter by the gain: the limiter works oversampled, so after the AAC encode the true peak of the reel is
// at most -1.0 dBTP; the render reports the true peak of the file.
func TestRunnerTruePeakBright(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	const maxTruePeak = -1.0

	ctx := t.Context()
	r := newTestRunner(2 * time.Minute)
	dir := t.TempDir()
	src := filepath.Join(dir, "bright.mkv") // PCM: the source is exactly the generated sound

	// Quiet speech (about -35 LUFS) with short bright bursts of 20 ms twice a second, 6 dB under full scale: the gain
	// of about +20 dB drives the bursts far into the limiter.
	_, err := r.run(ctx, r.ffmpegPath, concat(commonArgs(),
		lavfiInput("aevalsrc='0.12*(pow(abs(sin(2*PI*2*t)),3)*(0.35*sin(2*PI*150*t)+0.2*sin(2*PI*300*t)))+"+
			"if(lt(mod(t,0.5),0.02),0.25*sin(2*PI*11025*t+1)+0.25*sin(2*PI*7350*t),0)':s=48000:d=6"),
		lavfiInput("testsrc=duration=6:size=180x320:rate=30"),
		[]string{videoCodecFlag, videoCodec, audioCodecFlag, pcmCodec, shortestFlag, src}), nil)
	require.NoError(t, err)

	measured, err := r.ExtractAudioMeasured(ctx, src, filepath.Join(dir, "bright"+AudioExtension))
	require.NoError(t, err)
	require.NotNil(t, measured)

	cut := &Cut{
		Segments: []Segment{{Start: 0.5, End: 5.5}},
		Options: RenderOptions{
			Fade: 0.01, FrameRate: Rational{Num: 30, Den: 1},
			Loudness: &LoudnessTarget{Integrated: -14, TruePeak: -2, Range: 11, Measured: measured},
		},
	}

	// Before the encode: the oversampled limiter holds the true peak at its limit (at 48 kHz it lets the peaks between
	// the samples through: -1.2 dBTP at the limit of -2 here).
	in, err := localFile(src)
	require.NoError(t, err)

	fitted, _, err := r.fitLoudness(ctx, in, cut.Segments, cut.Options)
	require.NoError(t, err)

	processed, err := r.measureCut(ctx, in, cut.Segments, fitted)
	require.NoError(t, err)
	assert.LessOrEqual(t, processed.TruePeak, -2.0+0.2, "the limiter holds the true peak")

	reel, report := burnTestReelReport(ctx, t, r, dir, src, cut)

	out := measureLoudness(ctx, t, r, reel)
	assert.LessOrEqual(t, out.TruePeak, maxTruePeak, "the true peak after AAC")
	assert.InDelta(t, -14.0, out.Integrated, 0.5, "the gain is fitted through the limiter too")
	require.NotNil(t, report.Sound)
	require.NotNil(t, report.Sound.Output)
	assert.InDelta(t, out.TruePeak, report.Sound.Output.TruePeak, 0.05, "the render reports the true peak of the file")
	t.Logf("bright sound: source %.1f LUFS (peak %.1f dBTP) → processed %.1f dBTP → reel %.1f LUFS, true peak %.1f "+
		"dBTP after AAC; gain %+.1f dB (correction %+.1f dB, %d passes), lowered after AAC by %.1f dB", measured.Integrated,
		measured.TruePeak, processed.TruePeak, out.Integrated, out.TruePeak, report.Sound.Gain, report.Sound.Correction,
		report.Sound.Passes, report.Sound.PeakCut)
}

// TestRunnerCapFilePeak: a rendered file whose true peak is above -1 dBTP gets its sound lowered (the picture copied):
// the report says by how much and has the new measurement.
func TestRunnerCapFilePeak(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	ctx := t.Context()
	r := newTestRunner(time.Minute)
	reel := filepath.Join(t.TempDir(), "reel.mp4")

	_, err := r.run(ctx, r.ffmpegPath, concat(commonArgs(),
		lavfiInput("aevalsrc=0.98*sin(2*PI*1000*t):s=48000:d=3"),
		lavfiInput("testsrc=duration=3:size=180x320:rate=30"),
		[]string{videoCodecFlag, videoCodec, audioCodecFlag, renderACodec, shortestFlag, reel}), nil)
	require.NoError(t, err)

	in, err := localFile(reel)
	require.NoError(t, err)

	report := &SoundReport{Output: r.measureFile(ctx, in)}
	require.NotNil(t, report.Output)
	require.Greater(t, report.Output.TruePeak, maxFilePeak, "the generated tone is too loud")

	require.NoError(t, r.capFilePeak(ctx, reel, report))
	require.NotNil(t, report.Output)
	assert.LessOrEqual(t, report.Output.TruePeak, maxFilePeak)
	assert.Positive(t, report.PeakCut)
	assert.LessOrEqual(t, report.PeakCut, maxPeakCut)
	assert.Contains(t, probeStreams(t, r, reel), "h264,180,320", "the picture is kept")
}
