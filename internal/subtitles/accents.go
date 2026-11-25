package subtitles

import (
	"strings"
	"unicode/utf8"
)

const (
	// maxAccentSuffix lets an accent entry match inflected forms: "присед" matches "приседе", "приседа".
	maxAccentSuffix = 3
	// minStemRunes: shorter entries match only exactly, so "вес" does not match "весь" or "весна".
	minStemRunes = 4
	// minLongestRunes: only a long word is worth an accent of its own.
	minLongestRunes = 7
)

// phrase is an accent entry split into normalized words.
type phrase []string

func parsePhrases(entries []string) []phrase {
	phrases := make([]phrase, 0, len(entries))

	for _, e := range entries {
		var p phrase

		for _, f := range strings.Fields(e) {
			if n := normalize(f); n != "" {
				p = append(p, n)
			}
		}

		if len(p) > 0 {
			phrases = append(phrases, p)
		}
	}

	return phrases
}

// markAccents marks accent candidates before grouping. With an explicit list (from the Montage agent) only its
// phrases are accented. Otherwise numbers with the word after them ("10 повторений") and the trainer's dictionary
// words are marked; the longest word is chosen later, per subtitle (selectAccents).
func markAccents(tokens []token, explicit, dictionary []string) []token {
	marked := make([]token, len(tokens))
	copy(marked, tokens)

	if list := parsePhrases(explicit); len(list) > 0 {
		markPhrases(marked, list, explicitAccent)

		return marked
	}

	markPhrases(marked, parsePhrases(dictionary), dictionaryAccent)
	markNumbers(marked)

	return marked
}

// markNumbers accents a number with the word after it ("10 повторений", "двадцать пять повторений"), but not
// counting: a number next to another number ("раз два три четыре", "1 2 3") is not a quantity.
// tokens is a copy owned by the caller.
func markNumbers(tokens []token) {
	markNumberGroups(tokens, false)
}

// markNumberGroups is markNumbers; chained also accents a group right after another one: the word layout shows every
// quantity as an event of its own («3 подхода» | «10 повторений»), so they never merge.
func markNumberGroups(tokens []token, chained bool) {
	for i := 0; i < len(tokens); i++ {
		if !isNumber(tokens[i].Norm) {
			continue
		}

		end := numberEnd(tokens, i)
		counting := (i > 0 && isNumber(tokens[i-1].Norm)) || (end+1 < len(tokens) && isNumber(tokens[end+1].Norm))
		// "3 подхода 10 повторений": a group right after another one is not accented, so they do not merge
		// into one long accent.
		counting = counting || (!chained && i > 0 && tokens[i-1].Accent == numberAccent)

		if !counting && end+1 < len(tokens) && tokens[end].Break == noBreak && isSignificant(tokens[end+1].Norm) {
			for j := i; j <= end+1; j++ {
				tokens[j].Accent = numberAccent
			}
		}

		i = end
	}
}

// numberEnd returns the index of the last word of the number starting at i: compound numerals are one number.
func numberEnd(tokens []token, i int) int {
	for i+1 < len(tokens) && tokens[i].Break == noBreak && isCompoundNumber(tokens[i].Norm, tokens[i+1].Norm) {
		i++
	}

	return i
}

// markPhrases sets kind on every token sequence matching a phrase. tokens is a copy owned by the caller.
func markPhrases(tokens []token, phrases []phrase, kind accentKind) {
	for i := range tokens {
		for _, p := range phrases {
			if !matchesAt(tokens, i, p) {
				continue
			}

			for j := range p {
				tokens[i+j].Accent = kind
			}
		}
	}
}

func matchesAt(tokens []token, at int, p phrase) bool {
	if at+len(p) > len(tokens) {
		return false
	}

	for j, word := range p {
		if !matchesWord(tokens[at+j].Norm, word) {
			return false
		}
	}

	return true
}

// matchesWord: the word is the entry, or the entry of at least 4 letters with a short ending (an inflected form).
func matchesWord(norm, entry string) bool {
	if norm == entry {
		return true
	}

	entryLen := utf8.RuneCountInString(entry)
	if entryLen < minStemRunes || !strings.HasPrefix(norm, entry) {
		return false
	}

	return utf8.RuneCountInString(norm)-entryLen <= maxAccentSuffix
}

// selectAccents keeps accents of one subtitle: all explicit ones; otherwise up to maxPerCue groups of automatic
// ones by priority (numbers, then dictionary words). A subtitle without them gets its longest meaningful word
// accented when longest is set — the caller clears it after an accented subtitle, so such accents never come
// in two subtitles in a row.
func selectAccents(tokens []token, maxPerCue int, longest bool) []token {
	out := make([]token, len(tokens))
	copy(out, tokens)

	for _, t := range out {
		if t.Accent == explicitAccent {
			return out
		}
	}

	groups := accentGroups(out)
	kept := 0

	for _, kind := range []accentKind{numberAccent, dictionaryAccent} {
		for _, g := range groups {
			if out[g[0]].Accent != kind {
				continue
			}

			keep := kept < maxPerCue
			for i := g[0]; i < g[1]; i++ {
				if !keep {
					out[i].Accent = noAccent
				}
			}

			if keep {
				kept++
			}
		}
	}

	if kept == 0 && longest && maxPerCue > 0 {
		if i, ok := longestWord(out); ok {
			out[i].Accent = longestAccent
		}
	}

	return out
}

// accentGroups returns [from, to) ranges of adjacent tokens with the same accent kind.
func accentGroups(tokens []token) [][2]int {
	var groups [][2]int

	for i := 0; i < len(tokens); {
		j := i + 1
		for j < len(tokens) && tokens[j].Accent == tokens[i].Accent {
			j++
		}

		if tokens[i].Accent != noAccent {
			groups = append(groups, [2]int{i, j})
		}

		i = j
	}

	return groups
}

func longestWord(tokens []token) (int, bool) {
	best, bestLen := 0, 0

	for i, t := range tokens {
		n := utf8.RuneCountInString(t.Norm)
		if n > bestLen && n >= minLongestRunes && isSignificant(t.Norm) && !isCommand(t.Norm) {
			best, bestLen = i, n
		}
	}

	return best, bestLen > 0
}
