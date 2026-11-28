package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// The colour of the draft as the trainer grades her own reels (T3.25): a darker, warmer frame with the top, where the
// subtitles stand over a light wall, in a soft shadow.
const (
	// lumaBlack and lumaRange of the limited-range Y' (8 bits): black 16, white 16 + 219 = 235. The exposure curve
	// works on the luma between them.
	lumaBlack = 16
	lumaRange = 219
	// maxTopShadow: the top edge loses at most this share of its light.
	maxTopShadow = 0.8
	// maxGradeWarmth of the chroma shift, code values: more is a colour cast, not warmth.
	maxGradeWarmth = 12
	// minExposureEV of the midtones: a darker frame is underexposed, not graded.
	minExposureEV = -1.5
	// The shadow of the top: full over gradeTopPlateau of the height, then a cosine fade to nothing at gradeTopHeight
	// — the upper third is dark, the middle of the frame (the face) nearly untouched.
	gradeTopPlateau = 0.25
	gradeTopHeight  = 0.5

	gradedLabel   = "graded"
	gradeTopLabel = "gtop"
	// zoomLabelSuffix of the labels of the shadow of the zoomed pieces (Grade.ZoomTop).
	zoomLabelSuffix = "z"

	// The luma of a source is measured on small frames: their mean is the mean of the picture.
	lumaWidth  = 64
	lumaHeight = 112
	// lumaFrameBytes of a yuv420p frame of lumaWidth×lumaHeight (the luma plane, then the two chroma planes).
	lumaFrameBytes = lumaWidth * lumaHeight * 3 / 2
	// maxLuma of an 8-bit code value.
	maxLuma = 255
	// frameThird of the height of the frame.
	frameThird = 1.0 / 3
)

// The shares of the shadow of the top (the mean of its alpha over the rows, 0..1) in the upper, the middle and the lower
// third of the frame: 0.98, 0.15 and 0 — the top third is nearly all in the full shadow, the middle gets its fade.
var (
	topShadowTop    = shadeShare(0, frameThird)
	topShadowMiddle = shadeShare(frameThird, 2*frameThird)
	topShadowBottom = shadeShare(2*frameThird, 1)
)

var (
	ErrInvalidGrade = errors.New("grade must have the exposure within -1.5..0 EV, the warmth 0..12 and the tops 0..0.8")
	errNoLuma       = errors.New("no frame decoded at the sampled moments")
)

// Grade is the colour of a talking head: the exposure, a slight warmth and the shadow of the top of the frame. It works
// on the output frame after the fit, the tone mapping and the zoom (the shadow stays at the top of the frame when the
// picture is zoomed), and under the subtitles, which keep their colour.
type Grade struct {
	// Exposure of the midtones, EV ≤ 0: a power curve of the encoded luma, x = (Y' − 16) / 219 → x^(1 − Exposure),
	// takes the middle grey (x = ½) down by Exposure EV (−0.3: × 0.81), the shadows a little more, and keeps the black
	// and the white where they are — the frame is darker, not flatter. On the encoded luma, not linear light: −0.3 EV of
	// the midtones is about −0.7 EV of light, what darkens a bright phone frame to the trainer's.
	Exposure float64
	// Warmth, code values of the chroma: Cb is lowered (less blue) and Cr raised (more red) by it. The black bars of
	// pad get it too (RGB about 5, 0, 0): too dark to see.
	Warmth int
	// Top is the share of the light the top edge of the frame loses (0.3: 30 % darker), fading to nothing by the
	// middle of the frame; 0 — none.
	Top float64
	// ZoomTop is Top of the pieces shown closer (Cut.Zoom, T3.30): the zoom window shows other rows of the picture in
	// the thirds of the frame (at 1.3× the upper third shows the rows 0.09–0.35 of the frame, the lower 0.61–0.86), so
	// they need a shadow of their own for the same top. nil — Top.
	ZoomTop *float64
}

// zoomTop is the shadow of the top of the zoomed pieces.
func (g *Grade) zoomTop() float64 {
	if g.ZoomTop == nil {
		return g.Top
	}

	return *g.ZoomTop
}

// Zoomed is the grade of the pieces shown closer: the shadow of their top is ZoomTop.
func (g *Grade) Zoomed() *Grade {
	z := *g
	z.Top, z.ZoomTop = g.zoomTop(), nil

	return &z
}

// validate checks the grade before it reaches the graph.
func (g *Grade) validate() error {
	zoomTop := g.zoomTop()
	if math.IsNaN(g.Exposure) || g.Exposure > 0 || g.Exposure < minExposureEV || g.Warmth < 0 ||
		g.Warmth > maxGradeWarmth || !validTop(g.Top) || !validTop(zoomTop) {
		return fmt.Errorf("exposure %.2f EV, warmth %d, top %.2f, zoomed top %.2f: %w", g.Exposure, g.Warmth, g.Top,
			zoomTop, ErrInvalidGrade)
	}

	return nil
}

func validTop(top float64) bool {
	return !math.IsNaN(top) && top >= 0 && top <= maxTopShadow
}

// gradeGraph continues the chain of the output frame with the grade: one lookup table for the exposure curve and the
// warmth (lutyuv: the expression is computed once per code value, a table per plane — the cheapest filter of the graph), then the shadow of the top as one generated frame laid
// over it (the shade's gradient, overlay repeats its only frame): no per-pixel expression runs on the video. zoomed is
// the expression of the zoomed spans of the reel (zoomEnable, "" — none): with a ZoomTop of its own there are two
// shadows, each laid over its own frames only (overlay with enable: a frame of the other passes through as it is),
// so every frame still gets one overlay. The chain goes on after it.
func gradeGraph(g *Grade, width, height int, zoomed string) (string, error) {
	if err := g.validate(); err != nil {
		return "", err
	}

	black, luma := strconv.Itoa(lumaBlack), strconv.Itoa(lumaRange)
	warmth := strconv.Itoa(g.Warmth)
	graph := ",format=" + videoPixFmt + ",lutyuv=y='" + black + "+" + luma + "*pow((clip(val," + black + "," +
		strconv.Itoa(lumaBlack+lumaRange) + ")-" + black + ")/" + luma + "," + formatPrecise(1-g.Exposure) + ")'" +
		":u='val-" + warmth + "':v='val+" + warmth + "'"

	shadows := []topShadow{{top: g.Top}}
	if zoomed != "" && g.zoomTop() != g.Top {
		shadows = []topShadow{
			{top: g.Top, enable: "not(" + zoomed + ")"},
			{top: g.zoomTop(), enable: zoomed, suffix: zoomLabelSuffix},
		}
	}

	for _, shadow := range shadows {
		overlay, err := shadow.graph(width, height)
		if err != nil {
			return "", err
		}

		graph += overlay
	}

	return graph, nil
}

// topShadow is one overlay of the shadow of the top: its strength, the frames it is laid over (enable, "" — all) and
// the suffix of its labels.
type topShadow struct {
	top    float64
	enable string
	suffix string
}

// graph is the overlay continuing the chain; empty without a shadow.
func (t *topShadow) graph(width, height int) (string, error) {
	alpha := int(math.Round(t.top * maxAlpha))
	if alpha == 0 {
		return "", nil
	}

	shade := &Shade{
		Height:  evenFloor(float64(height) * gradeTopHeight),
		Plateau: evenFloor(float64(height) * gradeTopPlateau),
		Alpha:   alpha,
	}

	source, y, err := shadeSource(shade, width, height)
	if err != nil {
		return "", err
	}

	graded, top := gradedLabel+t.suffix, gradeTopLabel+t.suffix
	overlay := "[" + graded + "];" + source + "[" + top + "];" + "[" + graded + "][" + top + "]overlay=x=0:y=" + y
	if t.enable != "" {
		overlay += ":enable='" + t.enable + "'"
	}

	return overlay, nil
}

// Luma is the brightness of a picture, Y' of the limited range (8 bits: 16 black, 235 white): its mean and the means of
// its upper and lower thirds.
type Luma struct {
	Mean   float64
	Top    float64
	Bottom float64
	// Zoomed is the luma of the zoom window (the picture of a zoomed piece: MeanLuma with a zoom); nil — not measured.
	Zoomed *Luma
}

// Middle is the mean of the middle third: the thirds are equal, the mean is the mean of the three.
func (l Luma) Middle() float64 {
	return math.Min(maxLuma, math.Max(0, 3*l.Mean-l.Top-l.Bottom))
}

// Graded estimates the luma of a picture after the grade, as gradeGraph renders it: the exposure curve of the mean of
// each third, then the shadow of the top by its share in the third (overlay of black: Y' → 16 + (Y' − 16)·(1 − α)).
// The curve is nearly straight over the light of a third, so the estimate is within a couple of code values of the
// render: about ±1 on a grey, up to ±3 on untagged synthetic sources (TestRunnerGradeThirds). The warmth does not
// change the luma. It is the frame without the zoom (Reel: the whole reel with its zoomed pieces).
func (g *Grade) Graded(l Luma) Luma {
	top := shaded(g.curve(l.Top), g.Top*topShadowTop)
	middle := shaded(g.curve(l.Middle()), g.Top*topShadowMiddle)
	bottom := shaded(g.curve(l.Bottom), g.Top*topShadowBottom)

	return Luma{Mean: (top + middle + bottom) / 3, Top: top, Bottom: bottom}
}

// Reel estimates the luma of the whole reel (T3.30): Graded of the frame on the plain pieces and of the zoom window
// (l.Zoomed, under ZoomTop — the shadow is laid after the zoom, at the top of the output frame) on the zoomed ones,
// weighted by their share of the time of the reel (Cut.ZoomShare). The frame's estimate without a measured window.
func (g *Grade) Reel(l Luma, zoomShare float64) Luma {
	plain := g.Graded(l)
	if l.Zoomed == nil || zoomShare <= 0 {
		return plain
	}

	share := math.Min(1, zoomShare)
	zoomed := g.Zoomed().Graded(*l.Zoomed)
	mix := func(a, b float64) float64 { return a*(1-share) + b*share }

	return Luma{Mean: mix(plain.Mean, zoomed.Mean), Top: mix(plain.Top, zoomed.Top), Bottom: mix(plain.Bottom, zoomed.Bottom)}
}

// curve is the exposure curve of the lookup table of gradeGraph on one code value.
func (g *Grade) curve(v float64) float64 {
	x := (math.Min(lumaBlack+lumaRange, math.Max(lumaBlack, v)) - lumaBlack) / lumaRange

	return lumaBlack + lumaRange*math.Pow(x, 1-g.Exposure)
}

// shaded is the luma under black laid over it with the alpha share.
func shaded(v, alpha float64) float64 {
	return lumaBlack + (v-lumaBlack)*(1-alpha)
}

// shadeShare is the mean alpha (a share of the full one) of the shadow of the top between the heights from and to,
// shares of the frame's height from its top edge: full to gradeTopPlateau, then (1 + cos(π·t)) / 2 to gradeTopHeight.
func shadeShare(from, to float64) float64 {
	return (shadeIntegral(to) - shadeIntegral(from)) / (to - from)
}

// shadeIntegral is the integral of the alpha share of the shadow of the top from the top edge to the height y.
func shadeIntegral(y float64) float64 {
	const half = 0.5

	fade := gradeTopHeight - gradeTopPlateau

	switch {
	case y <= gradeTopPlateau:
		return y
	case y < gradeTopHeight:
		return gradeTopPlateau + half*(y-gradeTopPlateau) + fade/(2*math.Pi)*math.Sin(math.Pi*(y-gradeTopPlateau)/fade)
	default:
		return gradeTopPlateau + half*fade
	}
}

func evenFloor(v float64) int {
	return 2 * int(v/2)
}

// MeanLuma is the luma of the picture of the source at the moments, seconds (Luma: the mean and the means of the
// upper and the lower third, measured on the same frames in the same pass): one key frame at or before each moment is
// decoded (no other frame is), brought to SDR bt709 as the render brings it (toneMap: an HLG/PQ source) and averaged
// on a small copy. With a zoom factor over 1 (Zoom.Factor) the window the zoom shows (zoomWindow) is measured on the
// same small copy too (Luma.Zoomed, T3.30). A 4K frame costs a fraction of a second. A moment that gives no frame (past
// the end) or fails to decode is skipped; it fails only when no moment gave a frame, or with the context. Moments
// within one GOP measure the same key frame (a file with a single key frame, such as a reel, measures its first frame
// at every moment).
func (r *Runner) MeanLuma(ctx context.Context, path string, moments []float64, toneMap bool, zoom float64) (Luma, error) {
	in, err := localFile(path)
	if err != nil {
		return Luma{}, err
	}

	var (
		sum, zoomed Luma
		frames      int
		failed      error
	)

	window := lumaZoomRect(zoom)

	for _, at := range moments {
		plane, err := r.lumaPlane(ctx, in, at, toneMap)
		if ctx.Err() != nil {
			return Luma{}, fmt.Errorf("failed to measure the picture: %w", ctx.Err())
		}

		if err != nil {
			failed = err

			continue
		}

		if plane == nil {
			continue
		}

		sum = sum.plus(planeLuma(plane, lumaRect{bottom: lumaHeight, right: lumaWidth}))
		if window != nil {
			zoomed = zoomed.plus(planeLuma(plane, *window))
		}

		frames++
	}

	if frames == 0 {
		return Luma{}, fmt.Errorf("failed to measure the picture: %w", errors.Join(errNoLuma, failed))
	}

	n := float64(frames)
	out := sum.scaled(1 / n)

	if window != nil {
		out.Zoomed = new(zoomed.scaled(1 / n))
	}

	return out, nil
}

// plus is the sum of the means and thirds of two pictures (Zoomed is dropped).
func (l Luma) plus(o Luma) Luma {
	return Luma{Mean: l.Mean + o.Mean, Top: l.Top + o.Top, Bottom: l.Bottom + o.Bottom}
}

// scaled is the means and thirds times k (Zoomed is dropped).
func (l Luma) scaled(k float64) Luma {
	return Luma{Mean: l.Mean * k, Top: l.Top * k, Bottom: l.Bottom * k}
}

// lumaRect is a window of the small frame of MeanLuma: the rows [top, bottom) and the columns [left, right).
type lumaRect struct {
	top, bottom, left, right int
}

// lumaZoomRect is the window of the zoom of the factor on the small frame (zoomWindow: the middle across, its fixed
// point zoomAnchorY down); nil without a zoom.
func lumaZoomRect(factor float64) *lumaRect {
	if math.IsNaN(factor) || factor <= 1 {
		return nil
	}

	h, w := float64(lumaHeight)/factor, float64(lumaWidth)/factor
	y, x := (float64(lumaHeight)-h)*zoomAnchorY, (float64(lumaWidth)-w)/2

	return &lumaRect{
		top: int(math.Round(y)), bottom: int(math.Round(y + h)), left: int(math.Round(x)), right: int(math.Round(x + w)),
	}
}

// planeLuma is the luma of the window of the luma plane: its mean and the means of its upper and lower thirds (the
// whole small frame: 37 rows each).
func planeLuma(plane []byte, r lumaRect) Luma {
	third := int(math.Round(float64(r.bottom-r.top) * frameThird))

	return Luma{
		Mean:   rectMean(plane, r),
		Top:    rectMean(plane, lumaRect{top: r.top, bottom: r.top + third, left: r.left, right: r.right}),
		Bottom: rectMean(plane, lumaRect{top: r.bottom - third, bottom: r.bottom, left: r.left, right: r.right}),
	}
}

// rectMean is the mean of the window of the luma plane (lumaWidth wide).
func rectMean(plane []byte, r lumaRect) float64 {
	sum, count := 0, 0

	for row := r.top; row < r.bottom; row++ {
		for _, v := range plane[row*lumaWidth+r.left : row*lumaWidth+r.right] {
			sum += int(v)
		}

		count += r.right - r.left
	}

	if count == 0 {
		return 0
	}

	return float64(sum) / float64(count)
}

// lumaPlane is the luma plane (lumaWidth×lumaHeight) of the key frame at or before at; nil if none was decoded.
func (r *Runner) lumaPlane(ctx context.Context, in string, at float64, toneMap bool) ([]byte, error) {
	size := "scale=w=" + strconv.Itoa(lumaWidth) + ":h=" + strconv.Itoa(lumaHeight)

	filter := size + ":out_color_matrix=" + bt709 + ":out_range=tv,format=" + videoPixFmt
	if toneMap {
		filter = size + "," + toneMapFilter
	}

	// -copyts: without it the key frame before the moment gets a negative timestamp and is dropped.
	args := append(commonArgs(), "-copyts", "-noaccurate_seek", "-skip_frame", "nokey", seekFlag, formatPrecise(at))
	args = append(args, inputArgs(in)...)
	args = append(args, "-frames:v", "1", "-an", "-vf", filter, formatFlag, "rawvideo", "-")

	out, err := r.runLimited(ctx, r.ffmpegPath, args, lumaFrameBytes)
	if err != nil {
		return nil, err
	}

	if len(out) < lumaFrameBytes {
		return nil, nil
	}

	return []byte(out[:lumaWidth*lumaHeight]), nil
}
