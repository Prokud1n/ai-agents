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
	// lumaBlack is the black of the limited-range Y' (8 bits); the exposure scales the luma above it.
	lumaBlack = 16
	// maxGradeTop: the top edge loses at most this share of its light.
	maxGradeTop = 0.8
	// maxGradeWarmth of the chroma shift, code values: more is a colour cast, not warmth.
	maxGradeWarmth = 12
	// minGradeExposure, EV: a darker frame is underexposed, not graded.
	minGradeExposure = -1.5
	// The shadow of the top: full over gradeTopPlateau of the height, then a cosine fade to nothing at gradeTopHeight
	// — the upper third is dark, the middle of the frame (the face) nearly untouched.
	gradeTopPlateau = 0.25
	gradeTopHeight  = 0.5

	gradedLabel   = "graded"
	gradeTopLabel = "gtop"

	// The luma of a source is measured on small frames: their mean is the mean of the picture.
	lumaWidth  = 64
	lumaHeight = 112
	// lumaFrameBytes of a yuv420p frame of lumaWidth×lumaHeight (the luma plane, then the two chroma planes).
	lumaFrameBytes = lumaWidth * lumaHeight * 3 / 2
)

var (
	ErrInvalidGrade = errors.New("grade must have the exposure within -1.5..0 EV, the warmth 0..12 and the top 0..0.8")
	errNoLuma       = errors.New("no frame decoded at the sampled moments")
)

// Grade is the colour of a talking head: the exposure, a slight warmth and the shadow of the top of the frame. It works
// on the output frame after the fit, the tone mapping and the zoom (the shadow stays at the top of the frame when the
// picture is zoomed), and under the subtitles, which keep their colour.
type Grade struct {
	// Exposure, EV ≤ 0, on the encoded luma: Y' − 16 is multiplied by 2^Exposure (−0.3 EV: × 0.81). It is not linear
	// light: −0.3 EV here is about −0.7 EV of light, what darkens a bright phone frame to the trainer's.
	Exposure float64
	// Warmth, code values of the chroma: Cb is lowered (less blue) and Cr raised (more red) by it.
	Warmth int
	// Top is the share of the light the top edge of the frame loses (0.3: 30 % darker), fading to nothing by the
	// middle of the frame; 0 — none.
	Top float64
}

// validate checks the grade before it reaches the graph.
func (g *Grade) validate() error {
	if math.IsNaN(g.Exposure) || g.Exposure > 0 || g.Exposure < minGradeExposure || g.Warmth < 0 ||
		g.Warmth > maxGradeWarmth || math.IsNaN(g.Top) || g.Top < 0 || g.Top > maxGradeTop {
		return fmt.Errorf("exposure %.2f EV, warmth %d, top %.2f: %w", g.Exposure, g.Warmth, g.Top, ErrInvalidGrade)
	}

	return nil
}

// gradeGraph continues the chain of the output frame with the grade: one lookup table for the exposure and the warmth
// (lutyuv: a table per plane, the cheapest filter of the graph), then the shadow of the top as one generated frame laid
// over it (the shade's gradient, overlay repeats its only frame): no per-pixel expression runs on the video. The chain
// goes on after it.
func gradeGraph(g *Grade, width, height int) (string, error) {
	if err := g.validate(); err != nil {
		return "", err
	}

	gain := math.Exp2(g.Exposure)
	warmth := strconv.Itoa(g.Warmth)
	graph := ",format=" + videoPixFmt + ",lutyuv=y='" + strconv.Itoa(lumaBlack) + "+(val-" + strconv.Itoa(lumaBlack) +
		")*" + formatPrecise(gain) + "':u='val-" + warmth + "':v='val+" + warmth + "'"

	alpha := int(math.Round(g.Top * maxAlpha))
	if alpha == 0 {
		return graph, nil
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

	return graph + "[" + gradedLabel + "];" + source + "[" + gradeTopLabel + "];" +
		"[" + gradedLabel + "][" + gradeTopLabel + "]overlay=x=0:y=" + y, nil
}

func evenFloor(v float64) int {
	return 2 * int(v/2)
}

// MeanLuma is the mean luma (Y' of the limited range, 8 bits: 16 black, 235 white) of the picture of the source at the
// moments, seconds: one key frame at or before each moment is decoded (no other frame is), brought to SDR bt709 as
// the render brings it (toneMap: an HLG/PQ source) and averaged on a small copy. A 4K frame costs a fraction of a
// second; a moment past the end gives no frame and is skipped. It fails only when no moment gave a frame.
func (r *Runner) MeanLuma(ctx context.Context, path string, moments []float64, toneMap bool) (float64, error) {
	in, err := localFile(path)
	if err != nil {
		return 0, err
	}

	sum, frames := 0.0, 0

	for _, at := range moments {
		plane, err := r.lumaPlane(ctx, in, at, toneMap)
		if err != nil {
			return 0, fmt.Errorf("failed to measure the picture: %w", err)
		}

		if plane == nil {
			continue
		}

		sum += meanByte(plane)
		frames++
	}

	if frames == 0 {
		return 0, fmt.Errorf("failed to measure the picture: %w", errNoLuma)
	}

	return sum / float64(frames), nil
}

// lumaPlane is the luma plane (lumaWidth×lumaHeight) of the key frame at or before at; nil if none was decoded.
func (r *Runner) lumaPlane(ctx context.Context, in string, at float64, toneMap bool) ([]byte, error) {
	size := "scale=w=" + strconv.Itoa(lumaWidth) + ":h=" + strconv.Itoa(lumaHeight)

	filter := size + ":out_color_matrix=" + bt709 + ":out_range=tv,format=" + videoPixFmt
	if toneMap {
		filter = size + "," + toneMapFilter
	}

	args := append(commonArgs(), "-noaccurate_seek", "-skip_frame", "nokey", seekFlag, formatPrecise(at))
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

func meanByte(b []byte) float64 {
	sum := 0

	for _, v := range b {
		sum += int(v)
	}

	return float64(sum) / float64(len(b))
}
