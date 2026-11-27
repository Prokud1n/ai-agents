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

// renderBenchEnv turns the render speed measurement on (as in internal/subtitles).
const renderBenchEnv = "RENDER_BENCH"

// testGrade is the default of the worker for a bright source (MEDIA_WORKER_GRADE_EXPOSURE -0.3, _GRADE_TOP 30).
var testGrade = &Grade{Exposure: -0.3, Warmth: 3, Top: 0.3}

// TestGradeGraph: one lookup table for the exposure and the warmth, then the shadow of the top as one generated frame
// (full over the upper quarter, faded by the middle) laid over the frame; no shadow, no overlay.
func TestGradeGraph(t *testing.T) {
	t.Parallel()

	lut := ",format=yuv420p,lutyuv=y='16+(val-16)*0.812252':u='val-3':v='val+3'"

	tests := []struct {
		name        string
		input       Grade
		expected    string
		expectedErr error
	}{
		{
			name:  "the trainer's grade",
			input: *testGrade,
			expected: lut + "[graded];color=c=black:s=1080x960:r=1:d=1,format=rgba," +
				"geq=r=0:g=0:b=0:a='77*if(lt(Y,480),1,(1+cos(PI*(Y-480)/480))/2)'[gtop];[graded][gtop]overlay=x=0:y=0",
		},
		{
			name:     "no shadow of the top",
			input:    Grade{Exposure: -0.3, Warmth: 3},
			expected: lut,
		},
		{
			name:     "only the warmth",
			input:    Grade{Warmth: 3},
			expected: ",format=yuv420p,lutyuv=y='16+(val-16)*1.000000':u='val-3':v='val+3'",
		},
		{name: "a brighter frame", input: Grade{Exposure: 0.3}, expectedErr: ErrInvalidGrade},
		{name: "a black frame", input: Grade{Exposure: -3}, expectedErr: ErrInvalidGrade},
		{name: "a colour cast", input: Grade{Warmth: 40}, expectedErr: ErrInvalidGrade},
		{name: "a black top", input: Grade{Top: 1}, expectedErr: ErrInvalidGrade},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			graph, err := gradeGraph(&tt.input, 1080, 1920)
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, graph)
		})
	}
}

// TestBurnGraphGrade: the grade works on the output frame after the fit, the tone mapping and the zoom (the shadow
// stays at the top of the frame), before the shade and the subtitles, which keep their colour.
func TestBurnGraphGrade(t *testing.T) {
	t.Parallel()

	data := BurnSubtitlesData{
		SubtitlesName: testSubtitlesName, FontsName: testFontsName, Width: 1080, Height: 1920, Fit: CropFitMode, Dir: testJobDir,
		ToneMap: true, Shade: &Shade{Height: 900, Plateau: 600, Alpha: 153},
		Cut: &Cut{
			Segments: []Segment{{Start: 1, End: 3}, {Start: 4, End: 6}, {Start: 7, End: 9}},
			Options:  RenderOptions{Fade: 0.01, FrameRate: Rational{Num: 30, Den: 1}},
			Zoom:     &Zoom{Factor: 1.3, MinSegment: 1.2, Every: 2, Must: []float64{2}},
			Grade:    testGrade,
		},
	}

	graph, err := burnGraph(&data)
	require.NoError(t, err)

	order := []string{
		"concat=n=3", "[cut]scale=1080:1920", "zscale=t=linear", "split=2[zn][zs]", "[zn][zz]overlay=x=0:y=0:enable=",
		"lutyuv=", "[graded][gtop]overlay=x=0:y=0", "setsar=1[base]", "[base][shade]overlay=", "ass=filename=",
	}

	last := -1

	for _, filter := range order {
		at := strings.Index(graph, filter)
		require.GreaterOrEqual(t, at, 0, "%s is in the graph", filter)
		assert.Greater(t, at, last, "%s comes after the filters before it", filter)
		last = at
	}

	data.Cut.Grade = &Grade{Warmth: 20}
	_, err = burnGraph(&data)
	require.ErrorIs(t, err, ErrInvalidGrade)
}

// The picture of the grade checks: a flat grey of the luma of our own frame (131 against the trainer's 96).
const (
	gradeSourceColor = "0x858585" // RGB 133: Y' 130 in the limited range
	gradeSourceLuma  = 130.0
	// The trainer's frame is 96: the grade aims at 95–105.
	gradeMinLuma = 95.0
	gradeMaxLuma = 105.0
	// minTopShadow: the upper third of the frame (the light wall under the subtitles) is at least this much darker
	// than the lower third, code values of the luma.
	minTopShadow = 15.0
	// minWarmth of the red over the blue of a grey.
	minWarmth = 1.05
)

// TestRunnerGrade: a grey SDR frame of the luma of our frame comes out as dark as the trainer's on average, its top
// third clearly darker than its bottom third, and warmer (more red than blue); the reel keeps its frames.
func TestRunnerGrade(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	ctx := t.Context()
	r := newTestRunner(2 * time.Minute)
	dir := t.TempDir()
	src := filepath.Join(dir, "grey.mp4")

	_, err := r.run(ctx, r.ffmpegPath, concat(commonArgs(),
		lavfiInput("color=c="+gradeSourceColor+":size=540x960:rate=30:duration=4"),
		lavfiInput("sine=frequency=440:sample_rate=48000:duration=4"),
		[]string{videoCodecFlag, videoCodec, "-pix_fmt", videoPixFmt, audioCodecFlag, renderACodec, shortestFlag, src}), nil)
	require.NoError(t, err)

	source, err := r.MeanLuma(ctx, src, []float64{1, 3}, false)
	require.NoError(t, err)
	assert.InDelta(t, gradeSourceLuma, source, 2, "the grey of our own frame")

	reel := burnTestReel(ctx, t, r, dir, src, &Cut{
		Segments: []Segment{{Start: 0, End: 1.5}, {Start: 2, End: 3.5}},
		Options:  RenderOptions{Fade: 0.01, FrameRate: Rational{Num: 30, Den: 1}},
		Grade:    testGrade,
	})

	mean, err := r.MeanLuma(ctx, reel, []float64{0.5, 2}, false)
	require.NoError(t, err)

	top, bottom := thirdsLuma(ctx, t, r, reel)
	red, blue := redBlue(ctx, t, r, reel)

	t.Logf("grade: mean luma %.1f → %.1f, top third %.1f, bottom third %.1f, red/blue %.3f", source, mean, top, bottom,
		red/blue)
	assert.GreaterOrEqual(t, mean, gradeMinLuma)
	assert.LessOrEqual(t, mean, gradeMaxLuma)
	assert.Greater(t, bottom-top, minTopShadow, "the top of the frame is in shadow")
	assert.Greater(t, red/blue, minWarmth, "warmer: more red than blue")
}

// TestRunnerGradeHDR: an HLG source is tone mapped, then graded: the measurement sees the picture as the render brings
// it to SDR, and the graded reel is darker than the plain one, the top more so.
func TestRunnerGradeHDR(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	ctx := t.Context()
	r := newTestRunner(2 * time.Minute)
	dir := t.TempDir()
	src := filepath.Join(dir, "hlg.mkv")

	_, err := r.run(ctx, r.ffmpegPath, concat(commonArgs(),
		lavfiInput("testsrc2=size=540x960:rate=30:duration=4"),
		lavfiInput("sine=frequency=440:sample_rate=48000:duration=4"),
		[]string{
			"-vf", "setparams=color_primaries=bt2020:color_trc=" + hlgTransfer + ":colorspace=bt2020nc",
			videoCodecFlag, "ffv1", "-pix_fmt", "yuv420p10le", "-color_primaries", "bt2020", "-color_trc", hlgTransfer,
			"-colorspace", "bt2020nc", audioCodecFlag, renderACodec, shortestFlag, src,
		}), nil)
	require.NoError(t, err)

	source, err := r.MeanLuma(ctx, src, []float64{1, 3}, true)
	require.NoError(t, err)
	assert.Greater(t, source, 16.0, "the tone mapped picture is not black")

	cut := &Cut{
		Segments: []Segment{{Start: 0, End: 1.5}, {Start: 2, End: 3.5}},
		Options:  RenderOptions{Fade: 0.01, FrameRate: Rational{Num: 30, Den: 1}},
	}
	plain := burnToneMapped(ctx, t, r, filepath.Join(dir, "plain"), src, cut)

	graded := *cut
	graded.Grade = testGrade
	reel := burnToneMapped(ctx, t, r, filepath.Join(dir, "graded"), src, &graded)

	plainTop, plainBottom := thirdsLuma(ctx, t, r, plain)
	top, bottom := thirdsLuma(ctx, t, r, reel)

	t.Logf("grade HDR: source %.1f; plain top %.1f bottom %.1f; graded top %.1f bottom %.1f", source, plainTop,
		plainBottom, top, bottom)
	assert.Less(t, bottom, plainBottom-5, "the exposure darkens the whole frame")
	assert.Greater(t, plainTop-top, plainBottom-bottom+3, "the top is darkened more than the bottom")
}

// burnToneMapped renders the cut of an HDR src like burnTestReel, tone mapped, into a directory of its own.
func burnToneMapped(ctx context.Context, t *testing.T, r *Runner, dir, src string, cut *Cut) string {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, testFontsName), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, testSubtitlesName), []byte(emptyTestASS), 0o600))

	reel := filepath.Join(dir, "reel.mp4")
	_, err := r.BurnSubtitles(ctx, &BurnSubtitlesData{
		Input: src, Output: reel, Dir: dir, SubtitlesName: testSubtitlesName, FontsName: testFontsName,
		Width: 1080, Height: 1920, Fit: CropFitMode, Cut: cut, ToneMap: true,
	})
	require.NoError(t, err)

	return reel
}

const emptyTestASS = "[Script Info]\nScriptType: v4.00+\nPlayResX: 1080\nPlayResY: 1920\n\n[Events]\n" +
	"Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n"

// thirdsLuma is the mean luma of the upper and the lower third of the frame of the reel at 1 s.
func thirdsLuma(ctx context.Context, t *testing.T, r *Runner, path string) (top, bottom float64) {
	t.Helper()

	in, err := localFile(path)
	require.NoError(t, err)

	plane, err := r.lumaPlane(ctx, in, 1, false)
	require.NoError(t, err)
	require.Len(t, plane, lumaWidth*lumaHeight)

	third := lumaHeight / 3 * lumaWidth

	return meanByte(plane[:third]), meanByte(plane[len(plane)-third:])
}

// redBlue are the mean red and blue of the lower third of the frame of the reel at 1 s (below the shadow of the top).
func redBlue(ctx context.Context, t *testing.T, r *Runner, path string) (red, blue float64) {
	t.Helper()

	in, err := localFile(path)
	require.NoError(t, err)

	const width, height = 54, 96

	out, err := r.runLimited(ctx, r.ffmpegPath, concat(commonArgs(), []string{"-ss", "1"}, inputArgs(in),
		[]string{"-frames:v", "1", videoFilterFlag, "scale=" + strconv.Itoa(width) + ":" + strconv.Itoa(height) +
			",format=rgb24", formatFlag, rawVideoFormat, "-"}), 1<<20)
	require.NoError(t, err)
	require.Len(t, out, width*height*3)

	pixels := []byte(out)[height*2/3*width*3:]
	sumR, sumB := 0, 0

	for i := 0; i+2 < len(pixels); i += 3 {
		sumR += int(pixels[i])
		sumB += int(pixels[i+2])
	}

	n := float64(len(pixels) / 3)

	return float64(sumR) / n, float64(sumB) / n
}

// TestRenderSpeedGrade (RENDER_BENCH=1, in the media image on 2 CPUs): a vertical 4K30 H.264 SDR source as an iPhone
// records it, rough cut into a dozen fragments, rendered as before T3.25 (a 9 % zoom at every joint) and with its
// defaults (30 % at every second joint and on a section, the grade with the shadow of the top); the brightness of
// the source is measured as the worker measures it. The log line "render speed" is the measurement.
func TestRenderSpeedGrade(t *testing.T) {
	if os.Getenv(renderBenchEnv) == "" {
		t.Skip("set " + renderBenchEnv + "=1 to measure the render speed")
	}

	requireFFmpeg(t)

	const (
		duration  = 30
		fragments = 12
		rounds    = 2
		// maxSlowdown of the defaults of T3.25 against the render of T3.15: the criterion is about 10 %.
		maxSlowdown = 1.15
	)

	ctx := t.Context()
	r := newTestRunner(20 * time.Minute)
	dir := t.TempDir()
	src := filepath.Join(dir, "phone.mp4")

	started := time.Now()
	_, err := r.run(ctx, r.ffmpegPath, concat(commonArgs(),
		lavfiInput("testsrc2=size=2160x3840:rate=30:duration="+strconv.Itoa(duration)+",noise=alls=8:allf=t"),
		lavfiInput("sine=frequency=440:sample_rate=48000:duration="+strconv.Itoa(duration)),
		[]string{
			videoCodecFlag, videoCodec, "-preset", "ultrafast", "-crf", "23", "-g", "30", "-pix_fmt", videoPixFmt,
			audioCodecFlag, renderACodec, shortestFlag, src,
		}), nil)
	require.NoError(t, err)
	t.Logf("bench source: 4K30 H.264, %d s, made in %.1f s", duration, time.Since(started).Seconds())

	segments := make([]Segment, 0, fragments)
	for i := range fragments {
		start := float64(i) * duration / fragments
		segments = append(segments, Segment{Start: start, End: start + 1.9})
	}

	opts := RenderOptions{Fade: 0.01, FrameRate: Rational{Num: 30, Den: 1}}
	before := &Cut{Segments: segments, Options: opts, Zoom: &Zoom{Factor: 1.09, MinSegment: 1.2}}
	after := &Cut{
		Segments: segments, Options: opts, Grade: testGrade,
		Zoom: &Zoom{Factor: 1.3, MinSegment: 1.2, Every: 2, Must: []float64{5.7, 15.2}},
	}

	started = time.Now()
	luma, err := r.MeanLuma(ctx, src, []float64{1, 6, 11, 16, 21, 26}, false)
	require.NoError(t, err)
	t.Logf("render speed: the brightness of the 4K source (6 key frames) measured in %.2f s: mean luma %.1f",
		time.Since(started).Seconds(), luma)

	best := map[string]float64{}

	for round := range rounds {
		for _, c := range []struct {
			name string
			cut  *Cut
		}{{name: "before", cut: before}, {name: "after", cut: after}} {
			out := filepath.Join(dir, c.name+strconv.Itoa(round))
			require.NoError(t, os.MkdirAll(out, 0o700))

			started := time.Now()
			burnTestReel(ctx, t, r, out, src, c.cut)
			seconds := time.Since(started).Seconds()

			if b, ok := best[c.name]; !ok || seconds < b {
				best[c.name] = seconds
			}

			t.Logf("render speed: %s, round %d: %.1f s", c.name, round+1, seconds)
		}
	}

	changed, joints := after.ZoomChanges()
	ratio := best["after"] / best["before"]
	t.Logf("render speed: T3.25 defaults (grade + zoom 30 %%, %d of %d joints) %.1f s against T3.15 (zoom 9 %% at "+
		"every joint) %.1f s: %+.1f %%", changed, joints, best["after"], best["before"], (ratio-1)*100)
	assert.Less(t, ratio, maxSlowdown)
}
