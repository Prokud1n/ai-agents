package roughcut

import (
	"slices"
	"strings"
	"unicode"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// cloneEdit is a deep copy of the edit, so a plan never shares slices with its caller.
func cloneEdit(e *Edit) *Edit {
	return &Edit{
		Accept:  slices.Clone(e.Accept),
		Remove:  slices.Clone(e.Remove),
		Restore: slices.Clone(e.Restore),
		Words:   slices.Clone(e.Words),
		Notes:   slices.Clone(e.Notes),
		Details: slices.Clone(e.Details),
		Mode:    e.Mode,
		CutTime: slices.Clone(e.CutTime),
		Titles:  slices.Clone(e.Titles),
		Mute:    e.Mute,
		Hide:    slices.Clone(e.Hide),
		Hook:    e.Hook,
	}
}

// subtitleWords is the transcript the subtitles are made from: with the corrected words of the edit, if any.
func subtitleWords(src *transcription.Result, edit *Edit) *transcription.Result {
	if edit == nil || len(edit.Words) == 0 {
		return src
	}

	fixed := FixWords(src, edit.Words)

	return &fixed
}

// FixWords returns a copy of the transcript with the corrected words: the word's text and its token in the segment
// text are replaced, and the punctuation around the token stays ("Прокудино," -> "Прокудина,"), so the subtitles
// keep it. A segment whose words could not be aligned with its text gets the text of its words. Fixes outside the
// transcript or with an empty text are skipped (the caller validates them).
func FixWords(src *transcription.Result, fixes []WordFix) transcription.Result {
	out := *src
	out.Words = slices.Clone(src.Words)
	out.Segments = slices.Clone(src.Segments)

	texts := alignTokens(src)
	fixed := make(map[int]bool, len(fixes))

	for _, f := range fixes {
		text := strings.Join(strings.Fields(f.Text), " ")
		if f.Index < 0 || f.Index >= len(out.Words) || text == "" {
			continue
		}

		out.Words[f.Index].Word = text
		fixed[f.Index] = true

		if texts[f.Index] != "" {
			texts[f.Index] = withPunctuation(texts[f.Index], text)
		}
	}

	if len(fixed) == 0 {
		return out
	}

	w := 0

	for i, s := range out.Segments {
		from := skipBefore(out.Words, w, s.Start)
		to := wordsUntil(out.Words, from, s.End)
		w = to

		if !anyFixed(fixed, from, to) {
			continue
		}

		text := joinTokens(texts[from:to])
		if text == "" {
			text = joinWords(out.Words[from:to])
		}

		out.Segments[i].Text = " " + text
	}

	segmentTexts := make([]string, 0, len(out.Segments))
	for _, s := range out.Segments {
		segmentTexts = append(segmentTexts, strings.TrimSpace(s.Text))
	}

	out.Text = strings.Join(segmentTexts, " ")

	return out
}

func anyFixed(fixed map[int]bool, from, to int) bool {
	for i := from; i < to; i++ {
		if fixed[i] {
			return true
		}
	}

	return false
}

// withPunctuation puts the word into the token in place of its letters: what comes before the first letter or digit
// and after the last one stays.
func withPunctuation(token, word string) string {
	runes := []rune(token)
	first, last := -1, -1

	for i, r := range runes {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if first < 0 {
				first = i
			}

			last = i
		}
	}

	if first < 0 {
		return word
	}

	return string(runes[:first]) + word + string(runes[last+1:])
}
