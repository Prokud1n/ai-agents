package roughcut

import (
	"sort"
	"strings"
	"unicode"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// RemapTranscript moves a transcript of the source onto the timeline of the rendered cut, in the same
// format as transcription.Result. A word is kept if its middle lies in a kept interval; its times are
// shifted by what was cut before it and clipped to the interval. A segment keeps its own text when all
// its words are kept; otherwise its text is rebuilt from its own tokens with punctuation (see segmentText).
// Segments without kept words are dropped.
func RemapTranscript(src *transcription.Result, keep []Interval) transcription.Result {
	return remapTranscript(src, keep, nil)
}

// SubtitleTranscript is the transcript the subtitles of a cut are made from: RemapTranscript of the source with the
// corrected words of the edit (Edit.Words), without the words it hides (Edit.Hide). Hidden words stay in the video and
// the sound; only the subtitles leave them out, the punctuation of their segments is kept as for a cut word.
func SubtitleTranscript(src *transcription.Result, keep []Interval, edit *Edit) transcription.Result {
	return remapTranscript(subtitleWords(src, edit), keep, hiddenWords(edit, len(src.Words)))
}

// hiddenWords marks the words the edit hides in the subtitles; nil without any.
func hiddenWords(edit *Edit, words int) []bool {
	if edit == nil || len(edit.Hide) == 0 {
		return nil
	}

	hidden := make([]bool, words)

	for _, r := range edit.Hide {
		for i := max(r.From, 0); i <= r.To && i < words; i++ {
			hidden[i] = true
		}
	}

	return hidden
}

// remapTranscript is RemapTranscript without the hidden words (nil hides nothing).
func remapTranscript(src *transcription.Result, keep []Interval, hidden []bool) transcription.Result {
	offsets := intervalOffsets(keep)
	words := make([]transcription.Word, 0, len(src.Words))
	keptWord := make([]bool, len(src.Words))

	for i, w := range src.Words {
		k, ok := findInterval(keep, (w.Start+w.End)/2)
		if !ok || (i < len(hidden) && hidden[i]) {
			continue
		}

		iv, off := keep[k], offsets[k]
		moved := w
		moved.Start = roundTime(off + max(w.Start, iv.Start) - iv.Start)
		moved.End = roundTime(off + min(w.End, iv.End) - iv.Start)
		words = append(words, moved)
		keptWord[i] = true
	}

	segments := remapSegments(src, keepIndex(keptWord), words)

	texts := make([]string, 0, len(segments))
	for _, s := range segments {
		texts = append(texts, strings.TrimSpace(s.Text))
	}

	return transcription.Result{
		Language: src.Language,
		Duration: totalDuration(keep),
		Text:     strings.Join(texts, " "),
		Segments: segments,
		Words:    words,
	}
}

// keepIndex maps a source word index to its index among the kept words, or -1.
func keepIndex(kept []bool) []int {
	index := make([]int, len(kept))
	n := 0

	for i, k := range kept {
		index[i] = -1

		if k {
			index[i] = n
			n++
		}
	}

	return index
}

func remapSegments(src *transcription.Result, index []int, words []transcription.Word) []transcription.Segment {
	segments := make([]transcription.Segment, 0, len(src.Segments))
	texts := alignTokens(src)
	w := 0

	for _, s := range src.Segments {
		from := skipBefore(src.Words, w, s.Start)
		to := wordsUntil(src.Words, from, s.End)
		w = to

		kept := make([]transcription.Word, 0, to-from)
		for i := from; i < to; i++ {
			if index[i] >= 0 {
				kept = append(kept, words[index[i]])
			}
		}

		if len(kept) == 0 {
			continue
		}

		text := s.Text
		if len(kept) < to-from {
			text = segmentText(src.Words[from:to], texts[from:to], index[from:to])
		}

		segments = append(segments, transcription.Segment{Start: kept[0].Start, End: kept[len(kept)-1].End, Text: text})
	}

	return segments
}

// segmentText rebuilds the text of a partly kept segment from its own tokens, so punctuation stays:
// a sentence mark of a removed word moves to the kept word before it ("Вы готовы, э?" -> "Вы готовы?"),
// and if the start of a sentence was cut, the first kept word gets the capital letter ("Ну, смотрите." ->
// "Смотрите."). Without aligned tokens the kept words are joined as they are.
func segmentText(words []transcription.Word, texts []string, index []int) string {
	if joinTokens(texts) == "" {
		parts := make([]string, 0, len(words))

		for i, w := range words {
			if index[i] >= 0 {
				parts = append(parts, strings.TrimSpace(w.Word))
			}
		}

		return strings.Join(parts, " ")
	}

	parts := make([]string, 0, len(words))
	capitalize := false

	for i := range words {
		if index[i] >= 0 {
			token := texts[i]
			if capitalize {
				token = capitalized(token)
			}

			parts = append(parts, token)
			capitalize = false

			continue
		}

		if startsSentence(texts, i) && startsUpper(texts[i]) {
			capitalize = true
		}

		if len(parts) > 0 && endsSentence(texts[i]) {
			parts[len(parts)-1] = withMark(parts[len(parts)-1], trailingMark(texts[i]))
		}
	}

	return strings.Join(parts, " ")
}

// startsSentence: the token is the first of its segment or follows a sentence mark.
func startsSentence(texts []string, i int) bool {
	return i == 0 || endsSentence(texts[i-1])
}

// startsUpper reports whether the first letter of a token is upper case ("«Ну," -> true).
func startsUpper(token string) bool {
	for _, r := range token {
		if unicode.IsLetter(r) {
			return unicode.IsUpper(r)
		}
	}

	return false
}

// capitalized upper-cases the first letter of a token, past any opening quote.
func capitalized(token string) string {
	runes := []rune(token)
	for i, r := range runes {
		if unicode.IsLetter(r) {
			runes[i] = unicode.ToUpper(r)

			break
		}
	}

	return string(runes)
}

func joinWords(words []transcription.Word) string {
	parts := make([]string, 0, len(words))
	for _, w := range words {
		parts = append(parts, strings.TrimSpace(w.Word))
	}

	return strings.Join(parts, " ")
}

// intervalOffsets returns where each kept interval starts on the new timeline.
func intervalOffsets(keep []Interval) []float64 {
	offsets := make([]float64, len(keep))
	total := 0.0

	for i, iv := range keep {
		offsets[i] = total
		total += iv.End - iv.Start
	}

	return offsets
}

// findInterval finds the kept interval holding t; keep is sorted and does not overlap.
func findInterval(keep []Interval, t float64) (int, bool) {
	i := sort.Search(len(keep), func(i int) bool { return keep[i].End > t })
	if i < len(keep) && t >= keep[i].Start {
		return i, true
	}

	return 0, false
}
