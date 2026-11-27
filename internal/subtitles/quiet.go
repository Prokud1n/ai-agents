package subtitles

import (
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
)

const (
	// hookRepeatWindow: a phrase that starts this long after the hook at the latest may repeat it.
	hookRepeatWindow = 1.0
	// stemTail: two words of at least minStemRunes letters are one when they differ only in the last stemTail letters
	// («страха» and «страхи», «тренировках» and «тренировки»).
	stemTail = 2
	// leadTolerance: a subtitle that starts this little before a heading is its section's (its lead-in).
	leadTolerance = readingWindow
	// jointWindow: a heading this little after a joint of the cut stands on the question that starts at the joint.
	jointWindow = readingWindow
	// lateWordReach: the first word of the question under such a heading starts by Whisper at most this long after the
	// joint (Whisper starts it up to 0.35 s after the voice, the cut puts the voice 0.04..0.12 s after the joint); a
	// subtitle that starts later is the speech after a reading of the heading, not the question at the joint.
	lateWordReach = 0.5
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

// contentWords are the words of a text that carry its sense: textWords without function words and stop words.
func contentWords(text string) []string {
	words := textWords(text)
	out := make([]string, 0, len(words))

	for _, w := range words {
		if !proclitics[w] && !enclitics[w] && !stopWords[w] {
			out = append(out, w)
		}
	}

	return out
}

// hideHookRepeat drops the subtitles that repeat the hook (hook.hide_repeat): the trainer often says it right after it
// is shown. A subtitle repeats it when it starts within hookRepeatWindow after the hook's end and at least two thirds of
// the hook's content words and half of its own are the same words (endings aside). cues is not changed.
func hideHookRepeat(cues []cue, hook string, hookEnd float64) []cue {
	hookWords := contentWords(hook)
	if len(hookWords) == 0 || hookEnd <= 0 {
		return cues
	}

	out := make([]cue, 0, len(cues))

	for _, c := range cues {
		words := contentWords(cueString(c))
		matched := matchedWords(hookWords, words)

		// Two thirds of the hook: the words of its topic («онлайн-тренировки») come in other phrases too.
		if c.Start <= hookEnd+hookRepeatWindow && len(words) > 0 && 3*matched >= 2*len(hookWords) &&
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

// leadUnderSections shows the question under a heading that has just appeared a moment after it (section.lead), so the
// heading is read first, without delaying the speech: a subtitle whose speech starts with the heading (from
// leadTolerance before it — the editor may have cut its reading out of the sound, the heading then starts on the
// question) is shown from lead after the heading at the earliest, up to the next subtitle at the latest (dropped only
// when the next one starts there too); a subtitle that runs into the heading from before ends at its start (one left
// shorter than minNoteShown is not shown). The editor puts the heading on the late start Whisper gives the first
// word, about 0.2 s after the joint where the question starts: when a joint of the cut (joints) is up to jointWindow
// before the heading, lead counts from the joint (but not before the heading), and the question whose first word
// Whisper starts later still (up to lateWordReach after the joint, T3.31) is shown from there too — the voice starts
// at the joint, so the question shows at most max(lead, jointWindow) after it. cues is not changed.
func leadUnderSections(cues []cue, headings []noteBlock, lead float64, joints []float64) []cue {
	out := cues

	for _, h := range headings {
		from, pullUntil := h.start, math.Inf(-1)

		if j, ok := jointBefore(joints, h.start); ok {
			from, pullUntil = j, j+lateWordReach
		}

		out = leadAfter(out, h.start, max(from+lead, h.start), pullUntil)
	}

	return out
}

// jointBefore is the last joint up to jointWindow before the time (or at it); false when there is none.
func jointBefore(joints []float64, at float64) (float64, bool) {
	found, ok := 0.0, false

	for _, j := range joints {
		if j <= at && at-j <= jointWindow {
			found, ok = j, true
		}
	}

	return found, ok
}

// cutJoints are the starts of the fragments of the cut on its timeline; nil without a cut.
func cutJoints(cut *ffmpeg.Cut) []float64 {
	if cut == nil {
		return nil
	}

	joints := make([]float64, 0, len(cut.Segments))
	at := 0.0

	for _, s := range cut.Segments {
		joints = append(joints, at)
		at += s.End - s.Start
	}

	return joints
}

// leadAfter is leadUnderSections for the heading that starts at start: its question is shown from shownFrom at the
// earliest; the first subtitle of the section that starts after shownFrom but not after pullUntil (the late start
// Whisper gave the word at a joint) is shown from shownFrom.
func leadAfter(cues []cue, start, shownFrom, pullUntil float64) []cue {
	out := make([]cue, 0, len(cues))
	first := true // the next subtitle of the section is its first one

	shown := func(c cue) float64 { // where a subtitle of the section starts on the screen
		return max(c.Start, shownFrom)
	}

	for i, c := range cues {
		if c.Start < start-leadTolerance {
			c.End = min(c.End, max(start, c.Start))
			if c.End-c.Start >= minNoteShown {
				out = append(out, c)
			}

			continue
		}

		if first && c.Start > shownFrom && c.Start <= pullUntil {
			c.Start = shownFrom
		}

		first = false

		if c.Start < shownFrom {
			c.Start = shownFrom
			c.End = max(c.End, c.Start+minNoteShown)

			// Not over the next subtitle: one shifted up to it is a scrap.
			if i+1 < len(cues) {
				next := shown(cues[i+1])
				if c.Start >= next {
					continue
				}

				c.End = min(c.End, next)
			}
		}

		out = append(out, c)
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
