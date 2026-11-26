package subtitles

import (
	"fmt"
	"strings"
)

// The select animation of the hook (hook.animation select), as in the trainer's reel: the hook appears small at the
// right in a thin white frame with square handles at its corners and an arrow cursor at its bottom right corner, as an
// object selected in an editor; after selectHold it grows to its place and size over selectGrow; selectAfter later the
// frame and the cursor fade out, the hook stays.
const (
	// selectStartScale: the small hook is this share of its size.
	selectStartScale = 0.4
	selectHold       = 250 // ms
	selectGrow       = 500 // ms
	selectAfter      = 300 // ms
	// selectFrameFade: the frame and the cursor fade out over it, ms.
	selectFrameFade = 120
	// selectRight: the small frame's right edge stands this far from the right edge of the frame, px.
	selectRight = 90
	// selectPad is the frame around the text, selectStroke its width, selectHandleSize the side of a handle, px (of
	// the full size: the small frame is thinner).
	selectPad        = 14.0
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
)

// selectFitScale: the hook of the select animation takes the fewest lines that keep it at least this large (percent).
const selectFitScale = 93

// selectLines wraps the hook of the select animation: the fewest lines (up to the hook's max_lines) that keep it at
// least selectFitScale of its size — without a short word at the end of a line when that is as good, otherwise as the
// trainer does («РАЗВЕИВАЮ СТРАХИ ОБ» over «ОНЛАЙН-ТРЕНИРОВКАХ»); none such: the largest of them.
func selectLines(words []string, style *Style) [][2]int {
	var best [][2]int

	bestScale := 0

	for n := 1; n <= style.hookMaxLines(); n++ {
		for _, strict := range []bool{true, false} {
			lines := evenSplit(words, n, strict)
			scale := selectionOf(words, lines, style).scale

			if scale >= selectFitScale {
				return lines
			}

			if scale > bestScale {
				best, bestScale = lines, scale
			}
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

// selectionOf lays the hook of the lines out: the widest line sets the scale (no smaller than minHookShrinkScale) and,
// with the hook's character width, the frame's width; libass makes a line as tall as the font size.
func selectionOf(words []string, lines [][2]int, style *Style) selection {
	h := &style.Hook
	maxChars := style.hookLineChars()

	widest := 1
	for _, line := range lines {
		widest = max(widest, lineRunes(words[line[0]:line[1]]))
	}

	scale := max(percentBase*min(maxChars, widest)/widest, minHookShrinkScale)
	share := float64(scale) / percentBase
	width := float64(widest) * float64(h.Size) * h.CharWidth * share
	height := float64(len(lines)*h.Size) * share
	frame := point{x: width + 2*selectPad, y: height + 2*selectPad}

	center := point{x: FrameWidth / 2, y: float64(style.hookMargin()) + height/2}
	if style.hookPosition() == BottomPosition {
		center.y = float64(FrameHeight-style.hookMargin()) - height/2
	}

	start := point{x: FrameWidth - selectRight - selectStartScale*frame.x/2, y: center.y}

	return selection{scale: scale, frame: frame, start: start, center: center}
}

// selectEvents are the hook of the select animation until end: the text, the frame with its handles and the cursor,
// all moving and growing together (\move, \t of \fscx and \fscy, aligned by their middle), then the frame and the
// cursor fade out and the text stays. The text is plain: it is escaped like speech.
func selectEvents(words []string, end float64, style *Style) string {
	h := &style.Hook
	lines := selectLines(words, style)
	sel := selectionOf(words, lines, style)

	endMs := int(end * millisPerSecond)
	grown := [2]int{min(selectHold, endMs), min(selectHold+selectGrow, endMs)}
	fade := min(h.Fade, endMs/2)
	frameEnd := min(end, float64(selectHold+selectGrow+selectAfter)/millisPerSecond)
	move := fmt.Sprintf(`\move(%s,%s,%s,%s,%d,%d)`, tenth(sel.start.x), tenth(sel.start.y), tenth(sel.center.x),
		tenth(sel.center.y), grown[0], grown[1])
	small := tenth(float64(sel.scale) * selectStartScale)

	var text strings.Builder

	fmt.Fprintf(&text, `{\an5%s\fscx%s\fscy%s\t(%d,%d,\fscx%d\fscy%d)\fad(%d,%d)}`, move, small, small, grown[0],
		grown[1], sel.scale, sel.scale, fade, fade)

	key := hookKey(words)
	for i, line := range lines {
		if i > 0 {
			text.WriteString(assLineBreak)
		}

		writeHookWords(&text, words, line, key, h)
	}

	frameScale := tenth(selectStartScale * percentBase)
	frame := fmt.Sprintf(`{\an5%s\fscx%s\fscy%s\t(%d,%d,\fscx%d\fscy%d)\bord0\shad0\1c%s\1a&H00&\fad(%d,%d)\p1}%s{\p0}`,
		move, frameScale, frameScale, grown[0], grown[1], percentBase, percentBase, assWhite, fade, selectFrameFade,
		framePath(sel.frame))

	tipStart := point{
		x: sel.start.x + selectStartScale*sel.frame.x/2 + cursorOffset,
		y: sel.start.y + selectStartScale*sel.frame.y/2 + cursorOffset,
	}
	tipEnd := point{x: sel.center.x + sel.frame.x/2 + cursorOffset, y: sel.center.y + sel.frame.y/2 + cursorOffset}
	cursor := fmt.Sprintf(`{\an7\move(%s,%s,%s,%s,%d,%d)\bord%d\shad0\1c%s\3c%s\1a&H00&\3a&H00&\fad(%d,%d)\p1}%s{\p0}`,
		tenth(tipStart.x), tenth(tipStart.y), tenth(tipEnd.x), tenth(tipEnd.y), grown[0], grown[1], cursorBorder,
		assWhite, assOverrideColor("#000000"), fade, selectFrameFade, cursorPath)

	var b strings.Builder

	for _, e := range []struct {
		end  float64
		text string
	}{{end, text.String()}, {frameEnd, frame}, {frameEnd, cursor}} {
		fmt.Fprintf(&b, "Dialogue: %d,%s,%s,%s,,0,0,0,,%s\n", hookLayer, formatTime(0), formatTime(e.end), hookStyleName,
			e.text)
	}

	return b.String()
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
