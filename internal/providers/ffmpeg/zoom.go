package ffmpeg

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	// zoomAnchorY is the point of the frame that stays in place when it is zoomed in: the middle across, 40 % of the
	// height down — the face of a talking head, above the middle of a vertical frame.
	zoomAnchorY = 0.4
	// maxZoomFactor: a stronger zoom of a 1080×1920 frame loses visible sharpness.
	maxZoomFactor = 1.5
	// zoomEpsilon: segment edges in seconds without a known frame rate; a frame is never this short.
	zoomEpsilon = 1e-3
	half        = 0.5

	zoomPlainLabel = "zn"
	zoomCloseLabel = "zs"
	zoomedLabel    = "zz"
)

var ErrInvalidZoom = errors.New("zoom factor must be within (1, 1.5], the shortest zoomed segment and the joints between changes not negative")

// Zoom is the jump-cut zoom of a talking head: at the joints of the cut the framing changes, the segments are shown
// alternately as they are and closer, still within a segment (a jump, no animation). With Every 2 it changes at every
// second joint, so every other joint is hidden by the change of the framing and the zoomed shot lasts; a joint at a
// moment of Must always moves closer (zoomPlan.closeIn). A segment shorter than MinSegment keeps the framing of the one before it, so short
// pieces do not flicker, and its joint is not counted.
type Zoom struct {
	// Factor > 1: 1.3 shows the middle 1/1.3 of the frame, 30 % closer.
	Factor float64
	// MinSegment, seconds of the reel.
	MinSegment float64
	// Every: the framing changes at every Every-th counted joint; 0 or 1 — at every joint (T3.15).
	Every int
	// Must are moments of the reel, seconds (the start of a section: its question), where the framing moves closer: at the
	// first counted joint from mustLead before the moment to mustWindow after it; the count of Every starts again.
	Must []float64
}

// The joint the framing must change at is looked for around a moment of Zoom.Must, seconds: the section heading is put
// at the start of its phrase, the question follows it within a few seconds (the heading is shown about 2 s).
const (
	mustLead   = 0.3
	mustWindow = 4.0
)

// ZoomedSegments says which segments of the cut are shown closer; all false without a zoom.
func (c *Cut) ZoomedSegments() []bool {
	if c.Zoom == nil {
		return make([]bool, len(c.Segments))
	}

	lengths := segmentLengths(c.Segments, c.Options.FrameRate)
	plan := zoomPlan{zoomed: make([]bool, len(lengths)), every: max(c.Zoom.Every, 1), last: -1}
	must := mustJoints(lengths, c.Zoom)

	for i, d := range lengths {
		if i > 0 && d >= c.Zoom.MinSegment {
			if must[i] {
				plan.closeIn(i)
			} else {
				plan.count(i)
			}
		}

		plan.zoomed[i] = plan.closer
	}

	return plan.zoomed
}

// zoomPlan is the framing of the segments decided so far, joint by joint.
type zoomPlan struct {
	zoomed []bool
	every  int
	closer bool
	// counted joints since the last change of the framing.
	counted int
	// changed is the segment the current framing starts at; joint the last counted joint (a long segment) before the
	// current one; last the segment of the last moment of Zoom.Must (-1: none yet).
	changed, joint, last int
}

// count is an ordinary joint at segment i: the framing changes at every every-th one.
func (p *zoomPlan) count(i int) {
	p.counted++

	if p.counted >= p.every {
		p.closer, p.counted, p.changed = !p.closer, 0, i
	}

	p.joint = i
}

// closeIn is the joint at segment i where a section starts: the framing moves closer there, as the trainer zooms in on
// her questions. If the frame is already closer, it goes back to the plain one earlier — at the last joint before i,
// or where the closer shot began if no joint is between — so that i is a change from the plain frame to the closer
// one. A closer shot that a section began just before, with no joint between, stays: the earlier section keeps its
// zoom. The count of every starts again.
func (p *zoomPlan) closeIn(i int) {
	switch {
	case !p.closer:
	case p.joint > p.changed:
		p.unzoom(p.joint, i)
	case p.changed != p.last:
		p.unzoom(p.changed, i)
	}

	p.closer, p.counted, p.changed, p.joint, p.last = true, 0, i, i, i
}

// unzoom shows the segments [from, to) plain.
func (p *zoomPlan) unzoom(from, to int) {
	for k := from; k < to; k++ {
		p.zoomed[k] = false
	}
}

// mustJoints marks the segments whose joint must move the framing closer: for each moment of z.Must the first segment of at
// least MinSegment that starts within [moment - mustLead, moment + mustWindow].
func mustJoints(lengths []float64, z *Zoom) []bool {
	must := make([]bool, len(lengths))

	for _, moment := range z.Must {
		at := 0.0

		for i, d := range lengths {
			if i > 0 && d >= z.MinSegment && at >= moment-mustLead && at <= moment+mustWindow {
				must[i] = true

				break
			}

			at += d
		}
	}

	return must
}

// ZoomChanges counts the joints of the cut where the framing changes and all its joints.
func (c *Cut) ZoomChanges() (changed, joints int) {
	zoomed := c.ZoomedSegments()

	for i := 1; i < len(zoomed); i++ {
		if zoomed[i] != zoomed[i-1] {
			changed++
		}
	}

	return changed, max(len(zoomed)-1, 0)
}

// Zooms counts the times the reel moves closer: runs of zoomed segments.
func (c *Cut) Zooms() int {
	count, previous := 0, false

	for _, zoomed := range c.ZoomedSegments() {
		if zoomed && !previous {
			count++
		}

		previous = zoomed
	}

	return count
}

// segmentLengths are the lengths of the segments in the reel: whole frames (as videoTrim cuts them) when the frame
// rate is known.
func segmentLengths(segments []Segment, frameRate Rational) []float64 {
	rate := frameRate.Float()
	lengths := make([]float64, 0, len(segments))

	for _, s := range segments {
		if rate <= 0 {
			lengths = append(lengths, s.End-s.Start)

			continue
		}

		frames := math.Round(s.End*rate) - math.Round(s.Start*rate)
		lengths = append(lengths, frames/rate)
	}

	return lengths
}

// zoomSpans are the [start, end) intervals of the reel, in seconds, shown closer: neighbouring zoomed segments are one.
func zoomSpans(c *Cut) [][2]float64 {
	var spans [][2]float64

	zoomed := c.ZoomedSegments()
	at := 0.0

	for i, d := range segmentLengths(c.Segments, c.Options.FrameRate) {
		if zoomed[i] {
			if i > 0 && zoomed[i-1] {
				spans[len(spans)-1][1] = at + d
			} else {
				spans = append(spans, [2]float64{at, at + d})
			}
		}

		at += d
	}

	return spans
}

// zoomGraph continues the chain of the fitted frame (width×height, after the tone mapping): split in two, the second
// copy cropped to the middle 1/Factor and scaled back to the frame, laid over the first during the zoomed spans. Only
// the 1080×1920 frame is cropped and scaled, never the 4K source: about 1 ms of a frame. Empty without zoomed segments.
// Frame times are k/rate: an edge is put half a frame before its frame, so rounding never moves it.
func zoomGraph(c *Cut, width, height int) (string, error) {
	z := c.Zoom
	if z.Factor <= 1 || z.Factor > maxZoomFactor || z.MinSegment < 0 || z.Every < 0 {
		return "", fmt.Errorf("factor %.3f, shortest segment %.2f s, every %d: %w", z.Factor, z.MinSegment, z.Every,
			ErrInvalidZoom)
	}

	spans := zoomSpans(c)
	if len(spans) == 0 {
		return "", nil
	}

	margin := zoomEpsilon
	if rate := c.Options.FrameRate.Float(); rate > 0 {
		margin = half / rate
	}

	terms := make([]string, 0, len(spans))
	for _, s := range spans {
		terms = append(terms, "gte(t,"+formatPrecise(s[0]-margin)+")*lt(t,"+formatPrecise(s[1]-margin)+")")
	}

	w, h, x, y := zoomWindow(width, height, z.Factor)
	size := strconv.Itoa(width) + ":" + strconv.Itoa(height)

	return ",format=" + videoPixFmt + ",split=2[" + zoomPlainLabel + "][" + zoomCloseLabel + "];" +
		"[" + zoomCloseLabel + "]crop=" + strconv.Itoa(w) + ":" + strconv.Itoa(h) + ":" + strconv.Itoa(x) + ":" +
		strconv.Itoa(y) + ",scale=" + size + "[" + zoomedLabel + "];" +
		"[" + zoomPlainLabel + "][" + zoomedLabel + "]overlay=x=0:y=0:enable='" + strings.Join(terms, "+") + "'", nil
}

// zoomWindow is the part of the width×height frame shown zoomed in: of its aspect (1080×1920 → 990×1760 at 1.09),
// with even sizes and position (4:2:0 chroma), its fixed point the middle across and zoomAnchorY down.
func zoomWindow(width, height int, factor float64) (w, h, x, y int) {
	w = 2 * int(math.Round(float64(width)/factor/2))
	h = 2 * int(math.Round(float64(w)*float64(height)/float64(width)/2))
	x = 2 * ((width - w) / 2 / 2)
	y = 2 * int(math.Round(float64(height-h)*zoomAnchorY/2))

	return w, h, x, y
}
