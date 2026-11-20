package roughcut

// Limits of a failed take: a restart is looked for only this close to the start of the earlier attempt.
const (
	maxAttemptWords   = 30
	maxAttemptSeconds = 20.0
	// A phrase said again is compared only if it has at least this many words.
	minRepeatWords = 2
	// A restart after one broken-off word ("при… приступим") or several words needs a pause, so a phrase
	// said in one breath ("в зал в зале", "при приседании") is never cut.
	restartPause = 0.2
	// The kept take may have this share of extra words inserted while it is compared ("сегодня мы с вами…").
	retakeWindowSlack = 3
)

// token is a content word: not a filler, with letters, in the order of the transcript.
type token struct {
	idx   int // index in the transcript words
	norm  string
	start float64
	end   float64
	// boundary marks the start of a phrase: the first word, a word after a pause or a filler,
	// or the first word of a Whisper segment.
	boundary bool
	// sentenceEnd: the segment text has ".", "!" or "?" right after the word, so the phrase was finished.
	sentenceEnd bool
	// openEnd: the word was left hanging in the segment text (no punctuation, "…" or a dash), see endsOpen.
	openEnd bool
}

// retake is a failed attempt tokens[from:to) restarted at tokens[to]; the kept take spans tokens[to:keptTo).
type retake struct {
	from, to, keptTo int
	kind             TakeKind
	similarity       float64
}

// candidate is a phrase said again that is not cut automatically: the take decision is left to the editor.
type candidate struct {
	retake

	id     int
	reason CandidateReason
}

// detectRetakes scans the content words left to right. An attempt is removed only when it is clearly
// failed: broken off mid-word, or a shorter unfinished start of the next phrase. The last attempt in a series
// is never removed, so the last full take stays. Complete phrases said again (rhythm, enumerations,
// a finished sentence repeated) are returned as candidates and kept in the cut.
func detectRetakes(tokens []token, threshold float64) ([]retake, []candidate) {
	var (
		found      []retake
		candidates []candidate
	)

	for p := 0; p < len(tokens); {
		r, c, ok := findRestart(tokens, p, threshold)
		if c != nil {
			c.id = len(candidates)
			candidates = append(candidates, *c)
		}

		if !ok {
			p++

			continue
		}

		found = append(found, r)
		p = r.to
	}

	return found, candidates
}

// findRestart returns the first restart of the phrase at p or, if there is none, the first candidate.
func findRestart(tokens []token, p int, threshold float64) (retake, *candidate, bool) {
	var first *candidate

	for q := p + 1; q < len(tokens) && q-p <= maxAttemptWords; q++ {
		if tokens[q].start-tokens[p].start > maxAttemptSeconds {
			break
		}

		if r, ok := matchIncomplete(tokens, p, q); ok {
			return r, nil, true
		}

		r, c, ok := matchRepeated(tokens, p, q, threshold)
		if ok {
			return r, nil, true
		}

		if c != nil && first == nil {
			first = c
		}
	}

	return retake{}, first, false
}

// restartsAfterPause reports whether the take at q starts after a pause or at a phrase boundary.
func restartsAfterPause(tokens []token, q int) bool {
	return tokens[q].boundary || tokens[q].start-tokens[q-1].end >= restartPause
}

// matchIncomplete finds "давайте при… давайте приступим": the words of tokens[p:q] are said again after
// a pause and the last of them was broken off (not a finished sentence).
func matchIncomplete(tokens []token, p, q int) (retake, bool) {
	n := q - p
	if q+n > len(tokens) || !restartsAfterPause(tokens, q) || tokens[q-1].sentenceEnd {
		return retake{}, false
	}

	for k := range n - 1 {
		if tokens[p+k].norm != tokens[q+k].norm {
			return retake{}, false
		}
	}

	if !isTruncation(tokens[q-1].norm, tokens[q+n-1].norm) {
		return retake{}, false
	}

	return retake{from: p, to: q, keptTo: keptEnd(tokens, q, n), kind: IncompleteTakeKind, similarity: 1}, true
}

// matchRepeated compares a phrase with the next one that starts with the same word after a pause.
// It is a failed take if enough of its words are said again in order and it was not finished: broken off
// mid-word, or an exact start of the next phrase that goes on further ("сегодня мы… сегодня мы разберём").
// A finished phrase said again is a candidate: rhythm ("вдох, выдох. Вдох, выдох"), enumeration
// ("делаем по десять приседаний… выпадов"), or a repeat of a complete sentence.
func matchRepeated(tokens []token, p, q int, threshold float64) (retake, *candidate, bool) {
	n := q - p
	if n < minRepeatWords || !tokens[p].boundary || !tokens[q].boundary || tokens[p].norm != tokens[q].norm {
		return retake{}, nil, false
	}

	attempt := norms(tokens[p:q])
	if allCountWords(attempt) {
		return retake{}, nil, false
	}

	window := min(q+n+n/retakeWindowSlack, len(tokens))
	matched, truncated := alignTakes(attempt, norms(tokens[q:window]))

	similarity := float64(matched) / float64(n)
	if similarity < threshold {
		return retake{}, nil, false
	}

	r := retake{from: p, to: q, keptTo: keptEnd(tokens, q, n), kind: RepeatedTakeKind, similarity: similarity}

	switch {
	case truncated && !tokens[q-1].sentenceEnd:
		r.kind = IncompleteTakeKind

		return r, nil, true
	case isUnfinishedStart(tokens, p, q):
		return r, nil, true
	case isRhythm(tokens, p, q):
		return retake{}, &candidate{retake: r, reason: RhythmCandidateReason}, false
	default:
		return retake{}, &candidate{retake: r, reason: RepeatCandidateReason}, false
	}
}

// isUnfinishedStart: tokens[p:q] is exactly the start of the phrase at q, that phrase goes on with other
// words, and the attempt was left hanging ("Сегодня мы… Сегодня мы разберём"), not closed by a comma or
// a sentence mark ("Вдох, выдох, вдох, выдох и опускаемся" is rhythm).
func isUnfinishedStart(tokens []token, p, q int) bool {
	n := q - p
	next := q + n

	if !tokens[q-1].openEnd || next >= len(tokens) || tokens[next].boundary || isRhythm(tokens, p, q) {
		return false
	}

	for k := range n {
		if tokens[p+k].norm != tokens[q+k].norm {
			return false
		}
	}

	return true
}

// isRhythm: the phrase is a link of a chain of the same phrase: the previous phrase or the one after
// the repeat starts with it too ("опускаемся вниз, поднимаемся вверх" three times).
func isRhythm(tokens []token, p, q int) bool {
	n := q - p
	if next := q + n; next < len(tokens) && tokens[next].norm == tokens[p].norm {
		return true
	}

	return p-n >= 0 && tokens[p-n].norm == tokens[p].norm
}

// alignTakes counts the attempt's words said again in order (longest common subsequence). The attempt's
// last word also matches the start of a longer word: it may be broken off. truncated reports that it was.
func alignTakes(attempt, kept []string) (matched int, truncated bool) {
	n, m := len(attempt), len(kept)
	lcs := make([][]int, n+1)

	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}

	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if wordsMatch(attempt[i], kept[j], i == n-1) {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	// The last word counts as broken off when it matched only as a prefix and not as itself.
	last := attempt[n-1]
	for _, k := range kept {
		if k == last {
			return lcs[0][0], false
		}
	}

	for _, k := range kept {
		if isTruncation(last, k) {
			truncated = true
		}
	}

	return lcs[0][0], truncated
}

func wordsMatch(a, b string, last bool) bool {
	return a == b || (last && isTruncation(a, b))
}

// keptEnd is where the kept take ends for the log: its phrase, but at least as many words as the attempt.
func keptEnd(tokens []token, q, n int) int {
	return max(phraseEnd(tokens, q), min(q+n, len(tokens)))
}

// phraseEnd returns the index of the next phrase start after q, or len(tokens).
func phraseEnd(tokens []token, q int) int {
	for r := q + 1; r < len(tokens); r++ {
		if tokens[r].boundary {
			return r
		}
	}

	return len(tokens)
}

func norms(tokens []token) []string {
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, t.norm)
	}

	return out
}
