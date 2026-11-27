package subtitles

import (
	"fmt"
	"math"
	"strings"
)

// The select animation of the hook (hook.animation select), as in the trainer's reel: the hook appears small at the
// right in a thin white frame with square handles at its corners and an arrow cursor at its bottom right corner, as an
// object selected in an editor, stands so for selectHold, then grows to its place and size over selectGrow, slowing
// down at the end, its right edge held where it is (a hook narrower than the screen moves its right edge in from the
// right); selectFrameAfter later the frame fades out, the cursor at selectCursorAfter (about 1.7 s); the hook stays.
const (
	// selectStartScale: the small hook is this share of its size.
	selectStartScale = 0.35
	// selectDrop: the small hook stands this much lower than the hook in its place, px.
	selectDrop = 30.0
	selectHold = 950 // ms
	selectGrow = 280 // ms
	// selectAccel is the \t acceleration of the growth: below 1 it slows down to the end.
	selectAccel       = 0.5
	selectFrameAfter  = 180 // ms
	selectCursorAfter = 440 // ms
	// selectFrameFade: the frame and the cursor fade out over it, ms.
	selectFrameFade = 120
	// selectRight: the small frame's right edge stands at least this far right: this far from the right edge of the
	// frame, px.
	selectRight = 90
	// selectPad is the frame around the text, selectStroke its width, selectHandleSize the side of a handle, px (of
	// the full size: the small frame is thinner).
	selectPad        = 16.0
	selectStroke     = 2.5
	selectHandleSize = 12.0
	// cursorOffset: the tip of the cursor stands this far right and down of the frame's corner, px.
	cursorOffset = 3.0
	// cursorBorder is the dark outline of the white cursor, px.
	cursorBorder = 2
	// cursorPath is the arrow cursor, its tip at (0, 0), 26×40 px.
	cursorPath = "m 0 0 l 0 34 l 9 26 l 15 40 l 21 37 l 15 24 l 26 24"
	// assWhite is opaque white for the \1c override.
	assWhite = "&HFFFFFF&"
	// selectFitScale: the hook takes the fewest lines that keep it at least this large (percent), unless hook.fit_scale
	// says otherwise; trainerHookFitScale is the trainer's: two lines made smaller rather than three (T3.23).
	selectFitScale      = 93
	trainerHookFitScale = 75
)

// hookFitScale is the smallest scale of the hook that does not take one more line (hook.fit_scale).
func (s *Style) hookFitScale() int {
	if s.Hook.FitScale > 0 {
		return s.Hook.FitScale
	}

	return selectFitScale
}

// selectLines wraps the hook of the select animation: the fewest lines (up to the hook's max_lines) that keep it at
// least selectFitScale of its size — without a short word at the end of a line when that is as good, otherwise as the
// trainer does («РАЗВЕИВАЮ СТРАХИ ОБ» over «ОНЛАЙН-ТРЕНИРОВКАХ») — or, when none does, the larger of the two wraps of
// the fewest lines that keep it at least hook.fit_scale; none such: the largest of them.
func selectLines(words []string, style *Style) [][2]int {
	var best [][2]int

	bestScale := 0

	for n := 1; n <= style.hookMaxLines(); n++ {
		lineBest, lineScale := [][2]int(nil), 0

		for _, strict := range []bool{true, false} {
			lines := evenSplit(words, n, strict)
			scale := selectionOf(words, lines, style).scale

			if scale >= selectFitScale {
				return lines
			}

			if scale > lineScale {
				lineBest, lineScale = lines, scale
			}
		}

		// Fewer lines made smaller (hook.fit_scale) rather than one more line.
		if lineScale >= style.hookFitScale() {
			return lineBest
		}

		if lineScale > bestScale {
			best, bestScale = lineBest, lineScale
		}
	}

	return best
}

// selection is the geometry of the select animation: the scale of the hook (percent of its size: a long hook is made
// smaller as a whole to fit its lines), the size of the frame at that scale and the centers of the hook small at the
// start and in its place at the end.
type selection struct {
	scale         int
	frame         point
	start, center point
}

// selectionOf lays the hook of the lines out: the widest line (hookTextWidth) sets the scale against the width
// between the hook's side margins (no smaller than minHookShrinkScale) and the frame's width; libass makes a line as
// tall as the font size.
func selectionOf(words []string, lines [][2]int, style *Style) selection {
	widest := 1.0
	for _, line := range lines {
		widest = max(widest, style.hookTextWidth(strings.Join(words[line[0]:line[1]], " ")))
	}

	room := float64(FrameWidth - 2*style.hookMarginSide())
	scale := max(min(int(math.Floor(percentBase*room/widest)), fullScaleX), minHookShrinkScale)
	share := float64(scale) / percentBase
	height := float64(len(lines)*style.Hook.Size) * share
	frame := point{x: widest*share + 2*selectPad, y: height + 2*selectPad}

	center := point{x: FrameWidth / 2, y: float64(style.hookMargin()) + height/2}
	if style.hookPosition() == BottomPosition {
		center.y = float64(FrameHeight-style.hookMargin()) - height/2
	}

	// The right edge of the small frame is the hook's own in its place, so it grows to the left, or further right.
	right := max(center.x+frame.x/2, FrameWidth-selectRight)
	start := point{x: right - selectStartScale*frame.x/2, y: center.y + selectDrop}

	return selection{scale: scale, frame: frame, start: start, center: center}
}

// selectTimes are the moments of the animation within the hook's time, ms: the growth starts and ends, the frame and
// the cursor go, the hook's fade and its end.
type selectTimes struct {
	grow                [2]int
	frameEnd, cursorEnd int
	fade, total         int
}

func selectTimesOf(end float64, style *Style) selectTimes {
	total := int(math.Round(end * millisPerSecond))

	return selectTimes{
		grow:      [2]int{min(selectHold, total), min(selectHold+selectGrow, total)},
		frameEnd:  min(selectHold+selectGrow+selectFrameAfter, total),
		cursorEnd: min(selectHold+selectGrow+selectCursorAfter, total),
		fade:      min(style.Hook.Fade, total/2),
		total:     total,
	}
}

// selectEvents are the hook of the select animation until end: the text, the frame with its handles and the cursor at
// its corner, small at the right while it stands, then moving and growing together to the hook's place (the move and
// the scale slow down alike, so the frame never leaves the screen; animSteps pieces of straight moves and linear \t),
// then the frame and the cursor fade out and the text stays. The text is plain: it is escaped like speech.
func selectEvents(words []string, end float64, style *Style) string {
	lines := selectLines(words, style)
	sel := selectionOf(words, lines, style)
	tm := selectTimesOf(end, style)

	var text strings.Builder

	key := hookKey(words)
	for i, line := range lines {
		if i > 0 {
			text.WriteString(assLineBreak)
		}

		writeHookWords(&text, words, line, key, &style.Hook)
	}

	scaled := func(full float64) func(from, to animState, dur int) string {
		return func(from, to animState, dur int) string {
			tags := `\an5` + placeTags(from.center, to.center, dur) + scaleTags(from.share*full)
			if to.share != from.share {
				tags += fmt.Sprintf(`\t(0,%d,%s)`, dur, scaleTags(to.share*full))
			}

			return tags
		}
	}

	cursor := func(from, to animState, dur int) string {
		return `\an7` + placeTags(sel.corner(from), sel.corner(to), dur)
	}

	frameLook := `\bord0\shad0\1c` + assWhite + `\1a&H00&`
	cursorLook := fmt.Sprintf(`\bord%d\shad0\1c%s\3c%s\1a&H00&\3a&H00&`, cursorBorder, assWhite,
		assOverrideColor("#000000"))

	var b strings.Builder

	writeAnimated(&b, &sel, &tm, animPart{end: tm.total, fadeOut: tm.fade, place: scaled(float64(sel.scale)),
		body: "}" + text.String()})
	writeAnimated(&b, &sel, &tm, animPart{end: tm.frameEnd, fadeOut: selectFrameFade, place: scaled(percentBase),
		look: frameLook, body: `\p1}` + framePath(sel.frame) + `{\p0}`})
	writeAnimated(&b, &sel, &tm, animPart{end: tm.cursorEnd, fadeOut: selectFrameFade, place: cursor, look: cursorLook,
		body: `\p1}` + cursorPath + `{\p0}`})

	return b.String()
}

// animSteps: the growth is drawn by this many pieces of straight moves and linear scaling.
const animSteps = 7

// animState is where the hook stands at a moment of the animation: its middle and its share of the full size.
type animState struct {
	center point
	share  float64
}

// stateAt is the state at the moment, ms: small at the start until the growth, then the middle and the size moving
// together, slowing down (the progress to the power of selectAccel), full after it.
func (sel *selection) stateAt(tm *selectTimes, ms int) animState {
	progress := 0.0
	if span := tm.grow[1] - tm.grow[0]; span > 0 {
		progress = math.Pow(min(max(float64(ms-tm.grow[0])/float64(span), 0), 1), selectAccel)
	}

	return animState{
		center: point{
			x: sel.start.x + (sel.center.x-sel.start.x)*progress, y: sel.start.y + (sel.center.y-sel.start.y)*progress,
		},
		share: selectStartScale + (1-selectStartScale)*progress,
	}
}

// corner is where the tip of the cursor stands in the state: at the bottom right corner of the frame.
func (sel *selection) corner(st animState) point {
	return point{
		x: st.center.x + st.share*sel.frame.x/2 + cursorOffset, y: st.center.y + st.share*sel.frame.y/2 + cursorOffset,
	}
}

// animPart is an element of the animation shown until end (ms), fading out over fadeOut: place gives its position and
// scale tags from one state to another over dur ms, look and body follow them («}» closes the tags before the text).
type animPart struct {
	end, fadeOut int
	place        func(from, to animState, dur int) string
	look, body   string
}

// writeAnimated writes the element as events: still before the growth (fading in), the pieces of the growth, still
// after it until its end (fading out); events past its end are cut there.
func writeAnimated(b *strings.Builder, sel *selection, tm *selectTimes, part animPart) {
	cuts := []int{0, tm.grow[0]}
	for i := 1; i <= animSteps; i++ {
		cuts = append(cuts, tm.grow[0]+(tm.grow[1]-tm.grow[0])*i/animSteps)
	}

	cuts = append(cuts, part.end)

	for i := 0; i+1 < len(cuts); i++ {
		from, to := cuts[i], min(cuts[i+1], part.end)
		if to <= from {
			continue
		}

		fadeIn, fadeOut := 0, 0
		if from == 0 {
			fadeIn = tm.fade
		}

		if to == part.end {
			fadeOut = part.fadeOut
		}

		tags := part.place(sel.stateAt(tm, from), sel.stateAt(tm, to), to-from) + part.look + fadTag(fadeIn, fadeOut)
		writeHookDialogue(b, from, to, "{"+tags+part.body)

		if to == part.end {
			return
		}
	}
}

// placeTags put an element at the point, or move it from one point to another over dur ms.
func placeTags(from, to point, dur int) string {
	if from == to {
		return `\pos(` + tenth(from.x) + "," + tenth(from.y) + ")"
	}

	return fmt.Sprintf(`\move(%s,%s,%s,%s,0,%d)`, tenth(from.x), tenth(from.y), tenth(to.x), tenth(to.y), dur)
}

// scaleTags is \fscx and \fscy of the scale, percent.
func scaleTags(scale float64) string {
	return `\fscx` + tenth(scale) + `\fscy` + tenth(scale)
}

// writeHookDialogue writes an event of the hook from start to end, ms.
func writeHookDialogue(b *strings.Builder, start, end int, text string) {
	fmt.Fprintf(b, "Dialogue: %d,%s,%s,%s,,0,0,0,,%s\n", hookLayer, formatTime(float64(start)/millisPerSecond),
		formatTime(float64(end)/millisPerSecond), hookStyleName, text)
}

// framePath is the selection frame of the size as an ASS drawing: the ring of the rectangle (an outer contour and an
// inner one drawn the other way, so the fill leaves a hole) and a square handle centered on every corner. The drawing
// starts at (0, 0), so aligned by its middle it stands centered on the hook.
func framePath(size point) string {
	o := selectHandleSize / 2
	parts := []string{
		contour(rectPath(point{x: o, y: o}, point{x: o + size.x, y: o + size.y}), false),
		contour(rectPath(point{x: o + selectStroke, y: o + selectStroke},
			point{x: o + size.x - selectStroke, y: o + size.y - selectStroke}), true),
	}

	for _, corner := range []point{{o, o}, {o + size.x, o}, {o + size.x, o + size.y}, {o, o + size.y}} {
		parts = append(parts, contour(rectPath(point{x: corner.x - o, y: corner.y - o},
			point{x: corner.x + o, y: corner.y + o}), false))
	}

	return strings.Join(parts, " ")
}

// rectPath is the rectangle from the top left corner to the bottom right one, clockwise, back to its start.
func rectPath(from, to point) closedPath {
	return closedPath{start: from, segments: []segment{
		{to: point{x: to.x, y: from.y}}, {to: to}, {to: point{x: from.x, y: to.y}}, {to: from},
	}}
}
