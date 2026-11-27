package ffmpeg

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The zoom of the worker (MEDIA_WORKER_ZOOM 30) and the gap of the top under the bottom it fits (mediaworker.topGap).
const (
	workerZoom = 1.3
	// zoomEstimateError of Grade.Reel against the render: the scaled zoom window blurs the edges of the strips, and
	// the render of these untagged sources is a few code values off their measurement (maxEstimateError).
	zoomEstimateError = 5.0
	testTopGap        = 13.0
	testTopStep       = 0.01
	reelRate          = 30
)

// TestLumaZoomRect: the zoom window on the small frame of MeanLuma — the middle 1/1.3 across, its fixed point 40 %
// down (zoomWindow); none without a zoom.
func TestLumaZoomRect(t *testing.T) {
	t.Parallel()

	assert.Equal(t, &lumaRect{top: 10, bottom: 96, left: 7, right: 57}, lumaZoomRect(workerZoom))
	assert.Nil(t, lumaZoomRect(0), "no zoom")
	assert.Nil(t, lumaZoomRect(1), "no zoom")
}

// TestPlaneLuma: the thirds of the whole small frame (37 rows each) and of the zoom window (29 rows of its 86).
func TestPlaneLuma(t *testing.T) {
	t.Parallel()

	plane := make([]byte, lumaWidth*lumaHeight)
	for row := range lumaHeight {
		v := byte(row) // the row number: the means are the middles of the rows
		for col := range lumaWidth {
			plane[row*lumaWidth+col] = v
		}
	}

	frame := planeLuma(plane, lumaRect{bottom: lumaHeight, right: lumaWidth})
	assert.InDelta(t, 55.5, frame.Mean, 1e-9)
	assert.InDelta(t, 18, frame.Top, 1e-9, "rows 0–36")
	assert.InDelta(t, 93, frame.Bottom, 1e-9, "rows 75–111")

	zoomed := planeLuma(plane, *lumaZoomRect(workerZoom))
	assert.InDelta(t, 52.5, zoomed.Mean, 1e-9)
	assert.InDelta(t, 24, zoomed.Top, 1e-9, "rows 10–38")
	assert.InDelta(t, 81, zoomed.Bottom, 1e-9, "rows 67–95")
}

// TestGradeReel (T3.30): the estimate of the reel weighs the pieces with and without the zoom by their time; the zoomed
// ones are graded with their own shadow on the zoom window.
func TestGradeReel(t *testing.T) {
	t.Parallel()

	window := Luma{Mean: 134, Top: 172, Bottom: 90}
	luma := Luma{Mean: 130, Top: 153, Bottom: 105, Zoomed: &window}
	grade := &Grade{Warmth: 2, Top: 0.46, ZoomTop: new(0.63)}

	plain := grade.Graded(luma)
	zoomed := (&Grade{Warmth: 2, Top: 0.63}).Graded(window)

	assert.Equal(t, plain, grade.Reel(luma, 0), "nothing zoomed")
	assert.Equal(t, plain, grade.Reel(Luma{Mean: 130, Top: 153, Bottom: 105}, 0.5), "no window measured")
	assert.Equal(t, zoomed, grade.Reel(luma, 1), "all zoomed")

	halfway := grade.Reel(luma, 0.5)
	assert.InDelta(t, (plain.Top+zoomed.Top)/2, halfway.Top, 1e-9)
	assert.InDelta(t, (plain.Bottom+zoomed.Bottom)/2, halfway.Bottom, 1e-9)
	assert.InDelta(t, (plain.Mean+zoomed.Mean)/2, halfway.Mean, 1e-9)
	assert.LessOrEqual(t, halfway.Top, halfway.Bottom-testTopGap+0.5, "the top is darker than the bottom over the reel")

	assert.Equal(t, &Grade{Warmth: 2, Top: 0.63}, grade.Zoomed())
	assert.Equal(t, &Grade{Warmth: 2, Top: 0.46}, (&Grade{Warmth: 2, Top: 0.46}).Zoomed(), "ZoomTop nil: Top")
}

// TestCutZoomShare: the share of the time of the reel shown closer.
func TestCutZoomShare(t *testing.T) {
	t.Parallel()

	cut := &Cut{
		Segments: []Segment{{Start: 0, End: 1.5}, {Start: 2, End: 3.5}, {Start: 4, End: 7}},
		Options:  RenderOptions{FrameRate: Rational{Num: reelRate, Den: 1}},
		Zoom:     &Zoom{Factor: workerZoom, MinSegment: 0.5, Every: 1},
	}

	require.Equal(t, []bool{false, true, false}, cut.ZoomedSegments())
	assert.InDelta(t, 0.25, cut.ZoomShare(), 1e-9)
	assert.Zero(t, (&Cut{Segments: cut.Segments}).ZoomShare(), "no zoom")
}

// The source of T3.30, synthetic: as the trainer's frame, a light wall over the middle of the frame and darker
// clothes, with a darker strip at the very top and a lighter one at the bottom (Y′ about 100 / 175 / 140 / 90 / 125 by
// rows 0–0.1–0.35–0.6–0.86–1). The thirds of the frame are about 153 / 105; the 1.3× zoom window shows the rows
// 0.09–0.86 — its thirds about 172 / 90: under the shadow of the frame its top is as light as its bottom (live checks 4
// and 5: 88 / 88).
const zoomLightSource = "color=c=0x7f7f7f:size=540x960:rate=30:duration=4," +
	"drawbox=x=0:y=0:w=540:h=96:color=0x626262:t=fill,drawbox=x=0:y=96:w=540:h=240:color=0xb9b9b9:t=fill," +
	"drawbox=x=0:y=336:w=540:h=240:color=0x909090:t=fill,drawbox=x=0:y=576:w=540:h=250:color=0x565656:t=fill"

// TestRunnerGradeZoomThirds (T3.30): half of the reel is zoomed 1.3×. MeanLuma measures the zoom window of the source
// in the same pass; with the shadow of the top fitted to the frame and to the window separately (each estimated topGap
// under its bottom), the upper third of the reel is at least 10 darker than its lower third over the whole reel and
// on the zoomed piece, the mean within 95–105, and the render measures as Grade.Reel estimates it. The shadow of the
// frame alone would leave the zoomed piece with its top as light as its bottom.
func TestRunnerGradeZoomThirds(t *testing.T) {
	t.Parallel()
	requireFFmpeg(t)

	ctx := t.Context()
	r := newTestRunner(2 * time.Minute)
	dir := t.TempDir()

	src := makeSource(ctx, t, r, filepath.Join(dir, "source.mp4"), zoomLightSource)
	source, err := r.MeanLuma(ctx, src, []float64{1, 3}, false, workerZoom)
	require.NoError(t, err)
	require.NotNil(t, source.Zoomed, "the zoom window is measured")

	window := *source.Zoomed
	t.Logf("source: frame %.1f (top %.1f, bottom %.1f), zoom window %.1f (top %.1f, bottom %.1f)", source.Mean,
		source.Top, source.Bottom, window.Mean, window.Top, window.Bottom)
	assert.Greater(t, window.Top, source.Top+10, "the window shows less of the dark strip at the top")
	assert.Less(t, window.Bottom, source.Bottom-10, "and less of the light strip at the bottom")

	top, zoomTop := fitTestTop(source), fitTestTop(window)
	frameOnly := &Grade{Warmth: 2, Top: top}
	require.Greater(t, frameOnly.Graded(window).Top, frameOnly.Graded(window).Bottom-testTopGap,
		"the shadow of the frame does not darken the zoomed top enough")

	grade := &Grade{Warmth: 2, Top: top, ZoomTop: new(zoomTop)}
	cut := &Cut{
		Segments: []Segment{{Start: 0, End: 1.5}, {Start: 2, End: 3.5}},
		Options:  RenderOptions{Fade: 0.01, FrameRate: Rational{Num: reelRate, Den: 1}},
		Zoom:     &Zoom{Factor: workerZoom, MinSegment: 0.5, Every: 1},
		Grade:    grade,
	}
	require.InDelta(t, 0.5, cut.ZoomShare(), 1e-9)

	reel := burnTestReel(ctx, t, r, filepath.Join(dir, mkdir(t, dir, "reel")), src, cut)
	frames := reelFrames(ctx, t, r, reel)
	whole, plain, zoomed := framesLuma(frames, 0, len(frames)), framesLuma(frames, 0, len(frames)/2),
		framesLuma(frames, len(frames)/2, len(frames))
	estimate := grade.Reel(source, cut.ZoomShare())

	t.Logf("grade: top %.2f, zoomed top %.2f; reel %.1f (top %.1f, bottom %.1f), estimated %.1f (top %.1f, bottom "+
		"%.1f); plain piece top %.1f / bottom %.1f, zoomed piece %.1f / %.1f", top, zoomTop, whole.Mean, whole.Top,
		whole.Bottom, estimate.Mean, estimate.Top, estimate.Bottom, plain.Top, plain.Bottom, zoomed.Top, zoomed.Bottom)
	assert.LessOrEqual(t, whole.Top, whole.Bottom-minTopGap, "the top is darker than the bottom over the whole reel")
	assert.LessOrEqual(t, zoomed.Top, zoomed.Bottom-minTopGap, "and on the zoomed piece")
	assert.LessOrEqual(t, plain.Top, plain.Bottom-minTopGap, "and on the plain one")
	assert.GreaterOrEqual(t, whole.Mean, gradeMinLuma)
	assert.LessOrEqual(t, whole.Mean, gradeMaxLuma)
	assert.InDelta(t, estimate.Mean, whole.Mean, zoomEstimateError)
	assert.InDelta(t, estimate.Top, whole.Top, zoomEstimateError)
	assert.InDelta(t, estimate.Bottom, whole.Bottom, zoomEstimateError)

	// The joint where the framing changes: the last frame before it has the shadow of the frame, the first after it the
	// shadow of the zoomed pieces — the shadow changes with the zoom, on the same frame.
	joint := len(frames) / 2
	before := planeLuma(frames[joint-1], lumaRect{bottom: lumaHeight, right: lumaWidth})
	after := planeLuma(frames[joint], lumaRect{bottom: lumaHeight, right: lumaWidth})
	plainEstimate, zoomedEstimate := grade.Graded(source), grade.Zoomed().Graded(window)

	t.Logf("joint: before %.1f / %.1f (estimated %.1f / %.1f), after %.1f / %.1f (estimated %.1f / %.1f)", before.Top,
		before.Bottom, plainEstimate.Top, plainEstimate.Bottom, after.Top, after.Bottom, zoomedEstimate.Top,
		zoomedEstimate.Bottom)
	assert.InDelta(t, plainEstimate.Top, before.Top, zoomEstimateError, "the shadow of the frame before the joint")
	assert.InDelta(t, plainEstimate.Bottom, before.Bottom, zoomEstimateError)
	assert.InDelta(t, zoomedEstimate.Top, after.Top, zoomEstimateError, "the shadow of the zoomed pieces after it")
	assert.InDelta(t, zoomedEstimate.Bottom, after.Bottom, zoomEstimateError)
}

// fitTestTop is the weakest shadow of the top (no exposure curve) that takes the upper third of the picture topGap
// under its lower third, as the worker fits it (mediaworker.gradeSettings.topFor).
func fitTestTop(l Luma) float64 {
	for top := 0.0; top <= maxTopShadow; top += testTopStep {
		if g := (&Grade{Top: top}).Graded(l); g.Top <= g.Bottom-testTopGap {
			return math.Round(top/testTopStep) * testTopStep
		}
	}

	return maxTopShadow
}

// reelFrames are the luma planes (lumaWidth×lumaHeight) of all the frames of the reel, as MeanLuma sees a frame.
func reelFrames(ctx context.Context, t *testing.T, r *Runner, path string) [][]byte {
	t.Helper()

	in, err := localFile(path)
	require.NoError(t, err)

	const maxFrames = 200

	filter := "scale=w=64:h=112:out_color_matrix=" + bt709 + ":out_range=tv,format=" + videoPixFmt
	out, err := r.runLimited(ctx, r.ffmpegPath, concat(commonArgs(), inputArgs(in),
		[]string{videoFilterFlag, filter, "-an", formatFlag, rawVideoFormat, "-"}), maxFrames*lumaFrameBytes)
	require.NoError(t, err)
	require.Zero(t, len(out)%lumaFrameBytes)

	frames := make([][]byte, 0, len(out)/lumaFrameBytes)
	for at := 0; at < len(out); at += lumaFrameBytes {
		frames = append(frames, []byte(out[at:at+lumaWidth*lumaHeight]))
	}

	require.Len(t, frames, 3*reelRate, "3 s of the reel")

	return frames
}

// framesLuma is the mean luma of the frames [from, to).
func framesLuma(frames [][]byte, from, to int) Luma {
	var sum Luma

	for _, plane := range frames[from:to] {
		sum = sum.plus(planeLuma(plane, lumaRect{bottom: lumaHeight, right: lumaWidth}))
	}

	return sum.scaled(1 / float64(to-from))
}
