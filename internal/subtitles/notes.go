package subtitles

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
)

// Times of the screen notes, in seconds.
const (
	// calloutDuration of a callout without an end; a callout with one is shown minCallout..maxCallout.
	calloutDuration = 2.5
	minCallout      = 1.5
	maxCallout      = 3.0
	// listTail: the last element of a list without an end stays this long, unless another note comes first.
	listTail = 5.0
	// minNoteShown: a note left shorter than this (the hook took its time, the next note came) is not drawn.
	minNoteShown = 0.5
	// noteJoin: notes closer than this follow each other without fading out and in.
	noteJoin = 0.02
)

// newListPrefixes start the first item of a list written without a title: it starts a new list.
var newListPrefixes = []string{"1/", "1.", "1)"}

// noteBlock is what the notes show from start to end: a list (its title and items, the latest that fit) or a callout,
// lines lines in all.
type noteBlock struct {
	start, end      float64
	entries         []string
	lines           int
	fadeIn, fadeOut bool
}

// noteBlocks lays the notes out in time at the hook's place. A note waits for the hook to go. A list title starts a
// list (so does an item numbered first after other items, «1/ …») and its items pile up under it, line by line, until the list ends: at the end of its last element, at the next
// note that is not its item, or listTail after its last element; when the lines do not fit (hookLines), the earliest
// items scroll away (the title goes last). A callout is shown alone (calloutDuration, or its end within
// minCallout..maxCallout) and the list comes back with its next item. A note never runs past the reel (reelEnd 0:
// unknown) or into the next one.
func noteBlocks(notes []Note, hookEnd, reelEnd float64, style *Style) []noteBlock {
	sorted := cleanNotes(notes, style)
	blocks := make([]noteBlock, 0, len(sorted))

	var (
		list   []string
		titled bool
	)

	for i, n := range sorted {
		start := max(n.Start, hookEnd)

		end := noteEnd(sorted, i, start, hookEnd)
		if reelEnd > 0 {
			end = min(end, reelEnd)
		}

		entries := []string{n.Text}

		switch {
		case n.Kind == ListTitleNote:
			list, titled = []string{n.Text}, true
			entries = list
		case n.Kind == ListItemNote && (len(list) == 0 || (startsNewList(n.Text) && (!titled || len(list) > 1))):
			list, titled = []string{n.Text}, false
			entries = list
		case n.Kind == ListItemNote:
			list = append(slices.Clone(list), n.Text)
			entries = list
		}

		if end-start < minNoteShown {
			continue
		}

		shown, lines := scrollEntries(entries, titled && n.Kind != CalloutNote, style)
		blocks = append(blocks, noteBlock{start: roundCS(start), end: roundCS(end), entries: shown, lines: lines})
	}

	return withFades(blocks)
}

// cleanNotes are the notes with a text to show (as the hook cleans it), ordered by time; an unknown kind is a callout.
func cleanNotes(notes []Note, style *Style) []Note {
	out := make([]Note, 0, len(notes))

	for _, n := range notes {
		words := hookWords(n.Text, style)
		if len(words) == 0 {
			continue
		}

		clean := n
		clean.Text = strings.Join(words, " ")

		if clean.Kind != ListTitleNote && clean.Kind != ListItemNote {
			clean.Kind = CalloutNote
		}

		out = append(out, clean)
	}

	slices.SortStableFunc(out, func(a, b Note) int { return cmp.Compare(a.Start, b.Start) })

	return out
}

// startsNewList: an item numbered first («1/ …») starts a new list.
func startsNewList(text string) bool {
	for _, prefix := range newListPrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}

	return false
}

// noteEnd is when the note i, shown from start, ends: a callout after its time, a note with an end at it, an element
// of a list before an item of it at that item, the last element of a list listTail later; never after the next note.
func noteEnd(notes []Note, i int, start, hookEnd float64) float64 {
	n := notes[i]

	next := math.Inf(1)
	if i+1 < len(notes) {
		next = max(notes[i+1].Start, hookEnd)
	}

	var end float64

	switch {
	case n.Kind == CalloutNote && n.End > n.Start:
		end = start + min(max(n.End-n.Start, minCallout), maxCallout)
	case n.Kind == CalloutNote:
		end = start + calloutDuration
	case n.End > n.Start:
		end = n.End
	case i+1 < len(notes) && notes[i+1].Kind == ListItemNote:
		end = next
	default:
		end = start + listTail
	}

	return min(end, next)
}

// scrollEntries keeps what fits hookLines lines: the earliest items scroll away first, the title (titled) last, the
// latest item always stays. It returns the entries shown and their lines.
func scrollEntries(entries []string, titled bool, style *Style) ([]string, int) {
	title, items := "", entries

	if titled && len(entries) > 1 {
		title, items = entries[0], entries[1:]
	}

	lines := func(texts []string) int {
		n := 0
		for _, e := range texts {
			n += noteLines(e, style)
		}

		return n
	}

	total := lines(items) + noteLines(title, style)

	for total > hookLines && len(items) > 1 {
		total -= noteLines(items[0], style)
		items = items[1:]
	}

	if total > hookLines && title != "" {
		total -= noteLines(title, style)
		title = ""
	}

	if title == "" {
		return slices.Clone(items), min(total, hookLines)
	}

	return append([]string{title}, items...), total
}

// noteLines is how many lines the text takes in the Hook style; 0 for none.
func noteLines(text string, style *Style) int {
	words := hookWords(text, style)
	if len(words) == 0 {
		return 0
	}

	return len(splitHook(words, style.hookLineChars()))
}

// fades are the fade in and out of the block, ms: fade where it fades, 0 where it does not.
func (bl *noteBlock) fades(fade int) (fadeIn, fadeOut int) {
	if bl.fadeIn {
		fadeIn = fade
	}

	if bl.fadeOut {
		fadeOut = fade
	}

	return fadeIn, fadeOut
}

// withFades fades a block in unless it follows another one without a gap, and out unless another one follows it.
func withFades(blocks []noteBlock) []noteBlock {
	out := make([]noteBlock, 0, len(blocks))

	for i, bl := range blocks {
		bl.fadeIn = i == 0 || blocks[i-1].end < bl.start-noteJoin
		bl.fadeOut = i+1 == len(blocks) || blocks[i+1].start > bl.end+noteJoin
		out = append(out, bl)
	}

	return out
}

// noteEvents are the dialogue lines of the note blocks: the Hook style at its place, each entry wrapped by words into
// lines squeezed to fit like the hook. The text is plain: it is escaped like speech.
func noteEvents(blocks []noteBlock, style *Style) string {
	var b strings.Builder

	maxChars := style.hookLineChars()

	for _, bl := range blocks {
		fadeIn, fadeOut := bl.fades(min(style.Hook.Fade, int((bl.end-bl.start)*millisPerSecond)/2))

		var text strings.Builder

		if tag := fadTag(fadeIn, fadeOut); tag != "" {
			text.WriteString("{" + tag + "}")
		}

		prevScale, first := fullScaleX, true

		for _, entry := range bl.entries {
			words := hookWords(entry, style)

			for _, line := range splitHook(words, maxChars) {
				if !first {
					text.WriteString(assLineBreak)
				}

				first = false

				if scale := charsScale(lineRunes(words[line[0]:line[1]]), maxChars); scale != prevScale {
					fmt.Fprintf(&text, `{\fscx%d}`, scale)
					prevScale = scale
				}

				text.WriteString(escapeText(strings.Join(words[line[0]:line[1]], " ")))
			}
		}

		fmt.Fprintf(&b, "Dialogue: %d,%s,%s,%s,,0,0,0,,%s\n", hookLayer, formatTime(bl.start), formatTime(bl.end),
			hookStyleName, text.String())
	}

	return b.String()
}

// noteMargin is the margin of a subtitle of the phrase layout shown from start to end: under the tallest note block
// it meets (as under the hook), 0 when it meets none.
func noteMargin(start, end float64, blocks []noteBlock, style *Style) int {
	margin := 0

	for _, bl := range blocks {
		if start < bl.end && bl.start < end {
			margin = max(margin, style.hookSubtitlesMargin(bl.lines))
		}
	}

	return margin
}

// shadeSpan is a time the hook or a note block stands at the subtitles' edge with the phrase subtitles under it at
// margin.
type shadeSpan struct {
	start, end      float64
	margin          int
	fadeIn, fadeOut bool
}

// shadeEvents extend the vignette under the hook, the note blocks and the subtitles shown with them (the phrase
// layout at the top or the bottom with the vignette on): a drawing per span, until its end or the end of the last
// subtitle under it (margins are the subtitles' margins), but not into the next span — a span that runs into the next
// one joins it without fading out and in, so the frame does not flicker. They are the first events, on layer 0, under
// every text.
func shadeEvents(cues []cue, margins []int, style *Style, hook hookLayout) string {
	if !style.Vignette.Enabled || style.Layout.Position == MiddlePosition || style.wordMode() {
		return ""
	}

	spans := extendSpans(shadeSpans(style, hook), cues, margins)

	var b strings.Builder

	joinedPrev := false

	for i, sp := range spans {
		joinedNext := i+1 < len(spans) && sp.end >= spans[i+1].start-noteJoin
		if i+1 < len(spans) {
			sp.end = min(sp.end, spans[i+1].start)
		}

		sp.fadeIn, sp.fadeOut = sp.fadeIn && !joinedPrev, sp.fadeOut && !joinedNext
		b.WriteString(sp.drawing(style))
		joinedPrev = joinedNext
	}

	return b.String()
}

// shadeSpans are the hook and the note blocks that move the phrase subtitles, in time.
func shadeSpans(style *Style, hook hookLayout) []shadeSpan {
	spans := make([]shadeSpan, 0, len(hook.notes)+1)
	if hook.end > 0 && hook.margin > 0 {
		spans = append(spans, shadeSpan{start: 0, end: hook.end, margin: hook.margin, fadeIn: true, fadeOut: true})
	}

	for _, bl := range hook.notes {
		if margin := style.hookSubtitlesMargin(bl.lines); margin > 0 {
			spans = append(spans, shadeSpan{
				start: bl.start, end: bl.end, margin: margin, fadeIn: bl.fadeIn, fadeOut: bl.fadeOut,
			})
		}
	}

	return spans
}

// extendSpans make every span last until the end of the last subtitle it meets and reach down to the lowest of them.
func extendSpans(spans []shadeSpan, cues []cue, margins []int) []shadeSpan {
	extended := make([]shadeSpan, 0, len(spans))

	for _, sp := range spans {
		for i, c := range cues {
			if c.Start < sp.end && sp.start < c.End {
				sp.end, sp.margin = max(sp.end, c.End), max(sp.margin, margins[i])
			}
		}

		extended = append(extended, sp)
	}

	return extended
}

// drawing is the shade of the span, fading where it fades.
func (sp *shadeSpan) drawing(style *Style) string {
	fade := min(style.Hook.Fade, int((sp.end-sp.start)*millisPerSecond)/2)
	fades := [2]int{}

	if sp.fadeIn {
		fades[0] = fade
	}

	if sp.fadeOut {
		fades[1] = fade
	}

	return shadeDrawing(sp.start, sp.end, fades, sp.margin, style)
}

// cueMargin is the margin of a phrase subtitle: under the hook when it starts while the hook is shown, under the
// tallest note block it meets; 0 (the style's) otherwise.
func cueMargin(c cue, hook hookLayout, style *Style) int {
	margin := noteMargin(c.Start, c.End, hook.notes, style)
	if c.Start < hook.end {
		margin = max(margin, hook.margin)
	}

	return margin
}

// fadTag is \fad of the fades, ms; "" without them.
func fadTag(fadeIn, fadeOut int) string {
	if fadeIn == 0 && fadeOut == 0 {
		return ""
	}

	return fmt.Sprintf(`\fad(%d,%d)`, fadeIn, fadeOut)
}
