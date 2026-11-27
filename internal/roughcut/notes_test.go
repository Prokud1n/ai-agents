package roughcut

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Texts of the screen notes in the tests.
const (
	calloutYears = "6 лет"
	itemWater    = "1/ вода"
	sectionTwo   = "страх 2"
	sectionThree = "страх 3"
)

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
				{Kind: ListItemNote, Text: itemWater, Start: 3},
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

// TestRemapScreenNotesShift (T3.22): a note that starts just before a kept piece (Whisper started its word early, the
// cut stands by the sound) moves to the start of that piece; a section set on its cut reading («страх номер два»)
// moves to where the speech goes on; further away both are dropped.
func TestRemapScreenNotesShift(t *testing.T) {
	t.Parallel()

	// The reel: 100.0–120.0 | 150.233–151.6 | 153.2–160.0 of the source.
	keep := []Interval{{Start: 100, End: 120}, {Start: 150.233, End: 151.6}, {Start: 153.2, End: 160}}

	tests := []struct {
		name     string
		input    ScreenNote
		expected []ScreenNote
	}{
		{
			name:     "a section 0.11 s before the kept piece",
			input:    ScreenNote{Kind: SectionNote, Text: sectionTwo, Start: 150.12},
			expected: []ScreenNote{{Kind: SectionNote, Text: sectionTwo, Start: 20}},
		},
		{
			name:     "an item 0.11 s before the kept piece",
			input:    ScreenNote{Kind: ListItemNote, Text: itemWater, Start: 150.12, End: 151},
			expected: []ScreenNote{{Kind: ListItemNote, Text: itemWater, Start: 20, End: 20.767}},
		},
		{
			name:     "a section on its cut reading, 1.6 s before the question",
			input:    ScreenNote{Kind: SectionNote, Text: sectionThree, Start: 151.6},
			expected: []ScreenNote{{Kind: SectionNote, Text: sectionThree, Start: 21.367}},
		},
		{
			name:     "an end inside the reading is dropped",
			input:    ScreenNote{Kind: SectionNote, Text: sectionThree, Start: 151.6, End: 152.5},
			expected: []ScreenNote{{Kind: SectionNote, Text: sectionThree, Start: 21.367}},
		},
		{name: "a callout 1.6 s before speech", input: ScreenNote{Kind: CalloutNote, Text: calloutYears, Start: 151.6}},
		{name: "a section 4 s before speech", input: ScreenNote{Kind: SectionNote, Text: "страх 1", Start: 146}},
		{name: "a section after the last piece", input: ScreenNote{Kind: SectionNote, Text: "страх 4", Start: 161}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, RemapScreenNotes([]ScreenNote{tt.input}, keep))
		})
	}
}

// TestScreenNotesInEditAndLog: the notes are copied with the edit, written to the decisions log and to the JSON of
// the plan only when there are some; a workout plan draws none.
func TestScreenNotesInEditAndLog(t *testing.T) {
	t.Parallel()

	notes := []ScreenNote{{Kind: ListItemNote, Text: itemWater, Start: 1}, {Kind: CalloutNote, Text: calloutYears, Start: 2}}
	edit := &Edit{ScreenNotes: notes}
	cloned := cloneEdit(edit)
	edit.ScreenNotes[0].Text = "changed"

	assert.Equal(t, itemWater, cloned.ScreenNotes[0].Text, "a deep copy")

	src := speech("давайте приступим к разминке")
	plan := BuildPlan(src, src.Duration, testSettings())
	plan.Edit = cloned

	log := DecisionsLog(&plan)
	assert.Contains(t, log, "На экране: 2\n- с 0:01.0, пункт «1/ вода»\n- с 0:02.0, выноска «6 лет»\n")

	raw, err := json.Marshal(cloned)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"screen_notes":[{"kind":"list_item","text":"`+itemWater+`","start":1}`)

	plain, err := json.Marshal(&Edit{})
	require.NoError(t, err)
	assert.NotContains(t, string(plain), "screen_notes")

	assert.Equal(t, cloned.ScreenNotes, editScreenNotes(cloned))
	assert.Nil(t, editScreenNotes(&Edit{Mode: WorkoutMode, ScreenNotes: notes}), "a workout has no notes")
	assert.Nil(t, editScreenNotes(nil))
}
