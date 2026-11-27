package editplan

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// The end of a kept sentence (T3.26). cleanup_words cut failed takes, the preparation and the short links and
// reactions said as a line of their own; the model also cut the last words of a kept sentence with them — the end of
// the climax («…and then she ran her first ten kilometres» without «ten kilometres»), so the story broke off. A
// cleaning that cuts the end of a sentence whose start stays in the reel gets a hint back once (like checkEnding: the
// same decision again is accepted) and a line in the details. A sentence cut whole (a link, a reaction) is not
// judged, nor the words said again in a row at the end of a sentence («ну привет ну привет»), nor a failed take glued
// to the end of a kept sentence by the punctuation (takeWords).
//
// A sentence ends at a sentence mark of the segment text (roughcut.SentenceEnds), at a pause longer than phraseGap or
// at the end of the speech.
const maxTailHints = 3 // the cut ends of sentences named in the hint

// Model-facing text of the hint and the line of the decisions log.
const (
	// softRetryHint ends a hint of a soft check (the end of a sentence, the points, a fragment): the same decision
	// again is accepted.
	softRetryHint = " Исправь и вызови submit_edit ещё раз; если так и задумано, отправь то же решение ещё раз — оно " +
		"будет принято."
	tailDetailFormat = "Конец фразы в чистке: слова %s «%s»"
)

// checkPhraseEnds adds a line for every end of a kept sentence the cleaning cut to the details of the edit and returns
// the hint for the model; "" when the cleaning cut none.
func checkPhraseEnds(req *Request, in *Input, edit *roughcut.Edit, plan *roughcut.Plan) string {
	if req.IsRevision() || !req.CleansTakes() || len(in.CleanupWords) == 0 {
		return ""
	}

	words := req.Transcript.Words
	tails := cutSentenceEnds(req, in.CleanupWords, plan)

	if len(tails) == 0 {
		return ""
	}

	refs := make([]string, 0, len(tails))

	for _, r := range tails {
		text := quote(words, r)
		edit.Details = append(edit.Details, fmt.Sprintf(tailDetailFormat, indexRange(r.From, r.To), text))
		refs = append(refs, fmt.Sprintf("слова %s «%s»", indexRange(r.From, r.To), text))
	}

	return "конец фразы: cleanup_words вырезают последние слова предложения, начало которого осталось в ролике, — " +
		strings.Join(refs, "; ") + ". Без них мысль или история обрывается (особенно кульминация): верни конец " +
		"фразы — убери эти слова из cleanup_words или вырежи предложение целиком. Короткую связку или реакцию " +
		"отдельной репликой по-прежнему вырезай целиком. Если это оборванный или неудачный дубль в конце " +
		"предложения — отправь то же решение ещё раз"
}

// cutSentenceEnds are the ends of the sentences the cleaning cut: the words after the last kept word of a sentence up
// to its end, all cut from the reel, with its last word in the cleanup (cleanup) and kept before the decision; at most
// maxTailHints of them.
func cutSentenceEnds(req *Request, cleanup []roughcut.WordRange, plan *roughcut.Plan) []roughcut.WordRange {
	words := req.Transcript.Words
	ends := roughcut.SentenceEnds(req.Transcript)
	before, after := keptSet(words, req.Plan.Keep), keptSet(words, plan.Keep)
	takes := takeWords(req, after)

	var out []roughcut.WordRange

	for from := 0; from < len(words) && len(out) < maxTailHints; {
		to := sentenceEnd(words, ends, from)

		if tail, ok := cutEnd(words, after, from, to); ok && before[to] && inRanges(cleanup, to) &&
			!slices.Contains(takes[tail.From:tail.To+1], true) {
			out = append(out, tail)
		}

		from = to + 1
	}

	return out
}

// sentenceEnd is the last word of the sentence starting at from.
func sentenceEnd(words []transcription.Word, ends []bool, from int) int {
	for i := from; i < len(words)-1; i++ {
		if ends[i] || words[i+1].Start-words[i].End > phraseGap {
			return i
		}
	}

	return len(words) - 1
}

// cutEnd is the end of the sentence from–to cut from the reel after its last kept word; false when the sentence is
// kept to its end or cut whole, or when the cut words repeat the words right before them (a repeat in a row).
func cutEnd(words []transcription.Word, kept []bool, from, to int) (roughcut.WordRange, bool) {
	last := -1

	for i := to; i >= from; i-- {
		if kept[i] {
			last = i

			break
		}
	}

	if last < 0 || last == to {
		return roughcut.WordRange{}, false
	}

	tail := roughcut.WordRange{From: last + 1, To: to}
	size := tail.To - tail.From + 1

	if tail.From-size >= from && slices.Equal(normalizedWords(words[tail.From-size:tail.From]),
		normalizedWords(words[tail.From:tail.To+1])) {
		return roughcut.WordRange{}, false
	}

	return tail, true
}

// normalizedWords are the words normalized (normalizeWord).
func normalizedWords(words []transcription.Word) []string {
	out := make([]string, 0, len(words))
	for _, w := range words {
		out = append(out, normalizeWord(w.Word))
	}

	return out
}
