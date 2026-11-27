package subtitles

import (
	"fmt"
	"math"
	"strings"
	"unicode"
)

const (
	// hookRepeatWindow: a phrase that starts this long after the hook at the latest may repeat it.
	hookRepeatWindow = 1.0
	// stemTail: two words of at least minStemRunes letters are one when they differ only in the last stemTail letters
	// («страха» and «страхи», «тренировках» and «тренировки»).
	stemTail = 2
	// quietTolerance: a subtitle that starts this little before a heading is its section's (its lead-in).
	quietTolerance = readingWindow
)

// textWords are the words of a text as they are compared: lower case letters and digits, ё as е, the parts of a word
// with a hyphen apart («онлайн-тренировках» → онлайн, тренировках).
func textWords(text string) []string {
	parts := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })

	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if n := normalize(p); n != "" {
			out = append(out, n)
		}
	}

	return out
}

// sameWord: the words are equal, or long and differ only in the ending.
func sameWord(a, b string) bool {
	if a == b {
		return true
	}

	ra, rb := []rune(a), []rune(b)
	shorter := min(len(ra), len(rb))

	if shorter < minStemRunes {
		return false
	}

	common := 0
	for common < shorter && ra[common] == rb[common] {
		common++
	}

	return common >= max(minStemRunes, max(len(ra), len(rb))-stemTail)
}

// matchedWords counts the words of a that have a same word in b (each word of b is used once).
func matchedWords(a, b []string) int {
	used := make([]bool, len(b))
	n := 0

	for _, w := range a {
		for j, v := range b {
			if !used[j] && sameWord(w, v) {
				used[j], n = true, n+1

				break
			}
		}
	}

	return n
}

// hideHookRepeat drops the subtitles that repeat the hook (hook.hide_repeat): the trainer often says it right after it
// is shown. A subtitle repeats it when it starts within hookRepeatWindow after the hook's end and at least half of the
// hook's words and half of its own are the same words (endings aside). cues is not changed.
func hideHookRepeat(cues []cue, hook string, hookEnd float64) []cue {
	hookWords := textWords(hook)
	if len(hookWords) == 0 || hookEnd <= 0 {
		return cues
	}

	out := make([]cue, 0, len(cues))

	for _, c := range cues {
		words := textWords(cueString(c))
		matched := matchedWords(hookWords, words)

		if c.Start <= hookEnd+hookRepeatWindow && len(words) > 0 && 2*matched >= len(hookWords) &&
			2*matched >= len(words) {
			continue
		}

		out = append(out, c)
	}

	return out
}

// cueString is the text of a subtitle, its words joined by spaces.
func cueString(c cue) string {
	tokens := c.tokens()
	texts := make([]string, 0, len(tokens))

	for _, t := range tokens {
		texts = append(texts, t.Text)
	}

	return strings.Join(texts, " ")
}

// quietUnderSections keeps the subtitles off the screen for quiet seconds after a heading appears (section.quiet), as
// the trainer's heading stands alone before the question under it — whether or not the words that read it out were
// found (the editor may have cut them out of the sound, the heading then starts on the question): a subtitle that runs
// into the heading ends at its start, one that starts in the quiet is shown from its end. A subtitle left shorter than
// minNoteShown is not shown. cues is not changed.
func quietUnderSections(cues []cue, headings []noteBlock, quiet float64) []cue {
	if quiet <= 0 || len(headings) == 0 {
		return cues
	}

	out := cues

	for _, h := range headings {
		out = quietAfter(out, h.start, h.start+quiet)
	}

	return out
}

// quietAfter is quietUnderSections for the heading that starts at start, quiet until end.
func quietAfter(cues []cue, start, end float64) []cue {
	out := make([]cue, 0, len(cues))

	for _, c := range cues {
		switch {
		case c.Start < start-quietTolerance:
			c.End = min(c.End, max(start, c.Start))
		case c.Start < end:
			c.Start = end
		}

		if c.End-c.Start >= minNoteShown {
			out = append(out, c)
		}
	}

	return out
}

// fadeIns are the fade-ins of the subtitles, ms (layout.fade_in): a subtitle after a gap fades in, at most over half of
// its time; one that follows the previous without a gap does not (the text would blink).
func fadeIns(cues []cue, fadeIn int) []int {
	fades := make([]int, 0, len(cues))
	prevEnd := math.Inf(-1)

	for _, c := range cues {
		fade := 0
		if fadeIn > 0 && prevEnd < c.Start {
			fade = min(fadeIn, int((c.End-c.Start)*millisPerSecond)/2)
		}

		fades = append(fades, fade)
		prevEnd = c.End
	}

	return fades
}

// fadePrefix is the override block of a fade-in, "" for none.
func fadePrefix(ms int) string {
	if ms <= 0 {
		return ""
	}

	return fmt.Sprintf(`{\fad(%d,0)}`, ms)
}
