package roughcut

import (
	"strings"
	"unicode"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// Whisper gives words without punctuation; the text of a segment has it. alignTokens finds for every word
// its token in the segment text ("приседаем." for "приседаем"), so the sentence ends are known and a
// remapped segment keeps its punctuation.

const (
	ellipsis      = "…"
	threeDots     = "..."
	sentenceMarks = ".!?"
	// A word may be missing from the segment text (Whisper writes "10" in one and "десять" in the other):
	// this many tokens are looked ahead before the alignment of the segment is given up.
	alignLookahead = 3
)

// alignTokens returns the segment-text token of every word, or "" where the segment could not be aligned.
func alignTokens(src *transcription.Result) []string {
	tokens := make([]string, len(src.Words))
	w := 0

	for _, s := range src.Segments {
		from := skipBefore(src.Words, w, s.Start)
		to := wordsUntil(src.Words, from, s.End)
		w = to

		aligned, ok := alignSegment(src.Words[from:to], strings.Fields(s.Text))
		if ok {
			copy(tokens[from:to], aligned)
		}
	}

	return tokens
}

// skipBefore returns the first word from w whose middle is not before the segment start.
func skipBefore(words []transcription.Word, w int, start float64) int {
	for w < len(words) && mid(words[w]) < start-segmentStartSlack {
		w++
	}

	return w
}

// wordsUntil returns the end of the run of words from w whose middle is before the segment end.
func wordsUntil(words []transcription.Word, w int, end float64) int {
	for w < len(words) && mid(words[w]) < end {
		w++
	}

	return w
}

func mid(w transcription.Word) float64 {
	return (w.Start + w.End) / 2
}

// alignSegment matches words to the fields of the segment text in order. Fields without letters ("—")
// are skipped. Every word must be found, or the segment is not aligned at all.
func alignSegment(words []transcription.Word, fields []string) ([]string, bool) {
	out := make([]string, len(words))
	f := 0

	for i, w := range words {
		norm := normalizeWord(w.Word)
		found := false

		for k := f; k < len(fields) && k <= f+alignLookahead; k++ {
			if normalizeWord(fields[k]) == norm && norm != "" {
				out[i], f, found = fields[k], k+1, true

				break
			}
		}

		if !found {
			return nil, false
		}
	}

	return out, true
}

// endsSentence reports whether a text token closes a sentence: "приседаем." or "так?", but not "так…".
func endsSentence(token string) bool {
	t := strings.TrimRightFunc(token, func(r rune) bool { return strings.ContainsRune(`"»)`, r) })
	if strings.HasSuffix(t, ellipsis) || strings.HasSuffix(t, threeDots) || t == "" {
		return false
	}

	return strings.ContainsRune(sentenceMarks, []rune(t)[len([]rune(t))-1])
}

// endsOpen reports whether a token was left hanging: no punctuation after it ("мы"), or an ellipsis
// or a dash ("мы…", "мы —"). A comma or a sentence mark means the phrase was said as intended; an unknown
// token ("" where the segment could not be aligned) counts as finished, so nothing is cut on a guess.
func endsOpen(token string) bool {
	if token == "" {
		return false
	}

	t := strings.TrimRightFunc(token, func(r rune) bool { return strings.ContainsRune(`"»)`, r) })
	if strings.HasSuffix(t, ellipsis) || strings.HasSuffix(t, threeDots) || strings.HasSuffix(t, "-") ||
		strings.HasSuffix(t, "—") {
		return true
	}

	r := []rune(t)

	return len(r) > 0 && !unicode.IsPunct(r[len(r)-1])
}

// trailingMark returns the sentence mark that ends a token ("?" of "так?", "…" of "при…"), or "".
func trailingMark(token string) string {
	t := strings.TrimRightFunc(token, func(r rune) bool { return strings.ContainsRune(`"»)`, r) })

	switch {
	case strings.HasSuffix(t, ellipsis):
		return ellipsis
	case strings.HasSuffix(t, threeDots):
		return threeDots
	case endsSentence(t):
		return string([]rune(t)[len([]rune(t))-1])
	}

	return ""
}

// withMark puts a sentence mark at the end of a token in place of a comma, colon or dash it ended with,
// unless it already ends a sentence: "смотрите," + "?" -> "смотрите?".
func withMark(token, mark string) string {
	if mark == "" || trailingMark(token) != "" {
		return token
	}

	return strings.TrimRightFunc(token, func(r rune) bool { return unicode.IsPunct(r) && r != '»' && r != '"' }) + mark
}
