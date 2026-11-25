package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
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
	target := Loudnorm{Integrated: -14, TruePeak: -1.5, Range: 11}

	tests := []struct {
		name     string
		input    RenderOptions
		expected string
	}{
		{name: "as recorded", input: RenderOptions{}, expected: ""},
		{
			name:  "speech: the noise reduction, then the loudness by the measurement (linear)",
			input: RenderOptions{Denoise: true, Loudness: &Loudnorm{Integrated: -14, TruePeak: -1.5, Range: 11, Measured: measured}},
			expected: "highpass=f=80,afftdn=nr=10:nf=-50," +
				"loudnorm=I=-14.0:TP=-1.5:LRA=11.0:measured_I=-27.6:measured_TP=-9.3:measured_LRA=6.4:measured_thresh=-38.1:linear=true," +
				"aresample=48000",
		},
		{
			name:     "no measurement (a plan stored before it): one pass",
			input:    RenderOptions{Loudness: &target},
			expected: "loudnorm=I=-14.0:TP=-1.5:LRA=11.0,aresample=48000",
		},
		{name: "muted: silence is neither denoised nor raised", input: RenderOptions{Mute: true, Denoise: true, Loudness: &target}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, soundChain(tt.input))
		})
	}
}

// TestSegmentsFilterSound: the joined sound is processed once, after concat.
func TestSegmentsFilterSound(t *testing.T) {
	t.Parallel()

	opts := RenderOptions{Denoise: true, Loudness: &Loudnorm{Integrated: -14, TruePeak: -1.5, Range: 11}}
	got := segmentsFilter([]Segment{{Start: 1, End: 2}, {Start: 3, End: 4}}, opts)

	assert.Contains(t, got, "concat=n=2:v=1:a=1[v][j];[j]highpass=f=80,afftdn=nr=10:nf=-50,"+
		"loudnorm=I=-14.0:TP=-1.5:LRA=11.0,aresample=48000[a]")
	assert.Equal(t, 1, strings.Count(got, "loudnorm="), "one loudnorm for the whole reel")
}

// TestRunnerLoudness: a quiet (-30 LUFS) and a loud (-8 LUFS) source come out at -14 ± 1 LUFS with the true peak at
// most -1 dBTP, by the measurement taken while the audio is extracted (the worker) and without it (one pass); the cut
// and the subtitles are rendered in the same pass, as the worker does it.
func TestRunnerLoudness(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	const (
		quietGain = -5.5 // dB on the sine of lavfi (-24.5 LUFS with the tremolo): -30 LUFS
		loudGain  = 16.5 // -8 LUFS
		target    = -14.0
		tolerance = 1.0
	)

	tests := []struct {
		name    string
		gain    float64
		source  float64
		measure bool
		denoise bool
	}{
		{name: "quiet speech, measured", gain: quietGain, source: -30, measure: true, denoise: true},
		{name: "loud speech, measured", gain: loudGain, source: -8, measure: true, denoise: true},
		{name: "quiet, one pass without a measurement", gain: quietGain, source: -30},
		{name: "loud workout, one pass, no noise reduction", gain: loudGain, source: -8},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			r := newTestRunner(2 * time.Minute)
			dir := t.TempDir()
			src := filepath.Join(dir, "speech.mp4")

			// A tone with a slow tremolo, like syllables: a steady sine would be taken for noise by nothing here, but
			// the loudness range and the gate see a moving level.
			_, err := r.run(ctx, r.ffmpegPath, concat(commonArgs(),
				lavfiInput("sine=frequency=440:sample_rate=48000:duration=8"),
				lavfiInput("testsrc=duration=8:size=180x320:rate=30"),
				[]string{audioFilterFlag, "volume=" + strconv.FormatFloat(tt.gain, 'f', 1, 64) + "dB,apulsator=hz=0.7:amount=0.6",
					videoCodecFlag, videoCodec, audioCodecFlag, renderACodec, shortestFlag, src}), nil)
			require.NoError(t, err)

			measured, err := r.ExtractAudioMeasured(ctx, src, filepath.Join(dir, "audio"+AudioExtension))
			require.NoError(t, err)
			require.NotNil(t, measured, "ebur128 reported its summary")
			assert.InDelta(t, tt.source, measured.Integrated, tolerance, "the source as generated")

			loudnorm := &Loudnorm{Integrated: target, TruePeak: -1.5, Range: 11}
			if tt.measure {
				loudnorm.Measured = measured
			}

			reel := burnTestReel(ctx, t, r, dir, src, &Cut{
				Segments: []Segment{{Start: 0.5, End: 3.5}, {Start: 4, End: 7.5}},
				Options: RenderOptions{
					Fade: 0.01, FrameRate: Rational{Num: 30, Den: 1}, Denoise: tt.denoise, Loudness: loudnorm,
				},
			})

			out := measureLoudness(ctx, t, r, reel)
			assert.InDelta(t, target, out.Integrated, tolerance, "the reel at the target")
			assert.LessOrEqual(t, out.TruePeak, -1.0, "the true peak under -1 dBTP")
			t.Logf("loudness: source %.1f LUFS (peak %.1f) → reel %.1f LUFS (peak %.1f dBTP)",
				measured.Integrated, measured.TruePeak, out.Integrated, out.TruePeak)
		})
	}
}

// burnTestReel renders the cut of src at 1080×1920 with an empty ASS (no events) into dir and returns the reel.
func burnTestReel(ctx context.Context, t *testing.T, r *Runner, dir, src string, cut *Cut) string {
	t.Helper()

	const emptyASS = "[Script Info]\nScriptType: v4.00+\nPlayResX: 1080\nPlayResY: 1920\n\n[Events]\n" +
		"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"

	require.NoError(t, os.WriteFile(filepath.Join(dir, testSubtitlesName), []byte(emptyASS), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, testFontsName), 0o700))

	reel := filepath.Join(dir, "reel.mp4")
	_, err := r.BurnSubtitles(ctx, &BurnSubtitlesData{
		Input: src, Output: reel, Dir: dir, SubtitlesName: testSubtitlesName, FontsName: testFontsName,
		Width: 1080, Height: 1920, Fit: CropFitMode, Cut: cut,
	})
	require.NoError(t, err)

	return reel
}

// measureLoudness measures the sound of a file as the worker does it (ebur128 with the true peak).
func measureLoudness(ctx context.Context, t *testing.T, r *Runner, path string) *Loudness {
	t.Helper()

	in, err := localFile(path)
	require.NoError(t, err)

	log := newTailBuffer(measureLogTail)
	_, err = r.run(ctx, r.ffmpegPath, concat(commonArgs(), inputArgs(in),
		[]string{"-vn", audioFilterFlag, measureFilter, formatFlag, "null", "-"}), log)
	require.NoError(t, err)

	loudness := parseLoudness(log.String())
	require.NotNil(t, loudness, log.String())

	return loudness
}
