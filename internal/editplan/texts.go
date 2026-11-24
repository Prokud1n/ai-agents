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
	maxQuoteRunes    = 60
	tenths           = 10
	secondsPerMinute = 60
	float64Bits      = 64
	// sentenceMarks closing a quoted phrase are dropped: «…ровно».
	sentenceMarks = ".!?,;:"
)

// Lines about the mode and the sound.
const (
	workoutModeLine = "Смонтировал как тренировку: паузы и молчаливые отрезки не вырезал."
	talkModeLine    = "Смонтировал как разговорный ролик: вырезал паузы, оговорки и неудачные дубли."
	muteLine        = "Убрал звук исходника и субтитры речи — трек добавьте в Instagram."
	unmuteLine      = "Вернул звук исходника и субтитры речи."
)

// describeChanges are the sentences for the trainer about what this decision changed: the mode, the trim, the
// fragments cut and brought back, the takes chosen (a revision; the first version counts the repeats itself), the
// corrected words, the exercise titles (a revision; the first version lists them all) and the sound. No indices, times
// of the source or technical words: times are those of the timeline the trainer gave (the reel of a revision).
func describeChanges(req *Request, in *Input, trimmed *trim, placed *placedTitles) []string {
	words := req.Transcript.Words
	lines := make([]string, 0, maxChangeLines)
	previous := previousEdit(req.Plan)

	if mode := nextMode(req, in); req.IsRevision() && mode != previous.Mode.Effective() {
		lines = append(lines, map[roughcut.Mode]string{
			roughcut.WorkoutMode: workoutModeLine, roughcut.TalkMode: talkModeLine,
		}[mode])
	}

	if trimmed.start > 0 {
		lines = append(lines, "Обрезал начало: убрал "+secondsText(trimmed.start, "первую секунду", "первые")+".")
	}

	if trimmed.end > 0 {
		lines = append(lines, "Обрезал конец: убрал "+secondsText(trimmed.end, "последнюю секунду", "последние")+".")
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
			currentWord(req, f.Index), strings.TrimSpace(f.Text)))
	}

	for _, r := range in.Hide {
		lines = append(lines, "Убрал из субтитров «"+quote(words, r)+"».")
	}

	lines = append(lines, timeCutLines(&previous, in)...)

	if req.IsRevision() {
		lines = append(lines, titleChangeLines(&previous, in, placed)...)
	}

	if in.Mute != nil && *in.Mute != previous.Mute {
		lines = append(lines, map[bool]string{true: muteLine, false: unmuteLine}[*in.Mute])
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

// secondsText: «первую секунду» for a second, otherwise «первые 2,5 с» (tenths, at least 0,1 с).
func secondsText(seconds float64, one, many string) string {
	rounded := max(math.Round(seconds*tenths)/tenths, 1.0/tenths)
	if int(math.Round(rounded*tenths)) == tenths {
		return one
	}

	return many + " " + strings.Replace(strconv.FormatFloat(rounded, 'f', -1, float64Bits), ".", ",", 1) + " с"
}

// placedTitles are the titles this decision added and changed, with the kept intervals of the new reel.
type placedTitles struct {
	*newTitles

	keep []roughcut.Interval
}

// reelClock is when the title starts in the new reel.
func (p *placedTitles) reelClock(t roughcut.Title) string {
	return clock(roughcut.ReelTime(p.keep, t.Start))
}

// timeCutLines: «Вырезал отрезок 0:15–0:20.» for every cut by time («Вырезал 0,5 с на 0:15.» for one shorter than a
// second), «Вернул вырезанный ранее отрезок (2,5 с).» for every brought back one.
func timeCutLines(previous *roughcut.Edit, in *Input) []string {
	lines := make([]string, 0, len(in.CutSeconds)+len(in.RestoreCuts))

	for _, r := range in.CutSeconds {
		if r.To-r.From < 1 {
			lines = append(lines, fmt.Sprintf("Вырезал %s на %s.", durationText(r.To-r.From), clock(r.From)))

			continue
		}

		lines = append(lines, fmt.Sprintf("Вырезал отрезок %s–%s.", clock(r.From), clock(r.To)))
	}

	for _, i := range in.RestoreCuts {
		iv := previous.CutTime[i]
		lines = append(lines, "Вернул вырезанный ранее отрезок ("+durationText(iv.End-iv.Start)+").")
	}

	return lines
}

// titleChangeLines describe the changes of the exercise titles of a revision; times are on the new reel.
func titleChangeLines(previous *roughcut.Edit, in *Input, placed *placedTitles) []string {
	lines := make([]string, 0, len(in.AddTitles)+len(in.ChangeTitles)+len(in.RemoveTitles))

	for _, c := range in.ChangeTitles {
		old, changed := previous.Titles[c.Index], placed.changed[c.Index]

		if changed.Text != old.Text {
			lines = append(lines, fmt.Sprintf("Переименовал подпись «%s» → «%s».", old.Text, changed.Text))
		}

		if c.Word != nil || c.Start != nil || c.End != nil {
			lines = append(lines, fmt.Sprintf("Сдвинул подпись «%s»: теперь с %s.", changed.Text, placed.reelClock(changed)))
		}
	}

	for _, i := range in.RemoveTitles {
		lines = append(lines, "Убрал подпись «"+previous.Titles[i].Text+"».")
	}

	for _, t := range placed.added {
		lines = append(lines, fmt.Sprintf("Добавил подпись «%s» с %s.", t.Text, placed.reelClock(t)))
	}

	return lines
}

// clock formats seconds as m:ss.
func clock(seconds float64) string {
	total := int(math.Round(max(seconds, 0)))

	return fmt.Sprintf("%d:%02d", total/secondsPerMinute, total%secondsPerMinute)
}

// durationText: «2,5 с» (tenths, at least 0,1 с).
func durationText(seconds float64) string {
	rounded := max(math.Round(seconds*tenths)/tenths, 1.0/tenths)

	return strings.Replace(strconv.FormatFloat(rounded, 'f', -1, float64Bits), ".", ",", 1) + " с"
}
