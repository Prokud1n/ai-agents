package roughcut

import (
	"strings"
	"unicode"
)

// A broken-off word must keep at least this many letters to count as the start of the next one:
// "п" before "приступим" is too little to tell from a preposition.
const (
	minFragmentRunes = 2
	minMissingRunes  = 2
	maxFragmentShare = 0.7
)

// countWords are numbers a trainer counts repetitions with: "раз, два, три, раз, два, три" is not a retake.
var countWords = map[string]struct{}{
	"раз": {}, "два": {}, "три": {}, "четыре": {}, "пять": {}, "шесть": {}, "семь": {}, "восемь": {},
	"девять": {}, "десять": {}, "и": {}, "один": {}, "одна": {},
}

// normalizeWord keeps only letters and digits in lower case, so "Давайте," and "давайте" are equal
// and "э-э" becomes "ээ". "ё" is compared as "е": Whisper writes it either way.
func normalizeWord(w string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(w) {
		switch {
		case r == 'ё':
			b.WriteRune('е')
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		}
	}

	return b.String()
}

// collapseRepeats squeezes runs of the same letter: "эээ" -> "э", "ммм" -> "м", "эммм" -> "эм".
func collapseRepeats(w string) string {
	var (
		b    strings.Builder
		prev rune
	)

	for i, r := range w {
		if i > 0 && r == prev {
			continue
		}

		b.WriteRune(r)
		prev = r
	}

	return b.String()
}

// fillerSet holds the fillers in their collapsed form.
type fillerSet map[string]struct{}

func newFillerSet(fillers []string) fillerSet {
	set := make(fillerSet, len(fillers))

	for _, f := range fillers {
		if n := collapseRepeats(normalizeWord(f)); n != "" {
			set[n] = struct{}{}
		}
	}

	return set
}

// has reports whether a normalized word is a filler. Only a whole word matches, so "ну", "а", "это"
// stay unless they are in the list themselves.
func (s fillerSet) has(norm string) bool {
	if norm == "" {
		return false
	}

	_, ok := s[collapseRepeats(norm)]

	return ok
}

// isTruncation reports whether fragment is a broken-off start of word: "при" of "приступим". The fragment
// must miss at least minMissingRunes letters and be at most maxFragmentShare of the word, so close word
// forms ("зал" / "зале", "ног" / "ноги") are not taken for broken words. Longer forms ("мост" / "мостик")
// pass this check; a sentence end after the word (see sentenceEnds) tells them apart.
func isTruncation(fragment, word string) bool {
	f, w := []rune(fragment), []rune(word)

	return len(f) >= minFragmentRunes &&
		len(w)-len(f) >= minMissingRunes &&
		float64(len(f)) <= maxFragmentShare*float64(len(w)) &&
		string(w[:len(f)]) == fragment
}

func allCountWords(norms []string) bool {
	for _, n := range norms {
		if _, ok := countWords[n]; !ok && !isNumber(n) {
			return false
		}
	}

	return true
}

// isNumeral reports whether a normalized word is a number in digits or a counting word.
func isNumeral(norm string) bool {
	_, ok := countWords[norm]

	return (ok && norm != "и") || isNumber(norm)
}

func isNumber(norm string) bool {
	for _, r := range norm {
		if !unicode.IsDigit(r) {
			return false
		}
	}

	return norm != ""
}
