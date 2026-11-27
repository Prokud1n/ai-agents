package subtitles

import "strings"

// aside is a parenthetical phrase Whisper writes without its commas («да всем всё равно если честно»): its words (the
// norms) and where it is surely an aside — at the end of a sentence (atEnd: «…, если честно.»; not «кстати»: «очень
// кстати.») and at the start of one (atStart: «кстати, …»; not «если честно»: «если честно ответите, …»).
type aside struct {
	words   []string
	atEnd   bool
	atStart bool
}

// asides are the parenthetical phrases that get their commas (T3.31). «короче» is not one: «сделай короче» is a
// comparative; neither is the middle of a sentence without a comma Whisper put after the phrase.
var asides = []aside{
	{words: []string{wordIf, "честно"}, atEnd: true},
	{words: []string{"честно", "говоря"}, atEnd: true, atStart: true},
	{words: []string{"кстати"}, atStart: true},
}

// asideCommas puts the commas around the parenthetical phrases (asides) where they surely are asides: before one that
// ends a sentence (or that Whisper put a comma after), unless a preposition or a conjunction comes before it («а если
// честно, …»); after one that starts a sentence and goes on. Nothing else changes. tokens is not changed.
func asideCommas(tokens []token) []token {
	out := make([]token, len(tokens))
	copy(out, tokens)

	for _, a := range asides {
		for s := 0; s+len(a.words) <= len(out); s++ {
			if !asideAt(out, s, a.words) {
				continue
			}

			e := s + len(a.words) - 1
			if commaBefore(out, s, e, a) {
				withComma(&out[s-1])
			}

			if a.atStart && (s == 0 || sentenceEnds(out[s-1])) && e+1 < len(out) && out[e].Break == noBreak {
				withComma(&out[e])
			}
		}
	}

	return out
}

// asideAt: the words of the phrase are said at s, without a mark between them.
func asideAt(tokens []token, s int, words []string) bool {
	for k, w := range words {
		t := tokens[s+k]
		if t.Norm != w || (k+1 < len(words) && t.Break != noBreak) {
			return false
		}
	}

	return true
}

// commaBefore: the phrase s..e needs a comma before it: a word without a mark before it that is not a function word, and
// after it a comma or (atEnd) the end of the sentence.
func commaBefore(tokens []token, s, e int, a aside) bool {
	if s == 0 || tokens[s-1].Break != noBreak || isProclitic(tokens[s-1].Norm) {
		return false
	}

	last := tokens[e]
	if last.Break == softBreak {
		return true
	}

	return a.atEnd && (e+1 == len(tokens) || sentenceEnds(last))
}

// withComma ends the word with a comma.
func withComma(t *token) {
	t.Text, t.Break = strings.TrimRight(t.Text, ",")+",", softBreak
}
