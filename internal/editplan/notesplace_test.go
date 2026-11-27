package editplan

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// readingsSpeech reads a section heading twice (a failed take of the reading, then the reading right before the
// point):
//
//	0–2 сегодня про бег | 3–4 ошибка два || 5–6 ошибка два || 7–10 не разминаться перед бегом
const readingsSpeech = "сегодня про бег. | ошибка два. || ошибка два. || не разминаться перед бегом."

// Section headings of the tests.
const (
	sectionErrorOne   = "ошибка 1"
	sectionError      = "ошибка 2"
	sectionErrorThree = "ошибка 3"
)

// readingsRequest is the first version of readingsSpeech planned with the margins of the service.
func readingsRequest(t *testing.T) *Request {
	t.Helper()

	src := speech(readingsSpeech)
	settings := testSettings()
	settings.MaxPause, settings.PadBefore, settings.PadAfter = 0.3, 0.12, 0.18

	plan, err := roughcut.BuildPlanEdited(src, src.Duration, settings, &roughcut.Edit{})
	require.NoError(t, err)

	return &Request{TaskID: 7, Version: 1, Transcript: src, Plan: &plan}
}

// TestApplyPlacesNotes (T3.29): a section the app check hides but the worker's reach shows is moved onto the first kept
// word after it; one beyond the reach goes back to the model with where it stands, what is cut, the nearest kept word
// and what to do.
func TestApplyPlacesNotes(t *testing.T) {
	t.Parallel()

	words := speech(readingsSpeech).Words

	tests := []struct {
		name  string
		word  int
		moved float64
		err   []string
	}{
		{name: "the last reading: moved onto the point", word: 5, moved: words[7].Start},
		{
			name: "the failed take of the reading: refused with the details", word: 3,
			err: []string{
				"элементов на экране не видно в ролике: 1 (их начало вырезано",
				"раздел «ошибка 2» стоит на слове 3 «ошибка» (0:02.4 исходника)",
				"вырезаны слова 3–6 (remove_words 3–6)",
				"ближайшее оставленное слово после него — 7 «не» (0:08.1), через 5.7 с",
				"поставь раздел на слово 7 «не» или не вырезай слова 3–6",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outcome, err := Apply(readingsRequest(t), &Input{
				Decision: EditDecision, Reason: testReason, Remove: []roughcut.WordRange{{From: 3, To: 6}},
				ScreenNotes: new([]NoteInput{{Kind: roughcut.SectionNote, Text: sectionError, Word: new(tt.word)}}),
			})

			if tt.err == nil {
				require.NoError(t, err)
				require.Len(t, outcome.Edit.ScreenNotes, 1)
				assert.InDelta(t, tt.moved, outcome.Edit.ScreenNotes[0].Start, 1e-9)
				assert.Equal(t, "На экране: 1 раздел.", outcome.Changes[0])

				return
			}

			var check *CheckError
			require.ErrorAs(t, err, &check)
			assert.True(t, check.hiddenNotes)

			for _, part := range tt.err {
				assert.Contains(t, check.Message, part)
			}
		})
	}
}

// TestHiddenNoteText: a revision is told to bring the words back, a note after the speech to be removed.
func TestHiddenNoteText(t *testing.T) {
	t.Parallel()

	revision := readingsRequest(t)
	revision.Version = 2

	note := roughcut.ScreenNote{Kind: roughcut.CalloutNote, Text: noteYears, Start: 2.4}

	many := Input{}
	for i := 3; i <= 60; i += 2 {
		many.Remove = append(many.Remove, roughcut.WordRange{From: i, To: i})
	}

	tests := []struct {
		name     string
		req      *Request
		in       Input
		hidden   hiddenNote
		expected string
	}{
		{
			name: "a long description cut, the action kept", req: readingsRequest(t), in: many,
			hidden: hiddenNote{note: note, word: 3, next: 7, cut: roughcut.WordRange{From: 3, To: 60}},
			expected: truncate("выноска «6 лет» стоит на слове 3 «ошибка» (0:02.4 исходника); вырезаны слова 3–60"+
				cutSources(&many, roughcut.WordRange{From: 3, To: 60}), maxHiddenWhereRunes) +
				": поставь выноску на слово 7 «не» или не вырезай слова 3–60",
		},
		{
			name: "the revision", req: revision,
			hidden: hiddenNote{note: note, word: 3, next: 7, cut: roughcut.WordRange{From: 3, To: 6}},
			expected: "выноска «6 лет» стоит на слове 3 «ошибка» (0:02.4 исходника); вырезаны слова 3–6; ближайшее " +
				"оставленное слово после него — 7 «не» (0:08.1), через 5.7 с: поставь выноску на слово 7 «не» или " +
				"верни слова 3–6 (restore_words)",
		},
		{
			name: "nothing kept after it", req: readingsRequest(t),
			hidden: hiddenNote{note: note, word: 3, next: -1, cut: roughcut.WordRange{From: 3, To: 10}},
			expected: "выноска «6 лет» стоит на слове 3 «ошибка» (0:02.4 исходника); вырезаны слова 3–10; после " +
				"него в ролике речи нет: убери этот элемент или не вырезай слова 3–10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, hiddenNoteText(tt.req, &tt.in, &tt.hidden))
		})
	}
}

// TestSubmitSettlesHiddenNotes (T3.29): the last attempt that fails only by notes the reel would not show is accepted:
// a section is put on the first kept word after it (the question of its point), unless another note starts there; the
// other notes are dropped with a line for the trainer and one in the details. Another failure is the automatic cut.
func TestSubmitSettlesHiddenNotes(t *testing.T) {
	t.Parallel()

	const (
		hidden = `{"decision": "edit", "reason": "отбор", "remove_words": [{"from": 3, "to": 6}], "screen_notes": [` +
			`{"kind": "section", "text": "ошибка 2", "word": 3}]}`
		placed = `{"decision": "edit", "reason": "отбор", "remove_words": [{"from": 3, "to": 6}], "screen_notes": [` +
			`{"kind": "section", "text": "ошибка 2", "word": 7}]}`
		callout = `{"decision": "edit", "reason": "отбор", "remove_words": [{"from": 3, "to": 6}], "screen_notes": [` +
			`{"kind": "callout", "text": "6 лет", "word": 3}]}`
		blocked = `{"decision": "edit", "reason": "отбор", "remove_words": [{"from": 3, "to": 6}], "screen_notes": [` +
			`{"kind": "section", "text": "ошибка 2", "word": 3}, {"kind": "callout", "text": "6 лет", "word": 7}]}`
		broken = `{"decision": "edit", "reason": "x", "remove_words": [{"from": 90, "to": 91}]}`
	)

	sectionDetail := "Раздел «ошибка 2» поставлен на слово 7 «не» (0:08.1 исходника): его начало (0:02.4) вырезано"

	tests := []struct {
		name       string
		inputs     []string
		toolErrors int
		kind       OutcomeKind
		notes      int
		line       string
		detail     string
	}{
		{name: "placed after the message", inputs: []string{hidden, placed}, toolErrors: 1, kind: EditOutcome, notes: 1},
		{
			name: "a section hidden twice: put on the question", inputs: []string{hidden, hidden}, toolErrors: 1,
			kind: EditOutcome, notes: 1, detail: sectionDetail,
		},
		{
			name: "broken first, hidden last", inputs: []string{broken, hidden}, toolErrors: 1, kind: EditOutcome,
			notes: 1, detail: sectionDetail,
		},
		{
			name: "a callout hidden twice: dropped", inputs: []string{callout, callout}, toolErrors: 1, kind: EditOutcome,
			line: "Плашку «6 лет» не поставил: место, где она начиналась, не вошло в ролик. Если она нужна — " +
				"напишите в правке, к какой фразе её поставить.",
			detail: "Элемент на экране не показан, его начало вырезано: выноска «6 лет» с 0:02.4 исходника",
		},
		{
			name: "another note at the question: the section dropped", inputs: []string{blocked, blocked},
			toolErrors: 1, kind: EditOutcome, notes: 1,
			detail: "Элемент на экране не показан, его начало вырезано: раздел «ошибка 2» с 0:02.4 исходника",
		},
		{name: "hidden first, broken last", inputs: []string{hidden, broken}, toolErrors: 1, kind: InvalidOutcome},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := NewSession(readingsRequest(t))
			assert.Equal(t, tt.toolErrors, submitAll(t, session, tt.inputs))

			outcome, ok := session.Outcome()
			require.True(t, ok)
			assert.Equal(t, tt.kind, outcome.Kind, outcome.Reason)
			assert.Len(t, outcome.Edit.ScreenNotes, tt.notes)

			if tt.line != "" {
				assert.Contains(t, outcome.Changes, tt.line)
			}

			if tt.detail != "" {
				assert.Contains(t, outcome.Edit.Details, tt.detail)
			}
		})
	}

	answer, err := NewSubmitTool().Execute(WithSession(t.Context(), NewSession(readingsRequest(t))),
		json.RawMessage(hidden))
	require.Error(t, err)
	assert.Empty(t, answer)
}

// TestSubmitHiddenNotesOnlyChange (T3.29): a revision whose only change is a note the reel would not show is refused by
// the note first; on the last attempt, without it, it changes nothing: the reason is that.
func TestSubmitHiddenNotesOnlyChange(t *testing.T) {
	t.Parallel()

	const callout = `{"decision": "edit", "reason": "выноска", "screen_notes": [{"kind": "callout", "text": "6 лет", ` +
		`"word": 3}]}`

	req := readingsRequest(t)
	previous := roughcut.Edit{Remove: []roughcut.WordRange{{From: 3, To: 6}}}

	plan, err := roughcut.BuildPlanEdited(req.Transcript, req.Transcript.Duration, req.Plan.Settings, &previous)
	require.NoError(t, err)

	plan.Edit = &previous
	req.Version, req.Plan = 2, &plan

	_, err = NewSubmitTool().Execute(WithSession(t.Context(), NewSession(req)), json.RawMessage(callout))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "не видно в ролике")

	session := NewSession(req)
	assert.Equal(t, 1, submitAll(t, session, []string{callout, callout}))

	outcome, ok := session.Outcome()
	require.True(t, ok)
	assert.Equal(t, InvalidOutcome, outcome.Kind)
	assert.Contains(t, outcome.Reason, "ничего не меняет")
}

// TestApplyHiddenAfterOtherChecks (T3.29): the other checks of the notes come first, so the model sees them on its
// first attempt and a refusal by the hidden notes is a refusal by them only.
func TestApplyHiddenAfterOtherChecks(t *testing.T) {
	t.Parallel()

	_, err := Apply(readingsRequest(t), &Input{
		Decision: EditDecision, Reason: testReason, Remove: []roughcut.WordRange{{From: 3, To: 6}},
		ScreenNotes: new([]NoteInput{
			{Kind: roughcut.SectionNote, Text: sectionError, Word: new(5)},
			{Kind: roughcut.SectionNote, Text: sectionErrorThree, Word: new(7)},
			{Kind: roughcut.CalloutNote, Text: noteYears, Word: new(3)},
		}),
	})

	var check *CheckError
	require.ErrorAs(t, err, &check)
	assert.Contains(t, check.Message, "в одном месте")
	assert.False(t, check.hiddenNotes)
}

// TestDroppedRangesMovedSection: the reading of a section placeNotes moved is still its reading, not a phrase that did
// not make it.
func TestDroppedRangesMovedSection(t *testing.T) {
	t.Parallel()

	words := speech(readingsSpeech).Words
	reading := []roughcut.WordRange{{From: 5, To: 6}}
	source := roughcut.ScreenNote{Kind: roughcut.SectionNote, Text: sectionError, Start: words[5].Start}
	moved := roughcut.ScreenNote{Kind: roughcut.SectionNote, Text: sectionError, Start: words[7].Start + 1}

	assert.Equal(t, reading, droppedRanges(words, reading, []roughcut.ScreenNote{moved}))
	assert.Empty(t, droppedRanges(words, reading, []roughcut.ScreenNote{moved, source}))
}

// TestHiddenNotesLine: one note or several.
func TestHiddenNotesLine(t *testing.T) {
	t.Parallel()

	one := hiddenNote{note: roughcut.ScreenNote{Kind: roughcut.SectionNote, Text: sectionErrorOne}}
	two := hiddenNote{note: roughcut.ScreenNote{Kind: roughcut.SectionNote, Text: sectionError}}

	tests := []struct {
		name     string
		hidden   []hiddenNote
		expected []string
	}{
		{name: "none"},
		{
			name: "one", hidden: []hiddenNote{one},
			expected: []string{"Плашку «ошибка 1» не поставил: место, где она начиналась, не вошло в ролик. Если она " +
				"нужна — напишите в правке, к какой фразе её поставить."},
		},
		{
			name: "two", hidden: []hiddenNote{one, two},
			expected: []string{"Плашки «ошибка 1», «ошибка 2» не поставил: места, где они начинались, не вошли в " +
				"ролик. Если они нужны — напишите в правке, к каким фразам их поставить."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, hiddenNotesLine(tt.hidden))
		})
	}
}
