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

// The flags and the codec of the generated sources.
const (
	pixFmtFlag    = "-pix_fmt"
	losslessCodec = "ffv1" // keeps 10 bits and the HLG tags in Matroska
)

// renderBenchEnv turns the render speed measurement on (as in internal/subtitles).
const renderBenchEnv = "RENDER_BENCH"

// testGrade is the default of the worker for a flat bright source (MEDIA_WORKER_GRADE_EXPOSURE -0.3, _GRADE_TOP 30,
// the warmth of T3.28).
var testGrade = &Grade{Exposure: -0.3, Warmth: 2, Top: 0.3}

// TestGradeGraph: one lookup table for the exposure curve (x^(1 − EV): −0.3 EV of the midtones is the power 1.3) and
// the warmth, then the shadow of the top as one generated frame
// (full over the upper quarter, faded by the middle) laid over the frame; no shadow, no overlay.
func TestGradeGraph(t *testing.T) {
	t.Parallel()

	lut := ",format=yuv420p,lutyuv=y='16+219*pow((clip(val,16,235)-16)/219,1.300000)':u='val-2':v='val+2'"

	shade := func(alpha string) string {
		return "color=c=black:s=1080x960:r=1:d=1,format=rgba," +
			"geq=r=0:g=0:b=0:a='" + alpha + "*if(lt(Y,480),1,(1+cos(PI*(Y-480)/480))/2)'"
	}
	zoomed := "gte(t,1.483333)*lt(t,3.483333)"

	tests := []struct {
		name  string
		input Grade
		// zoomed: the zoomed spans of the reel (zoomEnable).
		zoomed      string
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
			input:    Grade{Exposure: -0.3, Warmth: 2},
			expected: lut,
		},
		{
			name:     "only the warmth",
			input:    Grade{Warmth: 3},
			expected: ",format=yuv420p,lutyuv=y='16+219*pow((clip(val,16,235)-16)/219,1.000000)':u='val-3':v='val+3'",
		},
		{name: "a brighter frame", input: Grade{Exposure: 0.3}, expectedErr: ErrInvalidGrade},
		{name: "a black frame", input: Grade{Exposure: -3}, expectedErr: ErrInvalidGrade},
		{name: "a colour cast", input: Grade{Warmth: 40}, expectedErr: ErrInvalidGrade},
		{name: "a black top", input: Grade{Top: 1}, expectedErr: ErrInvalidGrade},
		{name: "a black top of the zoomed pieces", input: Grade{ZoomTop: new(1.0)}, zoomed: zoomed, expectedErr: ErrInvalidGrade},
		{
			name: "a zoom, one shadow: one overlay", input: *testGrade, zoomed: zoomed,
			expected: lut + "[graded];" + shade("77") + "[gtop];[graded][gtop]overlay=x=0:y=0",
		},
		{
			name: "no zoomed spans: the shadow of the frame", input: Grade{Exposure: -0.3, Warmth: 2, Top: 0.3, ZoomTop: new(0.6)},
			expected: lut + "[graded];" + shade("77") + "[gtop];[graded][gtop]overlay=x=0:y=0",
		},
		{
			// T3.30: each overlay over its own frames — every frame still gets one.
			name: "the zoomed pieces with a shadow of their own", zoomed: zoomed,
			input: Grade{Exposure: -0.3, Warmth: 2, Top: 0.3, ZoomTop: new(0.6)},
			expected: lut + "[graded];" + shade("77") + "[gtop];[graded][gtop]overlay=x=0:y=0:enable='not(" + zoomed +
				")'[gradedz];" + shade("153") + "[gtopz];[gradedz][gtopz]overlay=x=0:y=0:enable='" + zoomed + "'",
		},
		{
			name: "a shadow on the zoomed pieces only", zoomed: zoomed, input: Grade{Exposure: -0.3, Warmth: 2, ZoomTop: new(0.6)},
			expected: lut + "[gradedz];" + shade("153") + "[gtopz];[gradedz][gtopz]overlay=x=0:y=0:enable='" + zoomed + "'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			graph, err := gradeGraph(&tt.input, 1080, 1920, tt.zoomed)
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
	// minWhite: the curve keeps the white (235) bright, the frame is darker, not flatter.
	minWhite = 225.0
)

// TestRunnerGrade: the grade of the default exposure (the worker's for a bright source, MEDIA_WORKER_GRADE_*) on a grey
// SDR frame of the luma of our frame: as dark as the trainer's on average, its top third clearly darker than its
// bottom third, warmer (more red than blue); a white field stays white under the curve (below the shadow of the top).
// The reels have one key frame: every moment of MeanLuma measures their first frame — the picture is still.
func TestRunnerGrade(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	ctx := t.Context()
	r := newTestRunner(2 * time.Minute)
	dir := t.TempDir()

	grey := makeStillSource(ctx, t, r, filepath.Join(dir, "grey.mp4"), gradeSourceColor)
	source, err := r.MeanLuma(ctx, grey, []float64{1, 3}, false, 0)
	require.NoError(t, err)
	assert.InDelta(t, gradeSourceLuma, source.Mean, 4, "the grey of our own frame")
	assert.InDelta(t, source.Top, source.Bottom, 1, "a flat frame: its thirds are as light as each other")

	cut := &Cut{
		Segments: []Segment{{Start: 0, End: 1.5}, {Start: 2, End: 3.5}},
		Options:  RenderOptions{Fade: 0.01, FrameRate: Rational{Num: 30, Den: 1}},
		Grade:    testGrade,
	}
	reel := burnTestReel(ctx, t, r, filepath.Join(dir, mkdir(t, dir, "grey")), grey, cut)

	graded, err := r.MeanLuma(ctx, reel, []float64{0.5, 2}, false, 0)
	require.NoError(t, err)

	mean := graded.Mean
	top, bottom := thirdsLuma(ctx, t, r, reel)
	red, blue := redBlue(ctx, t, r, reel)

	white := makeStillSource(ctx, t, r, filepath.Join(dir, "white.mp4"), "white")
	whiteReel := burnTestReel(ctx, t, r, filepath.Join(dir, mkdir(t, dir, "white")), white, cut)
	whiteTop, whiteBottom := thirdsLuma(ctx, t, r, whiteReel)

	t.Logf("grade: mean luma %.1f → %.1f, top third %.1f, bottom third %.1f, red/blue %.3f; white: top %.1f, "+
		"bottom %.1f", source.Mean, mean, top, bottom, red/blue, whiteTop, whiteBottom)
	assert.GreaterOrEqual(t, mean, gradeMinLuma)
	assert.LessOrEqual(t, mean, gradeMaxLuma)
	assert.Greater(t, bottom-top, minTopShadow, "the top of the frame is in shadow")
	assert.Greater(t, red/blue, minWarmth, "warmer: more red than blue")
	assert.Less(t, red/blue, maxWarmth, "not warmer than the trainer's frame")
	assert.GreaterOrEqual(t, whiteBottom, minWhite, "the white stays white")
	assertGradedAsEstimated(t, testGrade, source, graded)
}

// The light top over the dark bottom of T3.28 (our source: a light wall over dark clothes, the thirds about 161 /
// 100), synthetic: Y' 150 over a middle of 160 and a bottom of 80, the mean 130 as ours; and with a natural middle:
// 140 / 115 / 90, the mean 115 (the colours of the boxes land a few code values under their BT.709 luma when encoded,
// as the grey does).
const (
	lightTopSource = "color=c=0xacacac:size=540x960:rate=30:duration=4," +
		"drawbox=x=0:y=0:w=540:h=320:color=0xa0a0a0:t=fill,drawbox=x=0:y=640:w=540:h=320:color=0x4f4f4f:t=fill"
	naturalMiddleSource = "color=c=0x787878:size=540x960:rate=30:duration=4," +
		"drawbox=x=0:y=0:w=540:h=320:color=0x949494:t=fill,drawbox=x=0:y=640:w=540:h=320:color=0x5b5b5b:t=fill"
	// minTopGap: the upper third of the graded frame is at least this much darker than its lower third (T3.28).
	minTopGap = 10.0
	// maxEstimateError of Grade.Graded against the render, code values of the luma: the render brings these untagged
	// synthetic sources up to 3 code values lighter than their measurement (about 0.99·Y′ + 4, with no grade at all).
	maxEstimateError = 4.0
	// maxWarmth of the red over the blue of a grey: ±3 of T3.25 was warmer than the trainer's frame.
	maxWarmth = 1.12
)

// TestRunnerGradeThirds (T3.28): a light top over a dark bottom, graded as the worker fits it
// (mediaworker.TestGradeForSource), comes out with the mean of the trainer's frame (95–105) as Grade.Graded
// estimates; with a dark middle (150 / 160 / 80) its upper third is at least 10 darker than the lower third, with a
// natural middle (140 / 115 / 90) the floor of the mean holds the shadow back (42 %). The white below the shadow
// stays white.
func TestRunnerGradeThirds(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	tests := []struct {
		name   string
		source string
		luma   Luma
		grade  *Grade
		gap    bool
	}{
		{name: "light-top", source: lightTopSource, luma: Luma{Mean: 130, Top: 150, Bottom: 80},
			grade: &Grade{Warmth: 2, Top: 0.63}, gap: true},
		{name: "natural-middle", source: naturalMiddleSource, luma: Luma{Mean: 115, Top: 140, Bottom: 90},
			grade: &Grade{Warmth: 2, Top: 0.42}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			r := newTestRunner(2 * time.Minute)
			dir := t.TempDir()

			src := makeSource(ctx, t, r, filepath.Join(dir, tt.name+".mp4"), tt.source)
			source, err := r.MeanLuma(ctx, src, []float64{1, 3}, false, 0)
			require.NoError(t, err)
			assert.InDelta(t, tt.luma.Mean, source.Mean, 4)
			assert.InDelta(t, tt.luma.Top, source.Top, 4)
			assert.InDelta(t, tt.luma.Bottom, source.Bottom, 4)

			cut := &Cut{
				Segments: []Segment{{Start: 0, End: 1.5}, {Start: 2, End: 3.5}},
				Options:  RenderOptions{Fade: 0.01, FrameRate: Rational{Num: 30, Den: 1}},
				Grade:    tt.grade,
			}
			reel := burnTestReel(ctx, t, r, filepath.Join(dir, mkdir(t, dir, "reel")), src, cut)

			graded, err := r.MeanLuma(ctx, reel, []float64{0.5, 2}, false, 0)
			require.NoError(t, err)

			white := makeStillSource(ctx, t, r, filepath.Join(dir, "white.mp4"), "white")
			_, whiteBottom := thirdsLuma(ctx, t, r, burnTestReel(ctx, t, r, filepath.Join(dir, mkdir(t, dir, "white")), white, cut))

			t.Logf("grade thirds, %s: source %.1f (top %.1f, bottom %.1f) → reel %.1f (top %.1f, bottom %.1f); white "+
				"bottom %.1f", tt.name, source.Mean, source.Top, source.Bottom, graded.Mean, graded.Top, graded.Bottom,
				whiteBottom)
			assert.GreaterOrEqual(t, graded.Mean, gradeMinLuma)
			assert.LessOrEqual(t, graded.Mean, gradeMaxLuma)
			assert.GreaterOrEqual(t, whiteBottom, minWhite, "the white stays white")
			assertGradedAsEstimated(t, tt.grade, source, graded)

			if tt.gap {
				assert.LessOrEqual(t, graded.Top, graded.Bottom-minTopGap, "the top of the frame is darker than its bottom")
			}
		})
	}
}

// assertGradedAsEstimated: the render of the grade measures as Grade.Graded estimates it from the source.
func assertGradedAsEstimated(t *testing.T, g *Grade, source, graded Luma) {
	t.Helper()

	estimate := g.Graded(source)
	t.Logf("grade estimate: %.1f (top %.1f, bottom %.1f), rendered %.1f (top %.1f, bottom %.1f)", estimate.Mean,
		estimate.Top, estimate.Bottom, graded.Mean, graded.Top, graded.Bottom)
	assert.InDelta(t, estimate.Mean, graded.Mean, maxEstimateError)
	assert.InDelta(t, estimate.Top, graded.Top, maxEstimateError)
	assert.InDelta(t, estimate.Bottom, graded.Bottom, maxEstimateError)
}

// TestGradeGraded: the estimate of the grade on the means of the thirds — the curve keeps the black and the white, the
// shadow of the top is nearly all in the upper third, a little in the middle one, none in the lower one; the grade of
// T3.25 on the source of live check 3 gives what the check measured (106.7 / 78.5).
func TestGradeGraded(t *testing.T) {
	t.Parallel()

	assert.InDelta(t, 0.978, topShadowTop, 0.001)
	assert.InDelta(t, 0.147, topShadowMiddle, 0.001)
	assert.Zero(t, topShadowBottom)

	tests := []struct {
		name     string
		grade    Grade
		luma     Luma
		expected Luma
	}{
		{name: "no grade", luma: Luma{Mean: 130, Top: 150, Bottom: 80}, expected: Luma{Mean: 130, Top: 150, Bottom: 80}},
		{name: "the black and the white stay", grade: Grade{Exposure: -0.5}, luma: Luma{Mean: 162, Top: 235, Bottom: 16},
			expected: Luma{Mean: 162, Top: 235, Bottom: 16}},
		{name: "a flat grey of our frame", grade: *testGrade, luma: Luma{Mean: 130, Top: 130, Bottom: 130},
			expected: Luma{Mean: 99.2, Top: 82.2, Bottom: 109.7}},
		{name: "live check 3: the grade of T3.25", grade: *testGrade, luma: Luma{Mean: 130, Top: 161, Bottom: 100},
			expected: Luma{Mean: 96.7, Top: 106.5, Bottom: 79}},
		{name: "a light top over a dark bottom", grade: Grade{Warmth: 2, Top: 0.63}, luma: Luma{Mean: 130, Top: 150, Bottom: 80},
			expected: Luma{Mean: 98, Top: 67.4, Bottom: 80}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.grade.Graded(tt.luma)
			assert.InDelta(t, tt.expected.Mean, got.Mean, 0.1)
			assert.InDelta(t, tt.expected.Top, got.Top, 0.1)
			assert.InDelta(t, tt.expected.Bottom, got.Bottom, 0.1)
		})
	}
}

// makeStillSource encodes 4 s of a still colour with a tone, as the render tests read it.
func makeStillSource(ctx context.Context, t *testing.T, r *Runner, path, color string) string {
	t.Helper()

	return makeSource(ctx, t, r, path, "color=c="+color+":size=540x960:rate=30:duration=4")
}

// makeSource encodes 4 s of the lavfi picture (540×960) with a tone.
func makeSource(ctx context.Context, t *testing.T, r *Runner, path, picture string) string {
	t.Helper()

	_, err := r.run(ctx, r.ffmpegPath, concat(commonArgs(),
		lavfiInput(picture),
		lavfiInput("sine=frequency=440:sample_rate=48000:duration=4"),
		[]string{videoCodecFlag, videoCodec, pixFmtFlag, videoPixFmt, audioCodecFlag, renderACodec, shortestFlag, path}), nil)
	require.NoError(t, err)

	return path
}

// mkdir makes the directory name in dir and returns the name.
func mkdir(t *testing.T, dir, name string) string {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(dir, name), 0o700))

	return name
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
			videoFilterFlag, "setparams=color_primaries=bt2020:color_trc=" + hlgTransfer + ":colorspace=bt2020nc",
			videoCodecFlag, losslessCodec, pixFmtFlag, "yuv420p10le", "-color_primaries", "bt2020", "-color_trc", hlgTransfer,
			"-colorspace", "bt2020nc", audioCodecFlag, renderACodec, shortestFlag, src,
		}), nil)
	require.NoError(t, err)

	source, err := r.MeanLuma(ctx, src, []float64{1, 3}, true, 0)
	require.NoError(t, err)
	assert.Greater(t, source.Mean, 16.0, "the tone mapped picture is not black")

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

	t.Logf("grade HDR: source %.1f; plain top %.1f bottom %.1f; graded top %.1f bottom %.1f", source.Mean, plainTop,
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

// thirdsLuma is the mean luma of the upper and the lower third of the key frame of the reel at or before 1 s (a reel
// of the render has one key frame: its first one).
func thirdsLuma(ctx context.Context, t *testing.T, r *Runner, path string) (top, bottom float64) {
	t.Helper()

	in, err := localFile(path)
	require.NoError(t, err)

	plane, err := r.lumaPlane(ctx, in, 1, false)
	require.NoError(t, err)
	require.Len(t, plane, lumaWidth*lumaHeight)

	luma := planeLuma(plane, lumaRect{bottom: lumaHeight, right: lumaWidth})

	return luma.Top, luma.Bottom
}

// redBlue are the mean red and blue of the lower third of the frame of the reel at 1 s (below the shadow of the top;
// accurate seek: that very frame).
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
			videoCodecFlag, videoCodec, "-preset", "ultrafast", "-crf", "23", "-g", "30", pixFmtFlag, videoPixFmt,
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
	// After T3.30: the zoomed pieces have a shadow of their own — two overlays with enable.
	after := &Cut{
		Segments: segments, Options: opts,
		Grade: &Grade{Exposure: testGrade.Exposure, Warmth: testGrade.Warmth, Top: testGrade.Top, ZoomTop: new(0.6)},
		Zoom:  &Zoom{Factor: 1.3, MinSegment: 1.2, Every: 2, Must: []float64{5.7, 15.2}},
	}

	started = time.Now()
	luma, err := r.MeanLuma(ctx, src, []float64{1, 6, 11, 16, 21, 26}, false, 1.3)
	require.NoError(t, err)
	require.NotNil(t, luma.Zoomed, "the zoom window measured in the same pass")
	t.Logf("render speed: the brightness of the 4K source (6 key frames, with the zoom window) measured in %.2f s: "+
		"mean luma %.1f, zoomed %.1f; zoomed share of the reel %.2f", time.Since(started).Seconds(), luma.Mean,
		luma.Zoomed.Mean, after.ZoomShare())

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
