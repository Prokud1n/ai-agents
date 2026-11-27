package subtitles

import (
	"fmt"
	"math"
	"slices"
)

// PhraseLayout groups the speech of the phrase layout into whole phrases, as the trainer writes her subtitles (T3.23): a
// subtitle is a phrase of one or two lines that ends at the end of a sentence or at a pause; a phrase too long for the
// lines is split where the sense allows it (a comma, a conjunction) into pieces of at least SplitWords words; a piece
// shorter than MinWords words or MinDuration seconds is glued to a neighbor when they fit together, and otherwise
// stays on the screen until the next subtitle (across a pause up to HoldGap). Disabled: the pieces of before
// (groupCues).
type PhraseLayout struct {
	Enabled bool `yaml:"enabled"`
	// Gap is the pause between words that ends a phrase, seconds.
	Gap float64 `yaml:"gap"`
	// MinWords and MinDuration: a shorter subtitle is glued to a neighbor or held longer.
	MinWords    int     `yaml:"min_words"`
	MinDuration float64 `yaml:"min_duration"`
	// SplitWords: the pieces of a split phrase have at least this many words when the phrase allows it.
	SplitWords int `yaml:"split_words"`
	// HoldGap: pieces are glued, and a subtitle stays until the next one, across a pause up to this long, seconds.
	HoldGap float64 `yaml:"hold_gap"`
	// MaxDuration is the longest subtitle, seconds: a longer phrase is split.
	MaxDuration float64 `yaml:"max_duration"`
}

// The trainer's phrases and the limits of their values in a style file.
const (
	trainerPhraseGap         = 0.25
	trainerPhraseMinWords    = 3
	trainerPhraseMinDuration = 1.5
	trainerPhraseSplitWords  = 4
	trainerPhraseHoldGap     = 0.8
	trainerPhraseMaxDuration = 5.0

	minPhraseGap      = 0.1
	maxPhraseGap      = 1.0
	maxPhraseWords    = 8
	minPhraseDuration = 0.5
	maxPhraseDuration = 10.0
	maxPhraseHoldGap  = 2.0
	maxLayoutFadeIn   = 500
)

// Cut preferences of a long phrase and of the lines of a subtitle.
const (
	// sentenceCut: after the end of a sentence; commaCut: after a comma or a dash; conjunctionCut: before a conjunction
	// («, а», «и», «потому что»).
	sentenceCut    = 3
	commaCut       = 2
	conjunctionCut = 1
	// lineBreakPenalty is added to the width of a line split that leaves a preposition, a conjunction or «не» at the
	// end of a line, or a particle («же», «ли») at the start of the next one; lineCommaBonus is taken off when a line
	// ends with punctuation.
	lineBreakPenalty = 1000
	lineCommaBonus   = 4
)

// conjunctions start a clause: a long phrase is split before them.
var conjunctions = wordSet(
	"и", "а", "но", "или", "либо", "что", "чтобы", "потому", "поэтому", "если", "когда", "пока", "хотя", "который",
	"которая", "которое", "которые", "где", "зато", "однако", "ведь", "то",
)

func trainerPhrases() PhraseLayout {
	return PhraseLayout{
		Enabled:     true,
		Gap:         trainerPhraseGap,
		MinWords:    trainerPhraseMinWords,
		MinDuration: trainerPhraseMinDuration,
		SplitWords:  trainerPhraseSplitWords,
		HoldGap:     trainerPhraseHoldGap,
		MaxDuration: trainerPhraseMaxDuration,
	}
}

// validatePhrases checks the fade-in of the subtitles and, when they are on, the values of the phrases.
func validatePhrases(l *Layout) []error {
	var errs []error

	if l.FadeIn < 0 || l.FadeIn > maxLayoutFadeIn {
		errs = append(errs, fmt.Errorf("layout.fade_in must be 0..%d ms", maxLayoutFadeIn))
	}

	p := &l.Phrases
	if !p.Enabled {
		return errs
	}

	if p.Gap < minPhraseGap || p.Gap > maxPhraseGap {
		errs = append(errs, fmt.Errorf("layout.phrases.gap must be %.1f..%.1f s", minPhraseGap, maxPhraseGap))
	}

	if p.MinWords < 1 || p.MinWords > maxPhraseWords || p.SplitWords < 1 || p.SplitWords > maxPhraseWords {
		errs = append(errs, fmt.Errorf("layout.phrases.min_words and split_words must be 1..%d", maxPhraseWords))
	}

	if p.MinDuration < minPhraseDuration || p.MaxDuration > maxPhraseDuration || 2*p.MinDuration > p.MaxDuration {
		errs = append(errs, fmt.Errorf("layout.phrases.min_duration and max_duration must be %.1f..%.0f s, the "+
			"maximum at least twice the minimum", minPhraseDuration, maxPhraseDuration))
	}

	if p.HoldGap < 0 || p.HoldGap > maxPhraseHoldGap {
		errs = append(errs, fmt.Errorf("layout.phrases.hold_gap must be 0..%.0f s", maxPhraseHoldGap))
	}

	return errs
}

// phraseGroups are the units of the speech grouped into subtitles by the phrases of the style: split at the ends of
// phrases, long phrases split by sense, short pieces glued to their neighbors (not across a section heading: barriers
// are the starts of the drawn headings).
func phraseGroups(units []unit, limits layoutLimits, p *PhraseLayout, barriers []float64) [][]unit {
	var pieces [][]unit

	for _, phrase := range splitPhrases(units, p.Gap) {
		pieces = append(pieces, splitLong(phrase, limits, p)...)
	}

	return mergeShort(pieces, limits, p, barriers)
}

// splitPhrases splits the units at the ends of sentences and at pauses of at least gap, but not at a pause right
// after a preposition or a conjunction («и… никто из них»): the phrase goes on.
func splitPhrases(units []unit, gap float64) [][]unit {
	var (
		phrases [][]unit
		current []unit
	)

	for _, u := range units {
		if prev := len(current) - 1; prev >= 0 && phraseEnds(current[prev].last(), u.first(), gap) &&
			(current[prev].last().Break == strongBreak || !isProclitic(current[prev].last().Norm)) {
			phrases = append(phrases, current)
			current = nil
		}

		current = append(current, u)
	}

	if len(current) > 0 {
		phrases = append(phrases, current)
	}

	return phrases
}

// phraseEnds: a sentence or a segment ends between the words, or a pause of at least gap.
func phraseEnds(prev, next token, gap float64) bool {
	return prev.Break == strongBreak || next.Start-prev.End >= gap
}

// fitsPhrase: the units fit the lines and the longest duration.
func fitsPhrase(units []unit, limits layoutLimits, p *PhraseLayout) bool {
	return linesNeeded(units, limits.maxLineChars) <= limits.maxLines && spanOf(units) <= p.MaxDuration
}

// spanOf is the time from the first word of the units to the end of the last one.
func spanOf(units []unit) float64 {
	return units[len(units)-1].last().End - units[0].first().Start
}

// wordCount is the number of words of the units.
func wordCount(units []unit) int {
	n := 0
	for _, u := range units {
		n += len(u.tokens)
	}

	return n
}

// splitLong splits a phrase that does not fit into pieces that do, at the best cut (bestCut) and again in each part.
func splitLong(phrase []unit, limits layoutLimits, p *PhraseLayout) [][]unit {
	if len(phrase) < 2 || fitsPhrase(phrase, limits, p) {
		return [][]unit{phrase}
	}

	at := bestCut(phrase, limits, p)

	return slices.Concat(splitLong(phrase[:at], limits, p), splitLong(phrase[at:], limits, p))
}

// cutScore ranks a cut of a long phrase: parts of at least split_words words first, then parts that both fit, then the
// sense of the place (a comma, a conjunction), then the balance of the parts' lengths.
type cutScore struct {
	words, fit, sense int
	imbalance         int
}

func (a cutScore) better(b cutScore) bool {
	switch {
	case a.words != b.words:
		return a.words > b.words
	case a.fit != b.fit:
		return a.fit > b.fit
	case a.sense != b.sense:
		return a.sense > b.sense
	default:
		return a.imbalance < b.imbalance
	}
}

// bestCut is the index of the unit that starts the second part of the best cut of the phrase (len(phrase) ≥ 2).
func bestCut(phrase []unit, limits layoutLimits, p *PhraseLayout) int {
	best, bestScore := 1, cutScore{}

	for at := 1; at < len(phrase); at++ {
		head, tail := phrase[:at], phrase[at:]
		score := cutScore{sense: cutSense(head[len(head)-1].last(), tail[0].first())}

		if wordCount(head) >= p.SplitWords && wordCount(tail) >= p.SplitWords {
			score.words = 1
		}

		for _, part := range [][]unit{head, tail} {
			if fitsPhrase(part, limits, p) {
				score.fit++
			}
		}

		score.imbalance = abs(unitsChars(head) - unitsChars(tail))

		if at == 1 || score.better(bestScore) {
			best, bestScore = at, score
		}
	}

	return best
}

// cutSense is how good a place for a cut is between the words: after a comma, before a conjunction, elsewhere.
func cutSense(prev, next token) int {
	switch {
	case prev.Break == strongBreak:
		return sentenceCut
	case prev.Break == softBreak:
		return commaCut
	case conjunctions[next.Norm]:
		return conjunctionCut
	default:
		return 0
	}
}

func abs(n int) int {
	if n < 0 {
		return -n
	}

	return n
}

// isShortPiece: fewer words than min_words or said in less than min_duration.
func isShortPiece(piece []unit, p *PhraseLayout) bool {
	return wordCount(piece) < p.MinWords || spanOf(piece) < p.MinDuration
}

// mergeShort glues every short piece to a neighbor (glueSide) while one fits; the pieces are not changed.
func mergeShort(pieces [][]unit, limits layoutLimits, p *PhraseLayout, barriers []float64) [][]unit {
	merged := slices.Clone(pieces)

	for i := 0; i < len(merged); {
		if !isShortPiece(merged[i], p) {
			i++

			continue
		}

		with, ok := glueSide(merged, i, limits, p, barriers)
		if !ok {
			merged = rebalance(merged, i, limits, p, barriers)
			i++

			continue
		}

		from := min(i, with)
		joined := slices.Concat(merged[from], merged[from+1])
		merged = slices.Replace(merged, from, from+2, joined)
		i = from
	}

	return merged
}

// rebalance moves the cut between the short piece i and a neighbor too long to take it whole (the next one first)
// to the best place that leaves neither of them short (the sense of the place, then the balance): «привет!» and a
// long phrase become «привет! поговорим о том,» and the rest. The pieces stay as they are when no such place is.
func rebalance(pieces [][]unit, i int, limits layoutLimits, p *PhraseLayout, barriers []float64) [][]unit {
	for _, with := range []int{i + 1, i - 1} {
		if with < 0 || with >= len(pieces) {
			continue
		}

		from := min(i, with)
		a, b := pieces[from], pieces[from+1]

		if b[0].first().Start-a[len(a)-1].last().End > p.HoldGap ||
			crossesBarrier(a[len(a)-1].last(), b[0].first(), barriers) {
			continue
		}

		if head, tail, ok := evenCut(slices.Concat(a, b), limits, p); ok {
			return slices.Concat(pieces[:from], [][]unit{head, tail}, pieces[from+2:])
		}
	}

	return pieces
}

// evenCut is the best cut of the joined pieces into two that fit and are not short: by the sense of the place, then
// the balance of their lengths; false when there is none.
func evenCut(joined []unit, limits layoutLimits, p *PhraseLayout) (head, tail []unit, ok bool) {
	bestSense, bestImbalance := -1, 0

	for at := 1; at < len(joined); at++ {
		h, t := joined[:at], joined[at:]
		if !fitsPhrase(h, limits, p) || !fitsPhrase(t, limits, p) || isShortPiece(h, p) || isShortPiece(t, p) {
			continue
		}

		sense := cutSense(h[len(h)-1].last(), t[0].first())
		imbalance := abs(unitsChars(h) - unitsChars(t))

		if sense > bestSense || (sense == bestSense && imbalance < bestImbalance) {
			head, tail, ok = h, t, true
			bestSense, bestImbalance = sense, imbalance
		}
	}

	return head, tail, ok
}

// glueSide is the neighbor the short piece i is glued to: within its sentence first (a pause between them, not the end
// of a sentence), then the next one before the previous one; only across a pause up to hold_gap, not across a heading,
// and when the joined piece fits. false when none fits.
func glueSide(pieces [][]unit, i int, limits layoutLimits, p *PhraseLayout, barriers []float64) (int, bool) {
	type side struct {
		with     int
		sentence bool
	}

	var sides []side

	for _, with := range []int{i + 1, i - 1} {
		if with < 0 || with >= len(pieces) {
			continue
		}

		a, b := pieces[min(i, with)], pieces[max(i, with)]
		prev, next := a[len(a)-1].last(), b[0].first()

		if next.Start-prev.End > p.HoldGap || crossesBarrier(prev, next, barriers) ||
			!fitsPhrase(slices.Concat(a, b), limits, p) {
			continue
		}

		sides = append(sides, side{with: with, sentence: prev.Break != strongBreak})
	}

	// Stable: the next piece stays before the previous one among the same kind.
	slices.SortStableFunc(sides, func(x, y side) int {
		switch {
		case x.sentence == y.sentence:
			return 0
		case x.sentence:
			return -1
		default:
			return 1
		}
	})

	if len(sides) == 0 {
		return 0, false
	}

	return sides[0].with, true
}

// crossesBarrier: a heading starts between the words (within readingWindow of them).
func crossesBarrier(prev, next token, barriers []float64) bool {
	return slices.ContainsFunc(barriers, func(at float64) bool {
		return at > prev.End-readingWindow && at <= next.Start+readingWindow
	})
}

// phraseLines puts a phrase on the fewest lines that fit, cut where the sense allows: not after a preposition, a
// conjunction or «не», not before a particle («же», «ли»), after punctuation when it is as good; otherwise as even as
// possible (the widest line is the narrowest).
func phraseLines(units []unit, maxChars int) [][]token {
	count := linesNeeded(units, maxChars)
	if count <= 1 || len(units) < count {
		return splitLines(units, maxChars)
	}

	best, bestScore := []int(nil), math.MaxInt

	var search func(from, left int, cuts []int)

	search = func(from, left int, cuts []int) {
		if left == 1 {
			if score, ok := linesScore(units, cuts, maxChars); ok && score < bestScore {
				best, bestScore = slices.Clone(cuts), score
			}

			return
		}

		for to := from + 1; to <= len(units)-(left-1); to++ {
			search(to, left-1, append(cuts[:len(cuts):len(cuts)], to))
		}
	}

	search(0, count, nil)

	if best == nil {
		return splitLines(units, maxChars)
	}

	return linesAt(units, best)
}

// linesScore scores the lines that start at the cuts: the widest line, the penalty of a cut that breaks a preposition
// or a particle from its word, less the bonus of a cut after punctuation; false when a line of more than one unit is
// wider than maxChars.
func linesScore(units []unit, cuts []int, maxChars int) (int, bool) {
	widest, adjust, from := 0, 0, 0

	for _, to := range append(slices.Clone(cuts), len(units)) {
		line := units[from:to]
		width := unitsChars(line)

		if width > maxChars && len(line) > 1 {
			return 0, false
		}

		widest = max(widest, width)

		if to < len(units) {
			last, next := line[len(line)-1].last(), units[to].first()

			switch {
			case isProclitic(last.Norm) || isEnclitic(next.Norm):
				adjust += lineBreakPenalty
			case last.Break != noBreak:
				adjust -= lineCommaBonus
			}
		}

		from = to
	}

	return widest + adjust, true
}

// linesAt joins the units into the lines that start at the cuts.
func linesAt(units []unit, cuts []int) [][]token {
	lines := make([][]token, 0, len(cuts)+1)
	from := 0

	for _, to := range append(slices.Clone(cuts), len(units)) {
		var line []token
		for _, u := range units[from:to] {
			line = append(line, u.tokens...)
		}

		lines = append(lines, line)
		from = to
	}

	return lines
}
