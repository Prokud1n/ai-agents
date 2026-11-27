package editplan

import (
	"fmt"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// The ending of a selected reel (T3.22). The trainer's reels keep the proof or the story of the last point (her
// clients in other cities): it is the climax, the viewers watch to the end for it; they end on the question to the
// viewer and the call («пиши в комментариях»), without the self-introduction and «подписывайся» after it. A selection
// that cut the end of the last section for the length and kept the introduction after the call gets a hint back once
// (the model may submit the same decision again: a caption may ask to keep the introduction) and a line in the details.
//
// The reel is recognised by its words: the last section heading (screen_notes), a kept word of the call
// («коммент…») after it, a kept introduction after the call («подписыв…», «меня зовут»), and a run of at least
// minClimaxWords words the decision cut right before the call (the question to the viewer and the words of the call
// before «комментариях» may be kept between them). Without a section heading or the words of the call nothing is said:
// the hint is never a guess about a reel of another structure.
const (
	// minClimaxWords: a cut run this long at the end of the last section is its proof or story, not a short link.
	minClimaxWords = 8
	// callLeadWords: the kept words of the question and the call before the word of the call («а какие страхи у
	// тебя, пиши в» комментариях).
	callLeadWords = 10
	// maxPromoWords quoted of the introduction.
	maxPromoWords = 8
)

// Stems of the words of the call and of the introduction after it.
const (
	callStem      = "коммент"
	subscribeStem = "подписыв"
	introWord     = "меня"
	introNameWord = "зовут"
)

// Model-facing text of the hint and the line of the decisions log.
const (
	endingRetryHint = " Исправь и вызови submit_edit ещё раз; если так и задумано (тренер просила оставить " +
		"представление), отправь то же решение ещё раз — оно будет принято."
	endingDetailFormat = "Финал: вырезан конец последнего раздела (слова %s), оставлено представление после призыва " +
		"(слова %s)"
)

// endingIssue is a selected reel that cut the end of its last section (tail) and kept the introduction after the call
// (promo).
type endingIssue struct {
	tail, promo roughcut.WordRange
}

// checkEnding adds the line about an ending cut like that to the details of the edit and returns the hint for the
// model; "" when the ending is not so.
func checkEnding(req *Request, in *Input, edit *roughcut.Edit, sel *selection) string {
	issue, ok := findEndingIssue(req, in, edit, sel)
	if !ok {
		return ""
	}

	words := req.Transcript.Words
	tail, promo := indexRange(issue.tail.From, issue.tail.To), indexRange(issue.promo.From, issue.promo.To)
	edit.Details = append(edit.Details, fmt.Sprintf(endingDetailFormat, tail, promo))

	return fmt.Sprintf("финал: при отборе вырезан конец последнего раздела — слова %s «%s», а после призыва к "+
		"зрителю оставлено представление — слова %s «%s». Доказательство или история в последнем пункте — "+
		"кульминация, ради неё досматривают до конца: не вырезай её, а для длины вырежи представление и "+
		"«подписывайся» после призыва (если тренер не просила их оставить)", tail,
		truncate(quote(words, issue.tail), maxDroppedRunes), promo, truncate(quote(words, issue.promo),
			maxDroppedRunes))
}

// findEndingIssue finds a selected first version whose last section lost its end and whose introduction after the call
// stayed; a revision is the trainer's own request and is not judged.
func findEndingIssue(req *Request, in *Input, edit *roughcut.Edit, sel *selection) (endingIssue, bool) {
	words := req.Transcript.Words

	first, ok := lastSectionWord(words, edit.ScreenNotes)
	if !sel.on || req.IsRevision() || !ok {
		return endingIssue{}, false
	}

	kept := keptSet(words, sel.keep)

	call := nextKept(words, kept, first, func(i int) bool {
		return strings.HasPrefix(normalizeWord(words[i].Word), callStem)
	})
	if call < 0 {
		return endingIssue{}, false
	}

	promo := nextKept(words, kept, call+1, func(i int) bool { return isIntroduction(words, i) })
	if promo < 0 {
		return endingIssue{}, false
	}

	tail, ok := cutTail(in.Remove, kept, first, call)
	if !ok {
		return endingIssue{}, false
	}

	last := promo
	for last+1 < len(words) && kept[last+1] && last+1-promo < maxPromoWords {
		last++
	}

	return endingIssue{tail: tail, promo: roughcut.WordRange{From: promo, To: last}}, true
}

// lastSectionWord is the first word of the last section heading of the edit (the first word starting at it or later);
// false without a section.
func lastSectionWord(words []transcription.Word, notes []roughcut.ScreenNote) (int, bool) {
	start, found := 0.0, false

	for _, n := range notes {
		if n.Kind == roughcut.SectionNote && (!found || n.Start > start) {
			start, found = n.Start, true
		}
	}

	if !found {
		return 0, false
	}

	for i, w := range words {
		if w.Start >= start-segmentSlack {
			return i, true
		}
	}

	return 0, false
}

// nextKept is the first word from from on that is left in the reel and matches; -1 without one.
func nextKept(words []transcription.Word, kept []bool, from int, match func(i int) bool) int {
	for i := max(from, 0); i < len(words); i++ {
		if kept[i] && match(i) {
			return i
		}
	}

	return -1
}

// isIntroduction: «подписывайся», «подписывайтесь» or «меня зовут».
func isIntroduction(words []transcription.Word, i int) bool {
	word := normalizeWord(words[i].Word)

	return strings.HasPrefix(word, subscribeStem) ||
		(word == introWord && i+1 < len(words) && normalizeWord(words[i+1].Word) == introNameWord)
}

// cutTail is the run of words the decision cut (remove) right before the call (up to callLeadWords kept words of the
// question and the call between them), within the last section (from first on); false when it is shorter than
// minClimaxWords. The words of cleanup_words are not counted on purpose: they are failed takes, links and reactions
// the model cleaned, not the climax cut for the length.
func cutTail(remove []roughcut.WordRange, kept []bool, first, call int) (roughcut.WordRange, bool) {
	end := call - 1
	for lead := 0; end >= first && kept[end] && lead < callLeadWords; lead++ {
		end--
	}

	from := end + 1
	for from > first && !kept[from-1] && inRanges(remove, from-1) {
		from--
	}

	if end-from+1 < minClimaxWords {
		return roughcut.WordRange{}, false
	}

	return roughcut.WordRange{From: from, To: end}, true
}

// inRanges: the word i is in one of the ranges.
func inRanges(ranges []roughcut.WordRange, i int) bool {
	for _, r := range ranges {
		if i >= r.From && i <= r.To {
			return true
		}
	}

	return false
}
