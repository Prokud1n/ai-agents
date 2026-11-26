package roughcut

import (
	"fmt"
	"math"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	brokenTailWords = 4 // words of a broken-off take quoted in the log
	secondsPerMin   = 60
	tenthsPerSecond = 10
	percent         = 100
)

// phrases gives the words of a transcript with their segment-text tokens (with punctuation) and the
// content tokens retakes were found on.
type phrases struct {
	words  []transcription.Word
	texts  []string
	tokens []token
}

// span is the phrase of words[from..to], both included, with the punctuation of the segment text when the
// words could be aligned to it.
func (ph *phrases) span(from, to int) Span {
	text := joinTokens(ph.texts[from : to+1])
	if text == "" {
		text = joinWords(ph.words[from : to+1])
	}

	return Span{Start: roundTime(ph.words[from].Start), End: roundTime(ph.words[to].End), Text: text}
}

// attempt and kept are the word ranges of a retake: the removed attempt and the kept phrase.
func (ph *phrases) attempt(r *retake) (from, to int) {
	return ph.tokens[r.from].idx, ph.tokens[r.to-1].idx
}

func (ph *phrases) kept(r *retake) Span {
	words := ph.keptWords(r)

	return ph.span(words.From, words.To)
}

func (ph *phrases) keptWords(r *retake) WordRange {
	return WordRange{From: ph.tokens[r.to].idx, To: ph.tokens[r.keptTo-1].idx}
}

// joinTokens joins aligned tokens, or returns "" if any of them is unknown.
func joinTokens(tokens []string) string {
	for _, t := range tokens {
		if t == "" {
			return ""
		}
	}

	return strings.Join(tokens, " ")
}

// describeTakes turns detected retakes into decisions with an explanation in Russian. Restarts that follow
// one another form a series: its takes are numbered, and every removed take points to the last one,
// which is kept.
func describeTakes(ph *phrases, retakes []retake) []Take {
	numbers := make([]int, len(retakes))
	for i, r := range retakes {
		numbers[i] = 1
		if i > 0 && retakes[i-1].to == r.from {
			numbers[i] = numbers[i-1] + 1
		}
	}

	takes := make([]Take, 0, len(retakes))

	for i := range retakes {
		last := i
		for last+1 < len(retakes) && retakes[last+1].from == retakes[last].to {
			last++
		}

		from, to := ph.attempt(&retakes[i])
		take := Take{
			Kind:       retakes[i].kind,
			Number:     numbers[i],
			KeptNumber: numbers[last] + 1,
			Removed:    ph.span(from, to),
			Kept:       ph.kept(&retakes[last]),
			Similarity: math.Round(retakes[i].similarity*percent) / percent,
		}
		take.Explanation = explainTake(&take, ph.words, from, to)
		takes = append(takes, take)
	}

	return takes
}

// describeAccepted records the candidates the editor chose to cut as takes.
func describeAccepted(ph *phrases, accepted []candidate) []Take {
	takes := make([]Take, 0, len(accepted))

	for i := range accepted {
		from, to := ph.attempt(&accepted[i].retake)
		take := Take{
			Kind:       AcceptedTakeKind,
			Number:     1,
			KeptNumber: 2,
			Removed:    ph.span(from, to),
			Kept:       ph.kept(&accepted[i].retake),
			Similarity: math.Round(accepted[i].similarity*percent) / percent,
		}
		take.Explanation = fmt.Sprintf("Фраза (%s–%s) «%s» вырезана по решению монтажёра (спорный повтор %d), "+
			"оставлена (%s–%s) «%s».", FormatTimecode(take.Removed.Start), FormatTimecode(take.Removed.End),
			quote(take.Removed.Text), accepted[i].id, FormatTimecode(take.Kept.Start), FormatTimecode(take.Kept.End),
			quote(take.Kept.Text))
		takes = append(takes, take)
	}

	return takes
}

// describeCandidates explains the repeats left in the cut. Each gets the interval that accepting it
// removes (cutFor) and the indices of its words, so the editor only chooses.
func describeCandidates(ph *phrases, candidates []candidate, cutFor func(*candidate) Interval) []Candidate {
	out := make([]Candidate, 0, len(candidates))

	for i := range candidates {
		c := &candidates[i]
		from, to := ph.attempt(&c.retake)
		cand := Candidate{
			ID:           c.id,
			Reason:       c.reason,
			Earlier:      ph.span(from, to),
			EarlierWords: WordRange{From: from, To: to},
			LaterWords:   ph.keptWords(&c.retake),
			Later:        ph.kept(&c.retake),
			Cut:          cutFor(c),
			Similarity:   math.Round(c.similarity*percent) / percent,
		}

		why := "законченная фраза сказана ещё раз"
		if c.reason == RhythmCandidateReason {
			why = "похоже на ритм или перечисление"
		}

		cand.Explanation = fmt.Sprintf("Повтор %d: фраза (%s–%s) «%s» повторена (%s–%s) «%s», совпало %d%% слов: "+
			"%s, оставлены обе.", c.id,
			FormatTimecode(cand.Earlier.Start), FormatTimecode(cand.Earlier.End), quote(cand.Earlier.Text),
			FormatTimecode(cand.Later.Start), FormatTimecode(cand.Later.End), quote(cand.Later.Text),
			int(math.Round(cand.Similarity*percent)), why)
		out = append(out, cand)
	}

	return out
}

func explainTake(t *Take, words []transcription.Word, from, to int) string {
	keptPart := fmt.Sprintf("оставлен дубль %d (%s–%s) «%s»",
		t.KeptNumber, FormatTimecode(t.Kept.Start), FormatTimecode(t.Kept.End), quote(t.Kept.Text))

	if t.Kind == IncompleteTakeKind {
		tail := joinWords(words[max(from, to-brokenTailWords+1) : to+1])

		return fmt.Sprintf("Дубль %d (%s–%s) оборван на «%s…», %s.",
			t.Number, FormatTimecode(t.Removed.Start), FormatTimecode(t.Removed.End), tail, keptPart)
	}

	return fmt.Sprintf("Дубль %d (%s–%s) «%s» начат заново (повторено %d%% слов), %s.",
		t.Number, FormatTimecode(t.Removed.Start), FormatTimecode(t.Removed.End), quote(t.Removed.Text),
		int(math.Round(t.Similarity*percent)), keptPart)
}

// quote prepares a phrase for «…» inside a sentence of the log: a closing comma or sentence mark is dropped
// ("приступим." -> "приступим"), an ellipsis stays.
func quote(text string) string {
	if strings.HasSuffix(text, ellipsis) || strings.HasSuffix(text, threeDots) {
		return text
	}

	return strings.TrimRight(text, ".!?,;:")
}

// FormatTimecode formats seconds as m:ss.d ("0:12.4", "3:05.0").
func FormatTimecode(seconds float64) string {
	tenths := int(math.Round(max(seconds, 0) * tenthsPerSecond))
	minutes := tenths / (secondsPerMin * tenthsPerSecond)
	rest := float64(tenths-minutes*secondsPerMin*tenthsPerSecond) / tenthsPerSecond

	return fmt.Sprintf("%d:%04.1f", minutes, rest)
}

// DecisionsLog is the human-readable log of a plan in Russian: totals, every failed take with the reason
// it was removed, fillers and pauses.
func DecisionsLog(p *Plan) string {
	var b strings.Builder

	source := p.Source
	if source == "" {
		source = "исходник"
	}

	fmt.Fprintf(&b, "Черновая склейка: %s\n", source)
	fmt.Fprintf(&b, "Исходник %s → результат %s (вырезано %.1f с), фрагментов: %d\n",
		FormatTimecode(p.SourceDuration), FormatTimecode(p.Duration), p.SourceDuration-p.Duration, len(p.Keep))
	fmt.Fprintf(&b, "Порог паузы %.2f с, поля вокруг слов %.2f/%.2f с, сходство дублей %.0f%%\n\n",
		p.Settings.MaxPause, p.Settings.PadBefore, p.Settings.PadAfter, p.Settings.RetakeSimilarity*percent)

	if p.Mode == WorkoutMode {
		b.WriteString("Режим: тренировка — паузы, паразиты и неудачные дубли не вырезаются, только по решению монтажёра\n")
	}

	fmt.Fprintf(&b, "Дубли: %d\n", len(p.Takes))

	for _, t := range p.Takes {
		fmt.Fprintf(&b, "- %s\n", t.Explanation)
	}

	fmt.Fprintf(&b, "\nСпорные повторы (не вырезаны, решает монтажёр): %d\n", len(p.Candidates))

	for i := range p.Candidates {
		fmt.Fprintf(&b, "- %s\n", p.Candidates[i].Explanation)
	}

	writeCuts(&b, p.Cuts, FillerCutReason, "Слова-паразиты")
	writeCuts(&b, p.Cuts, PauseCutReason, "Паузы")

	if p.Edit != nil {
		writeCuts(&b, p.Cuts, EditCutReason, "Вырезано по правкам")
		writeEditExtras(&b, p.Edit)
		fmt.Fprintf(&b, "\nРешения монтажёра: %d\n", len(p.Edit.Notes))

		for _, note := range p.Edit.Notes {
			fmt.Fprintf(&b, "- %s\n", note)
		}

		if len(p.Edit.Details) > 0 {
			b.WriteString("\nПодробности:\n")

			for _, line := range p.Edit.Details {
				fmt.Fprintf(&b, "- %s\n", line)
			}
		}
	}

	return b.String()
}

func writeCuts(b *strings.Builder, cuts []Cut, reason CutReason, title string) {
	count, total := 0, 0.0

	for _, c := range cuts {
		if c.Reason == reason {
			count++
			total += c.End - c.Start
		}
	}

	fmt.Fprintf(b, "\n%s: %d (%.1f с)\n", title, count, total)

	for _, c := range cuts {
		if c.Reason != reason {
			continue
		}

		if c.Text != "" {
			fmt.Fprintf(b, "- %s–%s «%s»\n", FormatTimecode(c.Start), FormatTimecode(c.End), c.Text)
		} else {
			fmt.Fprintf(b, "- %s–%s (%.2f с)\n", FormatTimecode(c.Start), FormatTimecode(c.End), c.End-c.Start)
		}
	}
}

// writeEditExtras logs the sound, the hook title and the exercise titles of the edit.
func writeEditExtras(b *strings.Builder, edit *Edit) {
	if edit.Hook != "" {
		fmt.Fprintf(b, "\nЗаголовок-хук: «%s»\n", edit.Hook)
	}

	writeScreenNotes(b, edit.ScreenNotes)

	if edit.Mute {
		b.WriteString("\nЗвук исходника убран, субтитров речи нет\n")
	}

	for _, r := range edit.Hide {
		fmt.Fprintf(b, "\nСкрыты в субтитрах (видео и звук не тронуты): слова %d–%d\n", r.From, r.To)
	}

	if len(edit.Titles) == 0 {
		return
	}

	fmt.Fprintf(b, "\nПодписи упражнений: %d\n", len(edit.Titles))

	for _, t := range edit.Titles {
		end := "до следующей подписи"
		if t.End > t.Start {
			end = "до " + FormatTimecode(t.End)
		}

		fmt.Fprintf(b, "- с %s %s «%s»\n", FormatTimecode(t.Start), end, t.Text)
	}
}

// noteKindNames are the kinds of the screen notes in the decisions log.
var noteKindNames = map[NoteKind]string{
	ListTitleNote: "заголовок списка",
	ListItemNote:  "пункт",
	CalloutNote:   "выноска",
}

// writeScreenNotes logs the points and numbers on the screen, on the source timeline.
func writeScreenNotes(b *strings.Builder, notes []ScreenNote) {
	if len(notes) == 0 {
		return
	}

	fmt.Fprintf(b, "\nНа экране: %d\n", len(notes))

	for _, n := range notes {
		fmt.Fprintf(b, "- с %s, %s «%s»\n", FormatTimecode(n.Start), noteKindNames[n.Kind], n.Text)
	}
}
