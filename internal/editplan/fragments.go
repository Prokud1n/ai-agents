package editplan

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// A fragment of a sentence left by a revision (T3.29). A revision for the length cuts sentences of the reel; cutting a
// sentence but its last word (or its first) leaves the word alone between the kept phrases: a fragment heard for a
// fraction of a second, without a subtitle of its own. A revision that leaves at most maxFragmentWords words of a
// sentence of the previous version and cuts at least minFragmentCut of its words gets a hint back once (the same
// decision again is accepted) and a line in the details. A sentence ends as in checkPhraseEnds.
const (
	maxFragmentWords = 2
	minFragmentCut   = 3
	// maxFragmentHints named in the hint.
	maxFragmentHints = 3
)

// fragmentDetailFormat is the line of the decisions log.
const fragmentDetailFormat = "Обрывок фразы в правке: слова %s «%s»"

// fragment is what a revision left of a sentence it cut: the sentence and the words left.
type fragment struct {
	sentence roughcut.WordRange
	left     []int
}

// checkFragments adds a line for every fragment a revision left to the details of the edit and returns the hint for
// the model; "" without one. The first version is checked by checkPhraseEnds.
func checkFragments(req *Request, edit *roughcut.Edit, plan *roughcut.Plan) string {
	if !req.IsRevision() {
		return ""
	}

	words := req.Transcript.Words
	found := findFragments(req.Transcript, keptSet(words, req.Plan.Keep), keptSet(words, plan.Keep))

	if len(found) == 0 {
		return ""
	}

	refs := make([]string, 0, len(found))

	for _, f := range found {
		left := indexList(f.left)
		text := joinIndexed(words, f.left)
		edit.Details = append(edit.Details, fmt.Sprintf(fragmentDetailFormat, left, text))
		refs = append(refs, fmt.Sprintf("от предложения «%s» (слова %s) осталось только %s «%s»",
			truncate(quote(words, f.sentence), maxDroppedRunes), indexRange(f.sentence.From, f.sentence.To),
			wordsLabel(len(f.left))+" "+left, text))
	}

	return "обрывки фраз: правка вырезает почти всё предложение, но оставляет одно-два его слова — " +
		strings.Join(refs, "; ") + ". В ролике они прозвучат обрывком без смысла: вырежи и эти слова или верни " +
		"предложение целиком"
}

// findFragments are the sentences of the previous version the revision cut but at most maxFragmentWords words (at
// least minFragmentCut words cut by it), maxFragmentHints at most.
func findFragments(src *transcription.Result, before, after []bool) []fragment {
	words := src.Words
	ends := roughcut.SentenceEnds(src)

	var out []fragment

	for from := 0; from < len(words) && len(out) < maxFragmentHints; {
		to := sentenceEnd(words, ends, from)

		var left []int

		cut := 0

		for i := from; i <= to; i++ {
			switch {
			case after[i]:
				left = append(left, i)
			case before[i]:
				cut++
			}
		}

		if len(left) > 0 && len(left) <= maxFragmentWords && cut >= minFragmentCut {
			out = append(out, fragment{sentence: roughcut.WordRange{From: from, To: to}, left: left})
		}

		from = to + 1
	}

	return out
}

// indexList: «12», «12–13» or «12, 15».
func indexList(indices []int) string {
	if len(indices) == 2 && indices[1] == indices[0]+1 {
		return indexRange(indices[0], indices[1])
	}

	parts := make([]string, 0, len(indices))
	for _, i := range indices {
		parts = append(parts, strconv.Itoa(i))
	}

	return strings.Join(parts, ", ")
}

// wordsLabel: «слово» for one, «слова» for more.
func wordsLabel(n int) string {
	if n == 1 {
		return "слово"
	}

	return "слова"
}
