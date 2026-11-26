package subtitles

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// maxReadingFillers: the words of a reading that are not in the heading («номер» in «страх номер один»).
const maxReadingFillers = 2

// readingFillers are said between the words of a heading when it is read out: «страх номер один» for «СТРАХ 1».
var readingFillers = wordSet("номер", "пункт")

// readingNumbers map the numerals and the ordinal words of one to ten to their digits: «один», «первый» and
// «во-первых» read «1» out.
var readingNumbers = readingNumberWords()

// readingNumberWords builds readingNumbers: the cardinals with their forms, the ordinals with their endings, «во-…ых».
func readingNumberWords() map[string]string {
	cardinals := [][]string{
		{"один", "одна", "одно", "одного", "одной", "одну"}, {"два", "две", "двух"}, {"три", "трех"},
		{"четыре", "четырех"}, {"пять", "пяти"}, {"шесть", "шести"}, {"семь", "семи"}, {"восемь", "восьми"},
		{"девять", "девяти"}, {"десять", "десяти"},
	}
	ordinals := []string{"перв", "втор", "трет", "четверт", "пят", "шест", "седьм", "восьм", "девят", "десят"}
	endings := []string{"ый", "ой", "ая", "ое", "ую", "ого", "ом", "ые", "ий", "ья", "ье", "ью", "ьего", "ьей"}
	adverbs := []string{"вопервых", "вовторых", "втретьих", "вчетвертых", "впятых"}

	out := make(map[string]string, len(cardinals)*len(endings))

	for i, forms := range cardinals {
		digit := strconv.Itoa(i + 1)

		for _, f := range forms {
			out[f] = digit
		}

		for _, e := range endings {
			out[ordinals[i]+e] = digit
		}
	}

	for i, a := range adverbs {
		out[a] = strconv.Itoa(i + 1)
	}

	return out
}

// readingWord is a word of a heading or of the speech as they are compared: normalized, a number as its digits.
func readingWord(norm string) string {
	if digit, ok := readingNumbers[norm]; ok {
		return digit
	}

	if trimmed := strings.TrimLeft(norm, "0"); trimmed != "" && isDigits(norm) {
		return trimmed
	}

	return norm
}

// headingWords are the words of a heading as readingWord compares them: «СТРАХ №1» → страх, 1.
func headingWords(text string) []string {
	parts := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if w := readingWord(normalize(p)); w != "" {
			out = append(out, w)
		}
	}

	return out
}

// hideReadings drops the words that read a drawn section heading out («страх номер один» under «СТРАХ 1»): the
// heading shows them. Only the reading goes: the rest of its subtitle (the question) is shown, and the word before it
// ends a subtitle, so the speech before the heading does not run into the question. A phrase at the heading that is
// not its reading stays. tokens is not changed.
func hideReadings(tokens []token, headings []noteBlock) []token {
	hidden := make([]bool, len(tokens))
	found := false

	for _, h := range headings {
		if len(h.entries) == 0 {
			continue
		}

		from, to, ok := findReading(tokens, h.start, headingWords(h.entries[0]))
		if !ok {
			continue
		}

		for i := from; i <= to; i++ {
			hidden[i] = true
		}

		found = true
	}

	if !found {
		return tokens
	}

	out := make([]token, 0, len(tokens))

	for i, t := range tokens {
		if hidden[i] {
			continue
		}

		if i+1 < len(tokens) && hidden[i+1] {
			t.Break = strongBreak
		}

		out = append(out, t)
	}

	return out
}

// findReading finds the words that read the heading out: a run that starts within readingWindow of the heading's
// start and is the heading's words in any order with at most maxReadingFillers fillers, at least half of the heading
// and at least half of the run matching. false when no run reads it.
func findReading(tokens []token, start float64, heading []string) (from, to int, ok bool) {
	if len(heading) == 0 {
		return 0, 0, false
	}

	for i, t := range tokens {
		if math.Abs(t.Start-start) > readingWindow {
			continue
		}

		if end, matched := readingRun(tokens, i, heading); matched {
			return i, end, true
		}
	}

	return 0, 0, false
}

// readingRun reads the heading's words from the token at: the last word of the reading and whether it reads the
// heading out (at least half of the heading and of the run match). It stops at a word that is neither a heading's
// word left nor a filler, and after the end of a sentence.
func readingRun(tokens []token, at int, heading []string) (end int, ok bool) {
	left := slices.Clone(heading)
	matched, fillers, last := 0, 0, -1

run:
	for i := at; i < len(tokens); i++ {
		w := readingWord(tokens[i].Norm)

		switch idx := slices.Index(left, w); {
		case idx >= 0:
			left = slices.Delete(left, idx, idx+1)
			matched, last = matched+1, i
		case readingFillers[w] && fillers < maxReadingFillers:
			fillers++
		default:
			break run
		}

		if tokens[i].Break == strongBreak {
			break
		}
	}

	if last < 0 || 2*matched < len(heading) || 2*matched < last-at+1 {
		return 0, false
	}

	return last, true
}
