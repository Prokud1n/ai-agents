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

const sectionError = "ошибка 2"

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

			assert.LessOrEqual(t, len([]rune(check.Message)), maxHiddenMessageRunes+1)
		})
	}
}

// TestHiddenNoteText: a revision is told to bring the words back, a note after the speech to be removed.
func TestHiddenNoteText(t *testing.T) {
	t.Parallel()

	revision := readingsRequest(t)
	revision.Version = 2

	note := roughcut.ScreenNote{Kind: roughcut.CalloutNote, Text: noteYears, Start: 2.4}

	tests := []struct {
		name     string
		req      *Request
		hidden   hiddenNote
		expected string
	}{
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

			assert.Equal(t, tt.expected, hiddenNoteText(tt.req, &Input{}, &tt.hidden))
		})
	}
}

// TestSubmitDropsHiddenNotes (T3.29): the last attempt that fails only by notes the reel would not show is accepted
// without them, with a line for the trainer and one in the details; another failure is still the automatic cut.
func TestSubmitDropsHiddenNotes(t *testing.T) {
	t.Parallel()

	const (
		hidden = `{"decision": "edit", "reason": "отбор", "remove_words": [{"from": 3, "to": 6}], "screen_notes": [` +
			`{"kind": "section", "text": "ошибка 2", "word": 3}]}`
		placed = `{"decision": "edit", "reason": "отбор", "remove_words": [{"from": 3, "to": 6}], "screen_notes": [` +
			`{"kind": "section", "text": "ошибка 2", "word": 7}]}`
		broken = `{"decision": "edit", "reason": "x", "remove_words": [{"from": 90, "to": 91}]}`
	)

	tests := []struct {
		name       string
		inputs     []string
		toolErrors int
		kind       OutcomeKind
		dropped    bool
	}{
		{name: "placed after the message", inputs: []string{hidden, placed}, toolErrors: 1, kind: EditOutcome},
		{name: "hidden twice: accepted without it", inputs: []string{hidden, hidden}, toolErrors: 1, kind: EditOutcome, dropped: true},
		{name: "broken first, hidden last", inputs: []string{broken, hidden}, toolErrors: 1, kind: EditOutcome, dropped: true},
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

			if !tt.dropped {
				return
			}

			assert.Empty(t, outcome.Edit.ScreenNotes)
			assert.Contains(t, outcome.Changes, "Плашку «ошибка 2» не показал: её начало вырезано.")
			assert.Contains(t, outcome.Edit.Details,
				"Элемент на экране не показан, его начало вырезано: раздел «ошибка 2» с 0:02.4 исходника")
		})
	}

	answer, err := NewSubmitTool().Execute(WithSession(t.Context(), NewSession(readingsRequest(t))),
		json.RawMessage(hidden))
	require.Error(t, err)
	assert.Empty(t, answer)
}

// TestHiddenNotesLine: one note or several.
func TestHiddenNotesLine(t *testing.T) {
	t.Parallel()

	one := hiddenNote{note: roughcut.ScreenNote{Kind: roughcut.SectionNote, Text: sectionOne}}
	two := hiddenNote{note: roughcut.ScreenNote{Kind: roughcut.SectionNote, Text: sectionTwo}}

	tests := []struct {
		name     string
		hidden   []hiddenNote
		expected []string
	}{
		{name: "none"},
		{name: "one", hidden: []hiddenNote{one}, expected: []string{"Плашку «страх 1» не показал: её начало вырезано."}},
		{
			name: "two", hidden: []hiddenNote{one, two},
			expected: []string{"Плашки «страх 1», «страх 2» не показал: их начало вырезано."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, hiddenNotesLine(tt.hidden))
		})
	}
}
