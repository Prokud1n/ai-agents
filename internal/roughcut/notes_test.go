package roughcut

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// calloutYears is the text of a callout in the tests.
const calloutYears = "6 лет"

// TestRemapScreenNotes: the notes go onto the reel's timeline ordered by time; a note that starts in a cut piece is
// dropped, an end in a cut moves to where the reel goes on, an end not after the start is dropped.
func TestRemapScreenNotes(t *testing.T) {
	t.Parallel()

	keep := []Interval{{Start: 0, End: 2}, {Start: 4, End: 6}}

	tests := []struct {
		name     string
		input    []ScreenNote
		expected []ScreenNote
	}{
		{name: "none", expected: nil},
		{
			name: "moved, ordered, the cut one dropped",
			input: []ScreenNote{
				{Kind: ListTitleNote, Text: "3 вещи", Start: 5, End: 7},
				{Kind: CalloutNote, Text: calloutYears, Start: 1},
				{Kind: ListItemNote, Text: "1/ вода", Start: 3},
				{Kind: ListItemNote, Text: "2/ сон", Start: 4.5, End: 3},
				{Kind: ListItemNote, Text: "3/ бег", Start: 1.5, End: 3},
			},
			expected: []ScreenNote{
				{Kind: CalloutNote, Text: calloutYears, Start: 1},
				{Kind: ListItemNote, Text: "3/ бег", Start: 1.5, End: 2},
				{Kind: ListItemNote, Text: "2/ сон", Start: 2.5},
				{Kind: ListTitleNote, Text: "3 вещи", Start: 3, End: 4},
			},
		},
		{name: "all cut", input: []ScreenNote{{Kind: CalloutNote, Text: calloutYears, Start: 3}}, expected: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, RemapScreenNotes(tt.input, keep))
		})
	}
}

// TestScreenNotesInEditAndLog: the notes are copied with the edit, written to the decisions log and to the JSON of
// the plan only when there are some; a workout plan draws none.
func TestScreenNotesInEditAndLog(t *testing.T) {
	t.Parallel()

	notes := []ScreenNote{{Kind: ListItemNote, Text: "1/ вода", Start: 1}, {Kind: CalloutNote, Text: calloutYears, Start: 2}}
	edit := &Edit{ScreenNotes: notes}
	cloned := cloneEdit(edit)
	edit.ScreenNotes[0].Text = "changed"

	assert.Equal(t, "1/ вода", cloned.ScreenNotes[0].Text, "a deep copy")

	src := speech("давайте приступим к разминке")
	plan := BuildPlan(src, src.Duration, testSettings())
	plan.Edit = cloned

	log := DecisionsLog(&plan)
	assert.Contains(t, log, "На экране: 2\n- с 0:01.0, пункт «1/ вода»\n- с 0:02.0, выноска «6 лет»\n")

	raw, err := json.Marshal(cloned)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"screen_notes":[{"kind":"list_item","text":"1/ вода","start":1}`)

	plain, err := json.Marshal(&Edit{})
	require.NoError(t, err)
	assert.NotContains(t, string(plain), "screen_notes")

	assert.Equal(t, cloned.ScreenNotes, editScreenNotes(cloned))
	assert.Nil(t, editScreenNotes(&Edit{Mode: WorkoutMode, ScreenNotes: notes}), "a workout has no notes")
	assert.Nil(t, editScreenNotes(nil))
}
