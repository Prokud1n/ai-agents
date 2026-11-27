package editplan

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// Texts of the screen notes in the tests.
const (
	noteSpine  = "1/ спина ровно"
	noteSquats = "10 приседаний"
	noteYears  = "6 лет"
	sectionOne = "страх 1"
	sectionTwo = "страх 2"
)

// testNotes are a list of two items and a callout on the words of testSpeech (word 6, «эээ», is cut as a filler).
func testNotes() []NoteInput {
	return []NoteInput{
		{Kind: roughcut.ListItemNote, Text: noteSpine, Word: new(0)},
		{Kind: roughcut.ListTitleNote, Text: " «как держать спину». ", Start: new(0.45)},
		{Kind: roughcut.ListItemNote, Text: "2/ ещё раз ровно", Word: new(3), End: new(4.0)},
		{Kind: roughcut.CalloutNote, Text: noteSquats, Word: new(9)},
	}
}

// TestApplyScreenNotes: the notes of a talking head are checked, kept by the edit ordered by time on the source (the
// text cleaned like the hook) and named in the description; a workout, a cut word or a broken note is refused.
func TestApplyScreenNotes(t *testing.T) {
	t.Parallel()

	first, err := Apply(testRequest(1, nil), &Input{Decision: EditDecision, Reason: testReason,
		ScreenNotes: new(testNotes())})
	require.NoError(t, err)

	words := speech(testSpeech).Words
	assert.Equal(t, []roughcut.ScreenNote{
		{Kind: roughcut.ListTitleNote, Text: "как держать спину", Start: 0.45},
		{Kind: roughcut.ListItemNote, Text: noteSpine, Start: words[0].Start},
		{Kind: roughcut.ListItemNote, Text: "2/ ещё раз ровно", Start: words[3].Start, End: 4},
		{Kind: roughcut.CalloutNote, Text: noteSquats, Start: words[9].Start},
	}, first.Edit.ScreenNotes)
	assert.Equal(t, "На экране: 2 пункта и 1 выноска.", first.Changes[0], "named first, never folded")

	tooMany := make([]NoteInput, maxScreenNotes+1)
	for i := range tooMany {
		tooMany[i] = NoteInput{Kind: roughcut.CalloutNote, Text: noteYears, Word: new(0)}
	}

	tests := []struct {
		name  string
		notes []NoteInput
		mode  roughcut.Mode
		err   string
	}{
		{name: "an unknown kind", notes: []NoteInput{{Kind: "title", Text: noteYears, Word: new(0)}}, err: "kind"},
		{
			name: "too long", err: "от 1 до 40",
			notes: []NoteInput{{Kind: roughcut.CalloutNote, Text: strings.Repeat("а", maxNoteRunes+1), Word: new(0)}},
		},
		{
			name: "markup", err: "без переносов строк",
			notes: []NoteInput{{Kind: roughcut.CalloutNote, Text: "{\\an7}6 лет", Word: new(0)}},
		},
		{
			name: "an emoji", err: "без эмодзи",
			notes: []NoteInput{{Kind: roughcut.CalloutNote, Text: "6 лет 🔥", Word: new(0)}},
		},
		{
			name: "a word and seconds", err: "укажи начало",
			notes: []NoteInput{{Kind: roughcut.CalloutNote, Text: noteYears, Word: new(0), Start: new(1.0)}},
		},
		{name: "no start", notes: []NoteInput{{Kind: roughcut.CalloutNote, Text: noteYears}}, err: "укажи начало"},
		{
			name: "no such word", err: "нет в расшифровке",
			notes: []NoteInput{{Kind: roughcut.CalloutNote, Text: noteYears, Word: new(99)}},
		},
		{
			name: "past the source", err: "start_seconds",
			notes: []NoteInput{{Kind: roughcut.CalloutNote, Text: noteYears, Start: new(99.0)}},
		},
		{
			name: "the end before the start", err: "end_seconds",
			notes: []NoteInput{{Kind: roughcut.CalloutNote, Text: noteYears, Word: new(3), End: new(1.0)}},
		},
		{name: "too many", notes: tooMany, err: "слишком много"},
		{
			name: "a long section heading", err: "от 1 до 24",
			notes: []NoteInput{{Kind: roughcut.SectionNote, Text: strings.Repeat("а", maxSectionRunes+1), Word: new(0)}},
		},
		{name: "in a workout", notes: testNotes(), mode: roughcut.WorkoutMode, err: "только в разговорном"},
		{
			name: "on a cut word", err: "не видно в ролике",
			notes: []NoteInput{{Kind: roughcut.CalloutNote, Text: "эээ", Word: new(6)}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Apply(testRequest(1, nil), &Input{
				Decision: EditDecision, Reason: testReason, Mode: tt.mode, ScreenNotes: new(tt.notes),
			})

			var check *CheckError
			require.ErrorAs(t, err, &check)
			assert.Contains(t, check.Message, tt.err)
		})
	}
}

// TestApplySections: the section headings of a talking head («страх 1») are kept by the edit like the other notes,
// up to 24 characters, and named in the description.
func TestApplySections(t *testing.T) {
	t.Parallel()

	outcome, err := Apply(testRequest(1, nil), &Input{Decision: EditDecision, Reason: testReason,
		ScreenNotes: new([]NoteInput{
			{Kind: roughcut.SectionNote, Text: sectionTwo, Word: new(3)},
			{Kind: roughcut.SectionNote, Text: sectionOne, Word: new(0)},
		})})
	require.NoError(t, err)

	words := speech(testSpeech).Words
	assert.Equal(t, []roughcut.ScreenNote{
		{Kind: roughcut.SectionNote, Text: sectionOne, Start: words[0].Start},
		{Kind: roughcut.SectionNote, Text: sectionTwo, Start: words[3].Start},
	}, outcome.Edit.ScreenNotes)
	assert.Equal(t, "На экране: 2 раздела.", outcome.Changes[0])
}

// TestApplySectionOnCutReading (T3.22): a section set on the words it removes (the spoken heading) is shown where the
// speech goes on; a note at the same place, or a section too far before the speech, goes back to the model.
func TestApplySectionOnCutReading(t *testing.T) {
	t.Parallel()

	reading := []roughcut.WordRange{{From: 0, To: 2}}

	tests := []struct {
		name   string
		remove []roughcut.WordRange
		notes  []NoteInput
		err    string
	}{
		{name: "the section on its cut reading", remove: reading,
			notes: []NoteInput{{Kind: roughcut.SectionNote, Text: sectionOne, Word: new(0)}}},
		{
			name: "an item at the same place", remove: reading, err: "в одном месте",
			notes: []NoteInput{
				{Kind: roughcut.SectionNote, Text: sectionOne, Word: new(0)},
				{Kind: roughcut.CalloutNote, Text: noteYears, Word: new(3)},
			},
		},
		{
			name: "two sections in one cut", remove: reading, err: "в одном месте",
			notes: []NoteInput{
				{Kind: roughcut.SectionNote, Text: sectionOne, Word: new(0)},
				{Kind: roughcut.SectionNote, Text: sectionTwo, Word: new(1)},
			},
		},
		{
			name: "too far before the speech", remove: []roughcut.WordRange{{From: 3, To: 5}}, err: "их начало вырезано",
			notes: []NoteInput{{Kind: roughcut.SectionNote, Text: sectionTwo, Word: new(3)}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outcome, err := Apply(testRequest(1, nil), &Input{
				Decision: EditDecision, Reason: testReason, Remove: tt.remove, ScreenNotes: new(tt.notes),
			})

			if tt.err == "" {
				require.NoError(t, err)
				assert.Len(t, outcome.Edit.ScreenNotes, len(tt.notes))

				return
			}

			var check *CheckError
			require.ErrorAs(t, err, &check)
			assert.Contains(t, check.Message, tt.err)
		})
	}
}

// TestApplyScreenNotesRevision: a revision replaces the list (an empty one removes it), keeps it when it does not
// name it, and a list that changes nothing is no revision; the reel built with notes alone is the same.
func TestApplyScreenNotesRevision(t *testing.T) {
	t.Parallel()

	first, err := Apply(testRequest(1, nil), &Input{Decision: EditDecision, ScreenNotes: new(testNotes())})
	require.NoError(t, err)

	previous := first.Edit

	removed, err := Apply(testRequest(2, &previous), &Input{Decision: EditDecision, ScreenNotes: new([]NoteInput{})})
	require.NoError(t, err)
	assert.Nil(t, removed.Edit.ScreenNotes)
	assert.Equal(t, []string{notesRemovedLine}, removed.Changes)
	assert.InDelta(t, first.Duration, removed.Duration, 1e-9, "the cut is the same")

	changed := testNotes()[:1]
	replaced, err := Apply(testRequest(2, &previous), &Input{Decision: EditDecision, ScreenNotes: &changed})
	require.NoError(t, err)
	require.Len(t, replaced.Edit.ScreenNotes, 1)
	assert.Equal(t, []string{"На экране: 1 пункт."}, replaced.Changes)

	workout, err := Apply(testRequest(2, &previous), &Input{Decision: EditDecision, Mode: roughcut.WorkoutMode})
	require.NoError(t, err)
	assert.Nil(t, workout.Edit.ScreenNotes, "a workout has no notes")
	assert.Equal(t, []string{notesRemovedLine, workoutModeLine}, workout.Changes)

	kept, err := Apply(testRequest(2, &previous), &Input{Decision: EditDecision, TrimEnd: 1.5})
	require.NoError(t, err)
	assert.Equal(t, previous.ScreenNotes, kept.Edit.ScreenNotes, "kept when the revision does not name them")

	_, err = Apply(testRequest(2, &previous), &Input{Decision: EditDecision, ScreenNotes: new(testNotes())})

	var check *CheckError
	require.ErrorAs(t, err, &check)
	assert.Contains(t, check.Message, "ничего не меняет")
}

// TestApplyCleanupAllowsNotes: a reel only cleaned by the editor takes the screen notes: they cut nothing.
func TestApplyCleanupAllowsNotes(t *testing.T) {
	t.Parallel()

	outcome, err := Apply(cleanupRequest(), &Input{
		Decision: EditDecision, HookTitle: new(testHook),
		ScreenNotes: new([]NoteInput{{Kind: roughcut.CalloutNote, Text: noteSquats, Word: new(9)}}),
	})
	require.NoError(t, err)
	assert.Len(t, outcome.Edit.ScreenNotes, 1)
}

func TestNotesLine(t *testing.T) {
	t.Parallel()

	item := roughcut.ScreenNote{Kind: roughcut.ListItemNote}
	callout := roughcut.ScreenNote{Kind: roughcut.CalloutNote}
	title := roughcut.ScreenNote{Kind: roughcut.ListTitleNote}
	section := roughcut.ScreenNote{Kind: roughcut.SectionNote}

	tests := []struct {
		name     string
		input    []roughcut.ScreenNote
		expected string
	}{
		{name: "items and callouts", input: []roughcut.ScreenNote{title, item, item, item, callout, callout},
			expected: "На экране: 3 пункта и 2 выноски."},
		{name: "one item", input: []roughcut.ScreenNote{item}, expected: "На экране: 1 пункт."},
		{name: "three sections", input: []roughcut.ScreenNote{section, section, section}, expected: "На экране: 3 раздела."},
		{name: "sections and a callout", input: []roughcut.ScreenNote{section, callout, section},
			expected: "На экране: 2 раздела и 1 выноска."},
		{name: "five callouts", input: []roughcut.ScreenNote{callout, callout, callout, callout, callout},
			expected: "На экране: 5 выносок."},
		{name: "a title alone", input: []roughcut.ScreenNote{title}, expected: "На экране: заголовок списка."},
		{name: "two titles", input: []roughcut.ScreenNote{title, title}, expected: "На экране: 2 заголовка списка."},
		{name: "eleven items", input: []roughcut.ScreenNote{item, item, item, item, item, item, item, item, item, item, item},
			expected: "На экране: 11 пунктов."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, notesLine(tt.input))
		})
	}
}

// TestPromptScreenNotes: the model is asked for the notes of a first talking head, told they are added only on
// request in a revision, sees the current ones with their place in the reel; nothing of them in a workout.
func TestPromptScreenNotes(t *testing.T) {
	t.Parallel()

	withNotes := &roughcut.Edit{ScreenNotes: []roughcut.ScreenNote{
		{Kind: roughcut.ListItemNote, Text: noteSpine, Start: 0.5},
		{Kind: roughcut.CalloutNote, Text: "пауза", Start: 4.5}, // in the long pause, 1.6 s before the speech
	}}

	tests := []struct {
		name     string
		req      *Request
		expected []string
		absent   []string
	}{
		{
			name: "a first version", req: testRequest(1, nil),
			expected: []string{"Пункты и цифры на экране: если тренер перечисляет", readingsCut},
		},
		{
			name: "a revision without notes", req: testRequest(2, &roughcut.Edit{}),
			expected: []string{"Пунктов и цифр на экране нет. Добавь их через screen_notes, только если тренер просит."},
		},
		{
			name: "a revision with notes", req: testRequest(2, withNotes),
			expected: []string{
				"screen_notes заменяет весь список",
				"- пункт «1/ спина ровно», с 0:00.5 исходника (в ролике 0:00.",
				"- выноска «пауза», с 0:04.5 исходника (вырезано, в ролике не видно)",
			},
		},
		{
			name: "workout", req: testRequest(1, &roughcut.Edit{Mode: roughcut.WorkoutMode}),
			absent: []string{"screen_notes", "Пункты и цифры на экране"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			prompt := Prompt(tt.req)

			for _, s := range tt.expected {
				assert.Contains(t, prompt, s)
			}

			for _, s := range tt.absent {
				assert.NotContains(t, prompt, s)
			}
		})
	}
}
