package editplan

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

const (
	// maxScreenNotes of a reel: a list of a few items and a few callouts (usually 0–6).
	maxScreenNotes = 12
	// maxNoteRunes of a screen note: a short item («1/ на русском»), not a sentence; the subtitles wrap it into two
	// lines at most.
	maxNoteRunes = 40
	// maxSectionRunes of a section heading: «страх 1», «ошибка 2» — one short line.
	maxSectionRunes = 24
)

// Lines about the screen notes in the description of a draft.
const (
	notesRemovedLine = "Убрал пункты и цифры с экрана."
	notesLinePrefix  = "На экране: "
)

// NoteInput is a point or a number on the screen (screen_notes): a list title, an item of the list or a callout, from
// the word Word (where the trainer says it) or Start seconds of the source, until End seconds of the source (optional).
type NoteInput struct {
	Kind  roughcut.NoteKind `json:"kind"`
	Text  string            `json:"text"`
	Word  *int              `json:"word,omitempty"`
	Start *float64          `json:"start_seconds,omitempty"`
	End   *float64          `json:"end_seconds,omitempty"`
}

// checkNotesInput checks the screen notes of the decision: at most maxScreenNotes, each of a known kind, a short text
// on one line without markup, emoji or hashtags, a start (a word or seconds of the source) and an end after it within
// the source.
func checkNotesInput(req *Request, in *Input) error {
	if in.ScreenNotes == nil {
		return nil
	}

	notes := *in.ScreenNotes
	if len(notes) > maxScreenNotes {
		return checkFailedf("слишком много элементов на экране: не больше %d в screen_notes (обычно 0–6)", maxScreenNotes)
	}

	for i := range notes {
		if err := checkNote(req, &notes[i]); err != nil {
			return err
		}
	}

	return nil
}

// checkNote checks one screen note.
func checkNote(req *Request, n *NoteInput) error {
	text := normalizeHook(n.Text)

	limit := maxNoteRunes
	if n.Kind == roughcut.SectionNote {
		limit = maxSectionRunes
	}

	switch {
	case noteKindNames[n.Kind] == "":
		return checkFailedf("screen_notes: kind — section, list_title, list_item или callout, а не %q", n.Kind)
	case strings.ContainsFunc(n.Text, forbiddenRune):
		return checkFailedf("элемент на экране «%s»: без переносов строк, невидимых символов и { } \\ < >",
			truncate(oneLine(n.Text), maxNoteRunes))
	case text == "" || utf8.RuneCountInString(text) > limit:
		return checkFailedf("элемент на экране «%s»: от 1 до %d символов", truncate(text, limit), limit)
	case strings.ContainsFunc(text, emojiRune) || strings.Contains(text, "#"):
		return checkFailedf("элемент на экране «%s»: без эмодзи и хештегов", text)
	case (n.Word == nil) == (n.Start == nil):
		return checkFailedf("элемент на экране «%s»: укажи начало или словом (word), или секундами исходника "+
			"(start_seconds)", text)
	}

	return checkNoteTime(req, n, text)
}

// checkNoteTime: the word exists, the seconds are within the source, the end is after the start.
func checkNoteTime(req *Request, n *NoteInput, text string) error {
	total := req.Plan.SourceDuration

	if n.Word != nil && (*n.Word < 0 || *n.Word >= len(req.Transcript.Words)) {
		return checkFailedf("элемент на экране «%s»: слова с индексом %d нет в расшифровке", text, *n.Word)
	}

	if n.Start != nil && (!validSeconds(*n.Start) || *n.Start >= total) {
		return checkFailedf("элемент на экране «%s»: start_seconds от 0 до %.1f с исходника", text, total)
	}

	if n.End != nil && (!validSeconds(*n.End) || *n.End > total || *n.End <= noteStart(req, n)) {
		return checkFailedf("элемент на экране «%s»: end_seconds позже начала и не дальше %.1f с исходника", text,
			total)
	}

	return nil
}

// noteStart is the start of a checked note in seconds of the source.
func noteStart(req *Request, n *NoteInput) float64 {
	if n.Word != nil {
		return req.Transcript.Words[*n.Word].Start
	}

	return *n.Start
}

// screenNotesFor are the screen notes of the new version: the decision's (it replaces the whole list; empty removes
// them), or the previous version's. A workout has none: the decision's are refused, the previous ones dropped.
func screenNotesFor(req *Request, in *Input, mode roughcut.Mode, previous *roughcut.Edit) ([]roughcut.ScreenNote, error) {
	if mode == roughcut.WorkoutMode {
		if in.ScreenNotes != nil && len(*in.ScreenNotes) > 0 {
			return nil, checkFailedf("пункты и цифры на экране (screen_notes) — только в разговорном ролике (talk): " +
				"в тренировке подписывай упражнения (add_titles)")
		}

		return nil, nil
	}

	return nextScreenNotes(req, in, previous), nil
}

// nextScreenNotes are the notes of the decision as the edit keeps them (ordered by time, on the source), or the
// previous version's; nil without notes.
func nextScreenNotes(req *Request, in *Input, previous *roughcut.Edit) []roughcut.ScreenNote {
	if in.ScreenNotes == nil {
		return slices.Clone(previous.ScreenNotes)
	}

	notes := make([]roughcut.ScreenNote, 0, len(*in.ScreenNotes))

	for i := range *in.ScreenNotes {
		n := &(*in.ScreenNotes)[i]
		note := roughcut.ScreenNote{Kind: n.Kind, Text: normalizeHook(n.Text), Start: roundSeconds(noteStart(req, n))}

		if n.End != nil {
			note.End = roundSeconds(*n.End)
		}

		notes = append(notes, note)
	}

	if len(notes) == 0 {
		return nil
	}

	slices.SortStableFunc(notes, func(a, b roughcut.ScreenNote) int { return cmp.Compare(a.Start, b.Start) })

	return notes
}

// checkNotesShown: every note the decision set is seen in the reel built with the edit (its start is not cut). The
// notes of the previous versions are not checked: a revision that cuts the words of one drops it from the reel.
func checkNotesShown(in *Input, edit *roughcut.Edit, plan *roughcut.Plan) error {
	if in.ScreenNotes == nil {
		return nil
	}

	if hidden := len(edit.ScreenNotes) - len(roughcut.RemapScreenNotes(edit.ScreenNotes, plan.Keep)); hidden > 0 {
		return checkFailedf("элементов на экране не видно в ролике: %d (их начало вырезано) — поставь их на слова, "+
			"которые остались в ролике", hidden)
	}

	return nil
}

// screenNotesLines: «На экране: 3 пункта и 2 выноски.» when the decision sets the notes (a first version with them, a
// revision that changes them), «Убрал пункты и цифры с экрана.» when it removes them (or makes the reel a workout).
func screenNotesLines(req *Request, in *Input, previous *roughcut.Edit) []string {
	if nextMode(req, in) == roughcut.WorkoutMode {
		// A workout has no notes: a revision to a workout removes them.
		if len(previous.ScreenNotes) > 0 {
			return []string{notesRemovedLine}
		}

		return nil
	}

	if in.ScreenNotes == nil {
		return nil
	}

	notes := nextScreenNotes(req, in, previous)

	switch {
	case slices.Equal(notes, previous.ScreenNotes):
		return nil
	case len(notes) == 0:
		return []string{notesRemovedLine}
	default:
		return []string{notesLine(notes)}
	}
}

// notesLine says what is on the screen: «На экране: 3 раздела.», «На экране: 3 пункта и 2 выноски.»; a list without
// items is «заголовок списка» (one) or «2 заголовка списка».
func notesLine(notes []roughcut.ScreenNote) string {
	counts := map[roughcut.NoteKind]int{}
	for _, n := range notes {
		counts[n.Kind]++
	}

	parts := make([]string, 0, len(counts))

	if n := counts[roughcut.SectionNote]; n > 0 {
		parts = append(parts, plural(n, "раздел", "раздела", "разделов"))
	}

	if n := counts[roughcut.ListItemNote]; n > 0 {
		parts = append(parts, plural(n, "пункт", "пункта", "пунктов"))
	}

	if n := counts[roughcut.CalloutNote]; n > 0 {
		parts = append(parts, plural(n, "выноска", "выноски", "выносок"))
	}

	switch titles := counts[roughcut.ListTitleNote]; {
	case len(parts) > 0:
	case titles == 1:
		parts = append(parts, "заголовок списка")
	default:
		parts = append(parts, plural(titles, "заголовок списка", "заголовка списка", "заголовков списков"))
	}

	return notesLinePrefix + strings.Join(parts, " и ") + "."
}

// Russian plural forms by the last digits of the number.
const (
	pluralTens    = 10
	pluralHundred = 100
	teenFrom      = 11
	teenTo        = 14
	fewTo         = 4
)

// plural: «1 пункт», «3 пункта», «5 пунктов».
func plural(n int, one, few, many string) string {
	last, lastTwo := n%pluralTens, n%pluralHundred

	switch {
	case lastTwo >= teenFrom && lastTwo <= teenTo:
		return fmt.Sprintf("%d %s", n, many)
	case last == 1:
		return fmt.Sprintf("%d %s", n, one)
	case last >= 2 && last <= fewTo:
		return fmt.Sprintf("%d %s", n, few)
	default:
		return fmt.Sprintf("%d %s", n, many)
	}
}

// noteKindNames are the kinds of the screen notes as the model sees them.
var noteKindNames = map[roughcut.NoteKind]string{
	roughcut.ListTitleNote: "заголовок списка",
	roughcut.ListItemNote:  "пункт",
	roughcut.CalloutNote:   "выноска",
	roughcut.SectionNote:   "раздел",
}

// writeScreenNotes says the screen notes of the current version (a talking head) and what to do with them: change or
// remove them by the revision, propose them for the first version.
func writeScreenNotes(b *strings.Builder, req *Request) {
	previous := previousEdit(req.Plan)
	if previous.Mode == roughcut.WorkoutMode {
		return
	}

	switch {
	case len(previous.ScreenNotes) > 0:
		b.WriteString("\nНа экране сейчас (screen_notes заменяет весь список: чтобы поменять, добавить или убрать " +
			"элемент, перечисли все, которые остаются; пустой массив убирает все; не просят — не указывай):\n")

		for _, n := range previous.ScreenNotes {
			fmt.Fprintf(b, "- %s «%s», с %s исходника%s\n", noteKindNames[n.Kind], n.Text,
				roughcut.FormatTimecode(n.Start), notePlace(n, req.Plan.Keep))
		}
	case req.IsRevision():
		b.WriteString("\nПунктов и цифр на экране нет. Добавь их через screen_notes, только если тренер просит.\n")
	default:
		b.WriteString("\nПункты и цифры на экране: если тренер перечисляет («во-первых…», «три причины…») или " +
			"называет цифры, сроки, неожиданный факт — вынеси их через screen_notes по разделу «Пункты на экране»: " +
			"переход к новому пункту — заголовок раздела (section), цифры — выноски; выносить нечего — не указывай.")

		if !req.HookOnly() {
			b.WriteString(readingsCut)
		}

		b.WriteString("\n")
	}
}

// readingsCut: the spoken heading of a section goes from the sound, the plate replaces it (T3.22).
const readingsCut = " Произнесённое чтение заголовка раздела («страх номер два», «пункт первый») вырежи через " +
	"cleanup_words (при чистке и отборе) или remove_words, а вопрос или тезис раздела оставь: раздел покажется с того " +
	"места, где продолжается речь."

// notePlace is where the note starts in the current reel.
func notePlace(n roughcut.ScreenNote, keep []roughcut.Interval) string {
	shown := roughcut.RemapScreenNotes([]roughcut.ScreenNote{n}, keep)
	if len(shown) == 0 {
		return " (вырезано, в ролике не видно)"
	}

	return " (в ролике " + roughcut.FormatTimecode(shown[0].Start) + ")"
}
