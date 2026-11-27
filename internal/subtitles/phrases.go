package subtitles

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

// PhraseLayout groups the speech of the phrase layout into whole phrases, as the trainer writes her subtitles (T3.23): a
// subtitle is a phrase of one or two lines that ends at the end of a sentence or at a pause; a phrase too long for the
// lines is split where the sense allows it (a comma, a conjunction) into pieces of at least SplitWords words; a piece
// shorter than MinWords words or MinDuration seconds is glued to a neighbor in its sentence when they fit together, and
// otherwise stays on the screen until the next subtitle (across a pause up to HoldGap). The end of a sentence is never
// glued to the start of the next one (T3.27): a short sentence of its own («второе.», «и третье.») stands alone, or,
// when it would be on the screen less than MinDurationShort, goes with the whole next sentence («первое: …»); a chain
// of one-word sentences («помогу. расскажу. объясню.») is one sentence with commas. Disabled: the pieces of before
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
	// MinDurationShort: a short sentence of its own stands alone when it is on the screen at least this long (until
	// the next subtitle), otherwise it goes with the whole next sentence when they fit; 0: it always stands alone.
	MinDurationShort float64 `yaml:"min_duration_short"`
}

// The trainer's phrases and the limits of their values in a style file.
const (
	trainerPhraseGap         = 0.25
	trainerPhraseMinWords    = 3
	trainerPhraseMinDuration = 1.5
	trainerPhraseSplitWords  = 4
	trainerPhraseHoldGap     = 0.8
	trainerPhraseMaxDuration = 5.0
	// trainerPhraseMinShort: her «второе», «и третье» stand alone 0.5..0.7 s; a shorter one goes with its sentence.
	trainerPhraseMinShort = 0.8

	minPhraseGap      = 0.1
	maxPhraseGap      = 1.0
	maxPhraseWords    = 8
	minPhraseDuration = 0.5
	maxPhraseDuration = 10.0
	maxPhraseHoldGap  = 2.0
	maxLayoutFadeIn   = 500
)

// maxEnumeratorWords: an ordinal sentence that goes with the next one has at most this many words («и третье.»).
const maxEnumeratorWords = 2

// Cut preferences of a long phrase and of the lines of a subtitle.
const (
	// sentenceCut: after the end of a sentence; commaCut: after a comma or a dash; conjunctionCut: before a conjunction
	// («, а», «и», «потому что»).
	sentenceCut    = 3
	commaCut       = 2
	conjunctionCut = 1
	// brokenCut: after a preposition, a conjunction or «не» or before a particle («в | неделю», «могу | ли»): the
	// worst place, taken only when there is no other.
	brokenCut = -1
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

		MinDurationShort: trainerPhraseMinShort,
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

	return append(errs, validatePhraseTimes(p)...)
}

// validatePhraseTimes checks the durations and the hold of the phrases.
func validatePhraseTimes(p *PhraseLayout) []error {
	var errs []error

	if p.MinDuration < minPhraseDuration || p.MaxDuration > maxPhraseDuration || 2*p.MinDuration > p.MaxDuration {
		errs = append(errs, fmt.Errorf("layout.phrases.min_duration and max_duration must be %.1f..%.0f s, the "+
			"maximum at least twice the minimum", minPhraseDuration, maxPhraseDuration))
	}

	if p.HoldGap < 0 || p.HoldGap > maxPhraseHoldGap {
		errs = append(errs, fmt.Errorf("layout.phrases.hold_gap must be 0..%.0f s", maxPhraseHoldGap))
	}

	if p.MinDurationShort < 0 || p.MinDurationShort > p.MinDuration {
		errs = append(errs, errors.New("layout.phrases.min_duration_short must be 0..min_duration"))
	}

	return errs
}

// phraseGroups are the units of the speech grouped into subtitles by the phrases of the style: split at the ends of
// phrases, long phrases split by sense, short pieces glued to their neighbors (not across a section heading: barriers
// are the starts of the drawn headings).
func phraseGroups(units []unit, limits layoutLimits, p *PhraseLayout, barriers []float64) [][]unit {
	var pieces [][]unit

	for _, phrase := range splitPhrases(units, p.Gap, p.HoldGap) {
		pieces = append(pieces, splitLong(phrase, limits, p)...)
	}

	return mergeShort(pieces, limits, p, barriers)
}

// splitPhrases splits the units at the ends of sentences and at pauses of at least gap, but not at a pause up to
// holdGap right after a preposition or a conjunction («и… каждый из нас»): the phrase goes on.
func splitPhrases(units []unit, gap, holdGap float64) [][]unit {
	var (
		phrases [][]unit
		current []unit
	)

	for _, u := range units {
		if len(current) > 0 && splitsAt(current[len(current)-1].last(), u.first(), gap, holdGap) {
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

// splitsAt: a phrase ends between the words (phraseEnds), unless the pause follows a preposition or a conjunction and
// is not longer than holdGap.
func splitsAt(prev, next token, gap, holdGap float64) bool {
	if !phraseEnds(prev, next, gap) {
		return false
	}

	return prev.Break == strongBreak || !isProclitic(prev.Norm) || next.Start-prev.End > holdGap
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

// cutSense is how good a place for a cut is between the words: after the end of a sentence, after a comma, before a
// conjunction, elsewhere; worst between a function word and its word.
func cutSense(prev, next token) int {
	switch {
	case prev.Break == noBreak && isProclitic(prev.Norm), isEnclitic(next.Norm):
		return brokenCut
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
		head := merged[from]

		if sentenceEnds(head[len(head)-1].last()) {
			head = introduction(head)
		}

		joined := slices.Concat(head, merged[from+1])
		merged = slices.Replace(merged, from, from+2, joined)
		i = from
	}

	return merged
}

// sentenceEnds: a sentence ends after the word (not only a Whisper segment without a sentence mark).
func sentenceEnds(t token) bool {
	return t.Break == strongBreak && !t.open
}

// wholeSentence: the piece i is a sentence of its own: the one before it ends a sentence, and so does the piece.
func wholeSentence(pieces [][]unit, i int) bool {
	if i > 0 && !sentenceEnds(pieces[i-1][len(pieces[i-1])-1].last()) {
		return false
	}

	return sentenceEnds(pieces[i][len(pieces[i])-1].last())
}

// shownFor is how long the piece i stays on the screen: until the next one when the pause before it is up to
// hold_gap; +Inf otherwise (it stays at least min_duration).
func shownFor(pieces [][]unit, i int, p *PhraseLayout) float64 {
	piece := pieces[i]
	if i+1 >= len(pieces) || pieces[i+1][0].first().Start-piece[len(piece)-1].last().End > p.HoldGap {
		return math.Inf(1)
	}

	return pieces[i+1][0].first().Start - piece[0].first().Start
}

// isEnumerator: the word is an ordinal that counts the points of a talk: «первое», «третье», «во-вторых».
func isEnumerator(norm string) bool {
	if _, ok := readingNumbers[norm]; !ok {
		return false
	}

	return strings.HasSuffix(norm, "ое") || strings.HasSuffix(norm, "ье") || strings.HasSuffix(norm, "ых")
}

// introduction is the short sentence that goes with the next one: an ordinal («второе.», «и третье.») ends with a
// colon instead of its period («второе: …»); any other sentence stays as it is. piece is not changed.
func introduction(piece []unit) []unit {
	words := wordCount(piece)
	last := piece[len(piece)-1].last()

	if words > maxEnumeratorWords || !isEnumerator(last.Norm) || sentenceMark(last.Text) != "." {
		return piece
	}

	out := slices.Clone(piece)
	u := &out[len(out)-1]
	u.tokens = slices.Clone(u.tokens)
	t := &u.tokens[len(u.tokens)-1]
	t.Text, t.Break = strings.TrimSuffix(t.Text, ".")+":", softBreak

	return out
}

// rebalance moves the cut between the short piece i and a neighbor in its sentence too long to take it whole (the next
// one first) to the best place that leaves neither of them short (the sense of the place, then the balance): «каждый
// | из нас может начать прямо сегодня утром» becomes two even pieces. Not across the end of a sentence: the pieces stay
// as they are when no such place is.
func rebalance(pieces [][]unit, i int, limits layoutLimits, p *PhraseLayout, barriers []float64) [][]unit {
	for _, with := range []int{i + 1, i - 1} {
		if with < 0 || with >= len(pieces) {
			continue
		}

		from := min(i, with)
		a, b := pieces[from], pieces[from+1]
		prev := a[len(a)-1].last()

		if sentenceEnds(prev) || b[0].first().Start-prev.End > p.HoldGap || crossesBarrier(prev, b[0].first(), barriers) {
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
	bestSense, bestImbalance := brokenCut-1, 0

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

// glueSide is the neighbor the short piece i is glued to: within its sentence (a pause between them, not the end of a
// sentence), the next one before the previous one; a short sentence of its own on the screen less than
// min_duration_short goes with the whole next sentence; never the end of a sentence with the start of the next one. Only
// across a pause up to hold_gap, not across a heading, and when the joined piece fits. false when none fits.
func glueSide(pieces [][]unit, i int, limits layoutLimits, p *PhraseLayout, barriers []float64) (int, bool) {
	for _, with := range []int{i + 1, i - 1} {
		if with < 0 || with >= len(pieces) {
			continue
		}

		a, b := pieces[min(i, with)], pieces[max(i, with)]
		if !sentenceEnds(a[len(a)-1].last()) && gluable(a, b, limits, p, barriers) {
			return with, true
		}
	}

	next := i + 1
	if next < len(pieces) && wholeSentence(pieces, i) && sentenceEnds(pieces[next][len(pieces[next])-1].last()) &&
		shownFor(pieces, i, p) < p.MinDurationShort && gluable(pieces[i], pieces[next], limits, p, barriers) {
		return next, true
	}

	return 0, false
}

// gluable: the piece a and the next piece b may be one subtitle: a pause up to hold_gap between them, no heading, and
// together they fit.
func gluable(a, b []unit, limits layoutLimits, p *PhraseLayout, barriers []float64) bool {
	prev, next := a[len(a)-1].last(), b[0].first()

	return next.Start-prev.End <= p.HoldGap && !crossesBarrier(prev, next, barriers) &&
		fitsPhrase(slices.Concat(a, b), limits, p)
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

// commaChains joins a chain of one-word sentences (the periods Whisper puts into an enumeration: «Помогу. Расскажу.
// Объясню.») into one sentence with commas («помогу, расскажу, объясню.»): at least two in a row, each with a period,
// said within holdGap of each other; ordinals («Первое.», «Второе.») stay sentences. tokens is not changed.
func commaChains(tokens []token, holdGap float64) []token {
	single := make([]bool, len(tokens))
	for i := range tokens {
		single[i] = oneWordSentence(tokens, i)
	}

	out := slices.Clone(tokens)

	for i := 0; i+1 < len(out); i++ {
		if !single[i] || !single[i+1] || tokens[i+1].Start-tokens[i].End > holdGap {
			continue
		}

		out[i].Text, out[i].Break = strings.TrimSuffix(out[i].Text, ".")+",", softBreak

		if capitals(out[i+1].Text) == 1 {
			out[i+1].Text = lowerFirstLetter(out[i+1].Text)
		}
	}

	return out
}

// oneWordSentence: the token i is a sentence of one word ending with a period, not an ordinal.
func oneWordSentence(tokens []token, i int) bool {
	t := tokens[i]
	if i > 0 && !sentenceEnds(tokens[i-1]) {
		return false
	}

	return sentenceEnds(t) && sentenceMark(t.Text) == "." && !isEnumerator(t.Norm)
}
