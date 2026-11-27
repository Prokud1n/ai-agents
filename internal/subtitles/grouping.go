package subtitles

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// Grouping limits, in seconds. A pause this long always starts a new subtitle (T3.2 leaves pauses up to 0.4 s).
const (
	pauseBreak     = 0.3
	maxCueDuration = 3.5
)

// unit is words that must stay on one line: a preposition with the next word, a number with its unit,
// a particle with the previous word.
type unit struct {
	tokens []token
}

func (u unit) chars() int {
	n := len(u.tokens) - 1 // spaces

	for _, t := range u.tokens {
		n += utf8.RuneCountInString(t.Text)
	}

	return n
}

func (u unit) first() token { return u.tokens[0] }

func (u unit) last() token { return u.tokens[len(u.tokens)-1] }

// cue is one subtitle: lines of tokens shown from Start to End.
type cue struct {
	Start float64
	End   float64
	Lines [][]token
}

func (c cue) tokens() []token {
	var all []token
	for _, line := range c.Lines {
		all = append(all, line...)
	}

	return all
}

// gapBreaks: a pause between words breaks the text like the end of a sentence.
func gapBreaks(prev, next token) bool {
	return next.Start-prev.End >= pauseBreak
}

// buildUnits glues words that must not be separated by a line or subtitle break. Nothing is glued across
// punctuation or a pause, and a unit longer than a line is split back into words.
func buildUnits(tokens []token, maxLineChars int) []unit {
	var (
		units   []unit
		current []token
	)

	for _, t := range tokens {
		if len(current) > 0 && !sticks(current[len(current)-1], t) {
			units = append(units, unit{tokens: current})
			current = nil
		}

		current = append(current, t)
	}

	if len(current) > 0 {
		units = append(units, unit{tokens: current})
	}

	out := make([]unit, 0, len(units))

	for _, u := range units {
		if u.chars() <= maxLineChars || len(u.tokens) == 1 {
			out = append(out, u)

			continue
		}

		// The words are no longer together, so "number with its unit" no longer holds for them.
		for _, t := range u.tokens {
			if t.Accent == numberAccent {
				t.Accent = noAccent
			}

			out = append(out, unit{tokens: []token{t}})
		}
	}

	return out
}

// sticks: a preposition goes with the next word, a number with the word after it unless that is another number
// (counting "раз два три" is not glued; "двадцать пять" is one number), a particle with the previous word.
func sticks(prev, next token) bool {
	if prev.Break != noBreak || gapBreaks(prev, next) {
		return false
	}

	if isNumber(prev.Norm) {
		return !isNumber(next.Norm) || isCompoundNumber(prev.Norm, next.Norm)
	}

	return isProclitic(prev.Norm) || isEnclitic(next.Norm)
}

// layoutLimits are the size limits of one subtitle.
type layoutLimits struct {
	maxLines     int
	maxLineChars int
}

// groupCues splits units into subtitles: always after a sentence end, a segment end or a pause; after a comma
// when the subtitle already has half a line and the rest of the phrase is not a short tail; and when the next
// unit does not fit the lines or the duration. A short tail left after all is glued to the subtitle before it.
func groupCues(units []unit, limits layoutLimits) [][]unit {
	var (
		cues    [][]unit
		current []unit
	)

	for i, u := range units {
		if len(current) > 0 && shouldBreak(current, units[i:], limits) {
			cues = append(cues, current)
			current = nil
		}

		current = append(current, u)
	}

	if len(current) > 0 {
		cues = append(cues, current)
	}

	return mergeHeads(mergeTails(cues, limits), limits)
}

// shouldBreak decides whether rest[0] starts a new subtitle after current.
func shouldBreak(current, rest []unit, limits layoutLimits) bool {
	prev := current[len(current)-1].last()
	next := rest[0]

	switch {
	case endsPhrase(prev, next.first()):
		return true
	case prev.Break == softBreak && unitsChars(current) >= limits.maxLineChars/2 && !isShortTail(rest, limits):
		return true
	case next.last().End-current[0].first().Start > maxCueDuration:
		return true
	}

	candidate := slices.Concat(current, []unit{next})

	return linesNeeded(candidate, limits.maxLineChars) > limits.maxLines
}

// endsPhrase: a sentence or segment end, or a pause, between two words.
func endsPhrase(prev, next token) bool {
	return prev.Break == strongBreak || gapBreaks(prev, next)
}

// isShortTail: the words up to the end of the phrase are said in less than minCueDuration or fit in a line,
// so they are not worth a subtitle of their own.
func isShortTail(rest []unit, limits layoutLimits) bool {
	end := len(rest)

	for i := range rest {
		if i+1 < len(rest) && endsPhrase(rest[i].last(), rest[i+1].first()) {
			end = i + 1

			break
		}
	}

	tail := rest[:end]

	return tail[len(tail)-1].last().End-tail[0].first().Start < minCueDuration ||
		unitsChars(tail) <= limits.maxLineChars
}

// mergeHeads glues a subtitle shorter than minCueDuration that starts a phrase ("Обязательно," split off at its
// comma) to the next one, when the phrase goes on and the result still fits the lines.
func mergeHeads(cues [][]unit, limits layoutLimits) [][]unit {
	merged := make([][]unit, 0, len(cues))

	for i := 0; i < len(cues); i++ {
		c := cues[i]

		if i+1 < len(cues) && isShortCue(c) && !endsPhrase(c[len(c)-1].last(), cues[i+1][0].first()) {
			joined := slices.Concat(c, cues[i+1])
			if fits(joined, limits) {
				merged = append(merged, joined)
				i++

				continue
			}
		}

		merged = append(merged, c)
	}

	return merged
}

func isShortCue(c []unit) bool {
	return c[len(c)-1].last().End-c[0].first().Start < minCueDuration
}

// fits: the joined subtitle keeps to the lines and to a little more than the longest duration.
func fits(joined []unit, limits layoutLimits) bool {
	return linesNeeded(joined, limits.maxLineChars) <= limits.maxLines &&
		joined[len(joined)-1].last().End-joined[0].first().Start <= maxCueDuration+minCueDuration
}

// mergeTails glues a subtitle shorter than minCueDuration to the previous one when the phrase goes on between
// them (no sentence end or pause) and the result still fits the lines.
func mergeTails(cues [][]unit, limits layoutLimits) [][]unit {
	merged := make([][]unit, 0, len(cues))

	for _, c := range cues {
		if len(merged) > 0 {
			prev := merged[len(merged)-1]
			joined := slices.Concat(prev, c)

			if isShortCue(c) && !endsPhrase(prev[len(prev)-1].last(), c[0].first()) && fits(joined, limits) {
				merged[len(merged)-1] = joined

				continue
			}
		}

		merged = append(merged, c)
	}

	return merged
}

func unitsChars(units []unit) int {
	n := len(units) - 1
	for _, u := range units {
		n += u.chars()
	}

	return n
}

// linesNeeded fills lines greedily; a unit longer than a line takes a line of its own.
func linesNeeded(units []unit, maxChars int) int {
	lines, width := 0, 0

	for _, u := range units {
		switch {
		case lines == 0:
			lines, width = 1, u.chars()
		case width+1+u.chars() <= maxChars:
			width += 1 + u.chars()
		default:
			lines++
			width = u.chars()
		}
	}

	return lines
}

// splitLines breaks a subtitle into the fewest lines that fit, as even as possible: the longest line is minimal.
func splitLines(units []unit, maxChars int) [][]token {
	count := linesNeeded(units, maxChars)
	widths := make([]int, len(units))

	for i, u := range units {
		widths[i] = u.chars()
	}

	cuts := balancedCuts(widths, count)
	lines := make([][]token, 0, count)
	from := 0

	for _, to := range append(cuts, len(units)) {
		var line []token
		for _, u := range units[from:to] {
			line = append(line, u.tokens...)
		}

		lines = append(lines, line)
		from = to
	}

	return lines
}

// balancedCuts splits widths into parts lines (each non-empty) minimizing the widest line, where a line's width
// is the sum of its widths plus the spaces between them. It returns the start index of every line but the first.
// Subtitles have a few units, so a full search over the cut points is cheap.
func balancedCuts(widths []int, parts int) []int {
	if parts <= 1 || len(widths) < parts {
		return nil
	}

	best, bestWidth := []int(nil), -1

	var search func(from, left int, cuts []int, widest int)

	search = func(from, left int, cuts []int, widest int) {
		if left == 1 {
			w := max(widest, lineWidth(widths[from:]))
			if bestWidth < 0 || w < bestWidth {
				best, bestWidth = append([]int(nil), cuts...), w
			}

			return
		}

		for to := from + 1; to <= len(widths)-(left-1); to++ {
			search(to, left-1, append(cuts[:len(cuts):len(cuts)], to), max(widest, lineWidth(widths[from:to])))
		}
	}

	search(0, parts, nil, 0)

	return best
}

func lineWidth(widths []int) int {
	n := len(widths) - 1
	for _, w := range widths {
		n += w
	}

	return n
}

// buildCues turns grouped units into subtitles with lines; times are set by timeCues. With explicit accents
// the marked ones are kept as they are: no automatic accent is added. The longest-word accent is skipped in a
// subtitle right after an accented one, so it does not come in every subtitle.
func buildCues(groups [][]unit, style *Style, explicit bool) []cue {
	cues := make([]cue, 0, len(groups))
	prevAccented := false

	for _, g := range groups {
		units := g
		if !explicit {
			units = withAccents(g, style, !prevAccented)
		}

		c := cue{
			Start: units[0].first().Start,
			End:   units[len(units)-1].last().End,
			Lines: cueLines(units, style),
		}

		if style.Layout.TrimEndPunctuation {
			c = trimEnd(c)
		}

		prevAccented = slices.ContainsFunc(c.tokens(), func(t token) bool { return t.Accent != noAccent })
		cues = append(cues, c)
	}

	return cues
}

// withAccents selects the accents of one subtitle and returns its units with them.
func withAccents(units []unit, style *Style, allowLongest bool) []unit {
	var all []token
	for _, u := range units {
		all = append(all, u.tokens...)
	}

	selected := selectAccents(all, style.Accent.MaxPerCue, style.Accent.LongestWord && allowLongest)
	out := make([]unit, 0, len(units))
	i := 0

	for _, u := range units {
		out = append(out, unit{tokens: selected[i : i+len(u.tokens)]})
		i += len(u.tokens)
	}

	return out
}

// cueLines puts a subtitle on lines: by sense in the phrases of the style (phraseLines) unless it has accents, otherwise
// layoutLines.
func cueLines(units []unit, style *Style) [][]token {
	accented := slices.ContainsFunc(units, func(u unit) bool {
		return slices.ContainsFunc(u.tokens, func(t token) bool { return t.Accent != noAccent })
	})

	if style.Layout.Phrases.Enabled && !accented {
		return phraseLines(units, style.lineChars())
	}

	return layoutLines(units, style.lineChars(), style.Layout.MaxLines)
}

// layoutLines puts a subtitle on lines. A long phrase (more than one line) with accented words puts them on
// a line of their own, the words before and after on lines around it, when each of those fits a line;
// otherwise the lines are balanced (splitLines).
func layoutLines(units []unit, maxChars, maxLines int) [][]token {
	if linesNeeded(units, maxChars) <= 1 {
		return splitLines(units, maxChars)
	}

	first, last := -1, -1

	for i, u := range units {
		if slices.ContainsFunc(u.tokens, func(t token) bool { return t.Accent != noAccent }) {
			if first < 0 {
				first = i
			}

			last = i
		}
	}

	if first < 0 || maxLines < 2 {
		return splitLines(units, maxChars)
	}

	parts := [][]unit{units[:first], units[first : last+1], units[last+1:]}
	lines := make([][]token, 0, len(parts))

	for _, part := range parts {
		if len(part) == 0 {
			continue
		}

		if unitsChars(part) > maxChars {
			return splitLines(units, maxChars)
		}

		var line []token
		for _, u := range part {
			line = append(line, u.tokens...)
		}

		lines = append(lines, line)
	}

	return lines
}

// trimEnd drops a period, comma, colon or semicolon after the last word: Reels subtitles do without them.
// Question and exclamation marks and ellipses stay.
func trimEnd(c cue) cue {
	lines := make([][]token, len(c.Lines))
	copy(lines, c.Lines)

	last := lines[len(lines)-1]
	words := make([]token, len(last))
	copy(words, last)

	t := words[len(words)-1]
	if !strings.HasSuffix(t.Text, "...") {
		if trimmed := strings.TrimRight(t.Text, ".,;:"); trimmed != "" {
			t.Text = trimmed
		}
	}

	words[len(words)-1] = t
	lines[len(lines)-1] = words

	return cue{Start: c.Start, End: c.End, Lines: lines}
}
