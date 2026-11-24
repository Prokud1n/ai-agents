package editplan

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	// maxChangeLines in the description of a draft; the rest is counted.
	maxChangeLines = 6
	// maxQuoteRunes of a quoted fragment.
	maxQuoteRunes = 60
	tenths        = 10
	float64Bits   = 64
	// sentenceMarks closing a quoted phrase are dropped: «…ровно».
	sentenceMarks = ".!?,;:"
)

// describeChanges are the sentences for the trainer about what this decision changed: the trim, the fragments cut
// and brought back, the takes chosen (a revision; the first version counts the repeats itself) and the corrected
// words. No indices, times of the source or technical words.
func describeChanges(req *Request, in *Input, trimmed []roughcut.WordRange) []string {
	words := req.Transcript.Words
	lines := make([]string, 0, maxChangeLines)

	if in.TrimStart > 0 && len(trimmed) > 0 {
		lines = append(lines, "Обрезал начало: убрал первые "+secondsText(in.TrimStart)+".")
	}

	if in.TrimEnd > 0 && len(trimmed) > 0 {
		lines = append(lines, "Обрезал конец: убрал последние "+secondsText(in.TrimEnd)+".")
	}

	for _, r := range in.Remove {
		lines = append(lines, "Вырезал фрагмент «"+quote(words, r)+"».")
	}

	for _, r := range in.Restore {
		lines = append(lines, "Вернул фрагмент «"+quote(words, r)+"».")
	}

	if req.IsRevision() {
		lines = append(lines, takeLines(req.Plan, in.Candidates)...)
	}

	for _, f := range in.FixWords {
		lines = append(lines, fmt.Sprintf("Исправил в субтитрах: «%s» → «%s».",
			strings.TrimSpace(words[f.Index].Word), strings.TrimSpace(f.Text)))
	}

	if len(lines) > maxChangeLines {
		rest := len(lines) - maxChangeLines + 1
		lines = append(lines[:maxChangeLines-1], fmt.Sprintf("И ещё изменений: %d.", rest))
	}

	return lines
}

// takeLines: «Оставил второй дубль фразы «…».» for every disputed repeat cut.
func takeLines(plan *roughcut.Plan, decisions []CandidateDecision) []string {
	var lines []string

	for _, d := range decisions {
		if !d.Cut {
			continue
		}

		for i := range plan.Candidates {
			if c := &plan.Candidates[i]; c.ID == d.ID {
				phrase := strings.TrimRight(strings.TrimSpace(c.Later.Text), sentenceMarks)
				lines = append(lines, "Оставил второй дубль фразы «"+truncate(phrase, maxQuoteRunes)+"».")
			}
		}
	}

	return lines
}

// quote is the text of the words of the range, cut to maxQuoteRunes.
func quote(words []transcription.Word, r roughcut.WordRange) string {
	parts := make([]string, 0, r.To-r.From+1)
	for _, w := range words[r.From : r.To+1] {
		parts = append(parts, strings.TrimSpace(w.Word))
	}

	return truncate(strings.Join(parts, " "), maxQuoteRunes)
}

// secondsText: «3 с», «2,5 с».
func secondsText(seconds float64) string {
	rounded := math.Round(seconds*tenths) / tenths

	return strings.Replace(strconv.FormatFloat(rounded, 'f', -1, float64Bits), ".", ",", 1) + " с"
}
