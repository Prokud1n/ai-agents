package editplan

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// Screen notes whose start the decision cut (T3.29). The worker moves a note that starts in a cut piece to the next
// kept one within its reach (roughcut.RemapScreenNotes: a section up to 3 s, another note less), the app checks it with
// that reach shortened by roughcut.NoteCheckSlack, because the worker cuts by the sound and the app by the margins.
//
// A note the app check does not let through, but which the worker's own reach shows, is moved here onto the first kept
// word after it: then both sides see it on a kept word (the worker's piece starts at that word's sound, a note on a
// word's start moves with its trimmed start — roughcut.notesOnTrimmed), so the app and the worker cannot disagree about
// it. A note is never moved further than the worker would move it: what lies beyond its reach is not what the note
// named (the model put it on a failed take of the reading or on a point cut from the reel), so that note goes back to
// the model with where it stands, what is cut around it and the nearest kept word.
//
// On the last attempt a decision that fails only by such notes is accepted (Session, Input.dropHidden): a section is
// put on the first kept word after it — the start of the question of its point, a plate there is better than none —
// unless another note starts between them or a section already stands there (or the notes would then be at one place,
// checkNotesApart); the other notes, and a section with no place, are dropped with a line for the trainer.
const (
	// maxHiddenNamed notes are described in the check message; the rest are counted.
	maxHiddenNamed = 3
	// maxHiddenWhereRunes of the description of each note in the check message (where it stands and what is cut);
	// what to do is never cut, so the message stays about 1200 characters at most.
	maxHiddenWhereRunes = 250
)

// Texts about the notes left out on the last attempt.
const (
	hiddenOneFormat = "Плашку %s не поставил: место, где она начиналась, не вошло в ролик. Если она нужна — напишите в " +
		"правке, к какой фразе её поставить."
	hiddenManyFormat = "Плашки %s не поставил: места, где они начинались, не вошли в ролик. Если они нужны — напишите в " +
		"правке, к каким фразам их поставить."
	hiddenDetailFormat    = "Элемент на экране не показан, его начало вырезано: %s «%s» с %s исходника"
	relocatedDetailFormat = "Раздел «%s» поставлен на слово %d «%s» (%s исходника): его начало (%s) вырезано"
	hiddenAcceptedText    = "Принято: элементы на экране, начало которых вырезано, поставлены на ближайшее оставленное " +
		"слово или убраны."
)

// hiddenNote is a note of the decision the reel would not show: it stands on the word word (-1: after the speech), the
// words cut (From > To: none) lie from it to next, the first word kept after it (-1: none).
type hiddenNote struct {
	note       roughcut.ScreenNote
	word, next int
	cut        roughcut.WordRange
}

// placement is the notes of the decision as the reel shows them (notes), the ones it would not show (hidden); on the
// last attempt the hidden ones left out (dropped) and the lines of the decisions log about them.
type placement struct {
	notes   []roughcut.ScreenNote
	hidden  []hiddenNote
	dropped []hiddenNote
	details []string
}

// placeNotes places the notes of the decision: a note the app check hides but the worker's reach shows moved onto the
// first kept word after it; the notes it does not show are hidden (and, on the last attempt, put or dropped:
// settleHidden). The notes of the previous versions (the decision names none) are not checked: a revision that cuts
// the words of one drops it from the reel.
func placeNotes(req *Request, in *Input, notes []roughcut.ScreenNote, plan *roughcut.Plan) placement {
	if in.ScreenNotes == nil || len(notes) == 0 {
		return placement{notes: notes}
	}

	words := req.Transcript.Words
	kept := keptSet(words, plan.Keep)
	p := placement{notes: make([]roughcut.ScreenNote, 0, len(notes))}

	for _, n := range notes {
		if roughcut.NoteShown(n, plan.Keep, roughcut.NoteCheckSlack) {
			p.notes = append(p.notes, n)

			continue
		}

		h := locateNote(words, kept, n)
		if moved, ok := moveNote(words, plan, &h); ok {
			p.notes = append(p.notes, moved)

			continue
		}

		p.hidden = append(p.hidden, h)
	}

	if in.dropHidden {
		settleHidden(req, in, notes, plan, &p)
	}

	slices.SortStableFunc(p.notes, func(a, b roughcut.ScreenNote) int { return cmp.Compare(a.Start, b.Start) })

	if len(p.notes) == 0 {
		p.notes = nil
	}

	return p
}

// settleHidden puts every hidden section it can on the first kept word after it and drops the rest (the last attempt).
func settleHidden(req *Request, in *Input, source []roughcut.ScreenNote, plan *roughcut.Plan, p *placement) {
	words := req.Transcript.Words

	for i := range p.hidden {
		h := &p.hidden[i]

		if moved, ok := sectionPlace(words, source, plan, p.notes, h); ok {
			try := slices.Concat(p.notes, []roughcut.ScreenNote{moved})
			slices.SortStableFunc(try, func(a, b roughcut.ScreenNote) int { return cmp.Compare(a.Start, b.Start) })

			if checkNotesApart(in, &roughcut.Edit{ScreenNotes: try}, plan) == nil {
				p.notes = try
				p.details = append(p.details, fmt.Sprintf(relocatedDetailFormat, h.note.Text, h.next,
					wordText(words, h.next), roughcut.FormatTimecode(moved.Start),
					roughcut.FormatTimecode(h.note.Start)))

				continue
			}
		}

		p.dropped = append(p.dropped, *h)
	}

	p.details = append(p.details, hiddenNotesDetails(p.dropped)...)
}

// sectionPlace is the hidden section put on the first kept word after it; false for another note, without a kept word
// after it, with another note of the decision starting between them, or with a section already there.
func sectionPlace(
	words []transcription.Word,
	source []roughcut.ScreenNote,
	plan *roughcut.Plan,
	placed []roughcut.ScreenNote,
	h *hiddenNote,
) (roughcut.ScreenNote, bool) {
	if h.note.Kind != roughcut.SectionNote || h.next < 0 {
		return roughcut.ScreenNote{}, false
	}

	moved := h.note
	moved.Start = roundSeconds(words[h.next].Start)

	if moved.End <= moved.Start {
		moved.End = 0
	}

	for _, n := range source {
		if n != h.note && n.Start > h.note.Start && n.Start <= moved.Start+segmentSlack {
			return roughcut.ScreenNote{}, false
		}
	}

	if startsSection(moved.Start, placed) {
		return roughcut.ScreenNote{}, false
	}

	return moved, roughcut.NoteShown(moved, plan.Keep, roughcut.NoteCheckSlack)
}

// locateNote finds the word the note stands on (the first word that ends after its start), the words cut from it on
// and the first kept word after it.
func locateNote(words []transcription.Word, kept []bool, n roughcut.ScreenNote) hiddenNote {
	h := hiddenNote{note: n, word: -1, next: -1, cut: roughcut.WordRange{From: 0, To: -1}}

	for i, w := range words {
		if w.End > n.Start {
			h.word = i

			break
		}
	}

	if h.word < 0 {
		return h
	}

	for i := h.word; i < len(words); i++ {
		if kept[i] {
			h.next = i

			break
		}
	}

	last := len(words) - 1
	if h.next >= 0 {
		last = h.next - 1
	}

	if last >= h.word {
		h.cut = roughcut.WordRange{From: h.word, To: last}
	}

	return h
}

// moveNote moves the note onto the first kept word after it when the worker's own reach shows it there; false when it
// does not (or the moved note would still not be seen).
func moveNote(words []transcription.Word, plan *roughcut.Plan, h *hiddenNote) (roughcut.ScreenNote, bool) {
	if h.next < 0 || !roughcut.NoteShown(h.note, plan.Keep, 0) {
		return roughcut.ScreenNote{}, false
	}

	moved := h.note
	moved.Start = roundSeconds(words[h.next].Start)

	if moved.End <= moved.Start {
		moved.End = 0
	}

	return moved, roughcut.NoteShown(moved, plan.Keep, roughcut.NoteCheckSlack)
}

// hiddenNotesError is the check failure about the hidden notes: for each (maxHiddenNamed of them), where it stands,
// what is cut around it and the nearest kept word (cut to maxHiddenWhereRunes), and what to do (never cut).
func hiddenNotesError(req *Request, in *Input, hidden []hiddenNote) error {
	parts := make([]string, 0, min(len(hidden), maxHiddenNamed))
	for i := range hidden[:min(len(hidden), maxHiddenNamed)] {
		parts = append(parts, hiddenNoteText(req, in, &hidden[i]))
	}

	text := fmt.Sprintf("элементов на экране не видно в ролике: %d (их начало вырезано, а продолжение речи дальше, "+
		"чем элемент переносится) — %s", len(hidden), strings.Join(parts, "; "))
	if rest := len(hidden) - len(parts); rest > 0 {
		text += fmt.Sprintf("; и ещё %d", rest)
	}

	return &CheckError{Message: text, hiddenNotes: true}
}

// hiddenNoteText: «раздел «ошибка 2» стоит на слове 40 «ошибка» (0:21.6 исходника); вырезаны слова 40–45 (cleanup_words
// 40–42, remove_words 43–45); ближайшее оставленное слово после него — 46 «а» (0:32.2), через 10.6 с: поставь раздел
// на слово 46 «а» или не вырезай слова 40–45».
func hiddenNoteText(req *Request, in *Input, h *hiddenNote) string {
	where, action := hiddenNoteParts(req, in, h)

	return truncate(where, maxHiddenWhereRunes) + ": " + action
}

// hiddenNoteParts are where the hidden note stands (with what is cut and the nearest kept word) and what to do.
func hiddenNoteParts(req *Request, in *Input, h *hiddenNote) (where, action string) {
	words := req.Transcript.Words

	var b strings.Builder

	fmt.Fprintf(&b, "%s «%s» ", noteKindNames[h.note.Kind], h.note.Text)

	if h.word < 0 {
		fmt.Fprintf(&b, "стоит на %s исходника, после конца речи", roughcut.FormatTimecode(h.note.Start))

		return b.String(), "убери его или поставь на оставленное слово"
	}

	fmt.Fprintf(&b, "стоит на слове %d «%s» (%s исходника)", h.word, wordText(words, h.word),
		roughcut.FormatTimecode(h.note.Start))

	cut := ""
	if h.cut.From <= h.cut.To {
		cut = indexRange(h.cut.From, h.cut.To)
		fmt.Fprintf(&b, "; вырезаны слова %s%s", cut, cutSources(in, h.cut))
	}

	if h.next < 0 {
		b.WriteString("; после него в ролике речи нет")

		return b.String(), "убери этот элемент или " + keepWordsText(req, cut)
	}

	next := words[h.next]
	fmt.Fprintf(&b, "; ближайшее оставленное слово после него — %d «%s» (%s), через %.1f с", h.next,
		wordText(words, h.next), roughcut.FormatTimecode(next.Start), next.Start-h.note.Start)

	action = fmt.Sprintf("поставь %s на слово %d «%s»", noteKindAccusative[h.note.Kind], h.next, wordText(words, h.next))
	if cut != "" {
		action += " или " + keepWordsText(req, cut)
	}

	return b.String(), action
}

// noteKindAccusative: «поставь раздел / заголовок списка / пункт / выноску».
var noteKindAccusative = map[roughcut.NoteKind]string{
	roughcut.ListTitleNote: "заголовок списка",
	roughcut.ListItemNote:  "пункт",
	roughcut.CalloutNote:   "выноску",
	roughcut.SectionNote:   "раздел",
}

// keepWordsText: «не вырезай слова 40–45» (the first version), «верни слова 40–45 (restore_words)» (a revision).
func keepWordsText(req *Request, cut string) string {
	if req.IsRevision() {
		return "верни слова " + cut + " (restore_words)"
	}

	return "не вырезай слова " + cut
}

// cutSources names the fields of the decision that cut the words of the range: « (cleanup_words 40–42, remove_words
// 43–45)»; "" when the decision cut none of them (the automatic cut or an earlier version did).
func cutSources(in *Input, r roughcut.WordRange) string {
	var parts []string

	for _, field := range []struct {
		name   string
		ranges []roughcut.WordRange
	}{{"cleanup_words", in.CleanupWords}, {"remove_words", in.Remove}} {
		for _, c := range field.ranges {
			from, to := max(c.From, r.From), min(c.To, r.To)
			if from <= to {
				parts = append(parts, field.name+" "+indexRange(from, to))
			}
		}
	}

	if len(parts) == 0 {
		return ""
	}

	return " (" + strings.Join(parts, ", ") + ")"
}

// wordText is the word as the transcript has it, short.
func wordText(words []transcription.Word, i int) string {
	return truncate(oneLine(words[i].Word), maxWordRunes)
}

// hiddenNotesLine: «Плашку «ошибка 2» не поставил: …» for the notes dropped on the last attempt.
func hiddenNotesLine(hidden []hiddenNote) []string {
	if len(hidden) == 0 {
		return nil
	}

	names := make([]string, 0, len(hidden))
	for _, h := range hidden {
		names = append(names, "«"+h.note.Text+"»")
	}

	format := hiddenManyFormat
	if len(hidden) == 1 {
		format = hiddenOneFormat
	}

	return []string{fmt.Sprintf(format, strings.Join(names, ", "))}
}

// hiddenNotesDetails are the lines of the decisions log about the notes dropped on the last attempt.
func hiddenNotesDetails(hidden []hiddenNote) []string {
	out := make([]string, 0, len(hidden))
	for _, h := range hidden {
		out = append(out, fmt.Sprintf(hiddenDetailFormat, noteKindNames[h.note.Kind], h.note.Text,
			roughcut.FormatTimecode(h.note.Start)))
	}

	return out
}
