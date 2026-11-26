package subtitles

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Texts of the note tests.
const (
	listTitle    = "3 вещи"
	itemWater    = "1/ вода"
	listOne      = listTitle + "|" + itemWater
	listTwo      = listOne + "|2/ приседания"
	calloutYears = "6 лет"
)

// shownBlock is a note block as the tests compare it.
type shownBlock struct {
	start, end float64
	entries    string
}

// TestNoteBlocks: the notes wait for the hook, a list piles up under its title and stays until its end, a callout
// is shown alone for a moment and the list comes back with its next item, the earliest items scroll away, a note
// never runs past the reel or into the next note, a too short one is not drawn.
func TestNoteBlocks(t *testing.T) {
	t.Parallel()

	title := Note{Kind: ListTitleNote, Text: listTitle, Start: 2}
	item := func(text string, start float64) Note { return Note{Kind: ListItemNote, Text: text, Start: start} }

	tests := []struct {
		name     string
		notes    []Note
		hookEnd  float64
		reelEnd  float64
		expected []shownBlock
	}{
		{
			name:  "a list piles up and stays listTail after its last item",
			notes: []Note{item(itemWater, 4), title, item("2/ приседания", 6)},
			expected: []shownBlock{
				{2, 4, listTitle}, {4, 6, listOne}, {6, 11, listTwo},
			},
		},
		{
			name:     "a note waits for the hook",
			notes:    []Note{{Kind: CalloutNote, Text: calloutYears, Start: 3}},
			hookEnd:  5,
			expected: []shownBlock{{5, 7.5, calloutYears}},
		},
		{
			name: "a callout interrupts the list, the list comes back",
			notes: []Note{
				title, item(itemWater, 3), {Kind: CalloutNote, Text: "(каждый день)", Start: 4, End: 4.5},
				item("2/ приседания", 7),
			},
			expected: []shownBlock{
				{2, 3, listTitle}, {3, 4, listOne}, {4, 5.5, "(каждый день)"},
				{7, 12, listTwo},
			},
		},
		{
			name: "the earliest items scroll away",
			notes: []Note{
				title, item(itemWater, 3), item("2/ приседания", 4), item("3/ прогулка", 5), item("4/ завтрак", 6),
			},
			expected: []shownBlock{
				{2, 3, listTitle}, {3, 4, listOne}, {4, 5, listTwo},
				{5, 6, "3 вещи|1/ вода|2/ приседания|3/ прогулка"},
				{6, 11, "3 вещи|2/ приседания|3/ прогулка|4/ завтрак"},
			},
		},
		{
			name:     "an item numbered first after items starts a new list",
			notes:    []Note{title, item(itemWater, 3), item("2/ сон", 4), item("1/ бег", 6)},
			expected: []shownBlock{{2, 3, listTitle}, {3, 4, listOne}, {4, 6, "3 вещи|1/ вода|2/ сон"}, {6, 11, "1/ бег"}},
		},
		{
			name:     "an end of its own; the reel ends the last note",
			notes:    []Note{{Kind: ListTitleNote, Text: "итог", Start: 1, End: 2}, {Kind: CalloutNote, Text: "15 минут", Start: 9}},
			reelEnd:  10,
			expected: []shownBlock{{1, 2, "итог"}, {9, 10, "15 минут"}},
		},
		{
			name:     "too short and empty notes are not drawn",
			notes:    []Note{title, item(itemWater, 2.3), {Kind: CalloutNote, Text: " \u200b ", Start: 5}},
			expected: []shownBlock{{2.3, 7.3, listOne}},
		},
	}

	style := DefaultStyle()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			blocks := noteBlocks(tt.notes, tt.hookEnd, tt.reelEnd, &style)
			got := make([]shownBlock, 0, len(blocks))

			for _, bl := range blocks {
				got = append(got, shownBlock{start: bl.start, end: bl.end, entries: strings.Join(bl.entries, "|")})
			}

			assert.Equal(t, tt.expected, got)
		})
	}
}

// TestNoteFades: a block fades in after a gap and out before one; blocks that follow each other do not blink.
func TestNoteFades(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	blocks := noteBlocks([]Note{
		{Kind: ListTitleNote, Text: listTitle, Start: 1},
		{Kind: ListItemNote, Text: itemWater, Start: 2},
		{Kind: CalloutNote, Text: calloutYears, Start: 10},
	}, 0, 0, &style)

	require.Len(t, blocks, 3)
	assert.Equal(t, [2]bool{true, false}, [2]bool{blocks[0].fadeIn, blocks[0].fadeOut})
	assert.Equal(t, [2]bool{false, true}, [2]bool{blocks[1].fadeIn, blocks[1].fadeOut})
	assert.Equal(t, [2]bool{true, true}, [2]bool{blocks[2].fadeIn, blocks[2].fadeOut})
}

// TestNoteEvents: a block is one event in the Hook style, its entries on lines, markup escaped, fades only where they
// fade.
func TestNoteEvents(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	blocks := []noteBlock{
		{start: 2, end: 4, entries: []string{listTitle, "1/ стакан {\\b1}воды"}, lines: 2, fadeIn: true},
		{start: 4, end: 6, entries: []string{calloutYears}, lines: 1},
	}

	assert.Equal(t, "Dialogue: 2,0:00:02.00,0:00:04.00,Hook,,0,0,0,,{\\fad(150,0)}3 вещи\\N1/ стакан \\{b1\\}воды\n"+
		"Dialogue: 2,0:00:04.00,0:00:06.00,Hook,,0,0,0,,6 лет\n", noteEvents(blocks, &style))
}

// TestPhraseLayoutWithNotes: in the phrase layout the subtitles met by a note block stand under it, as under the hook,
// and the shade under them is continuous: one drawing at a time, a block that runs into the next joins it.
func TestPhraseLayoutWithNotes(t *testing.T) {
	t.Parallel()

	style := PhraseStyle()
	result := speech("Сегодня разберём три ошибки, которые все делают в планке.", "Первая — прогиб в пояснице.",
		"Вторая — задержка дыхания.")

	ass, _ := buildDocument(&CreateSubtitlesData{
		Transcript: result, Hook: hookPlank,
		Notes: []Note{
			{Kind: ListTitleNote, Text: "3 ошибки", Start: 3.5},
			{Kind: ListItemNote, Text: "1/ прогиб", Start: 4.2},
			{Kind: ListItemNote, Text: "2/ дыхание", Start: 6},
		},
	}, &style)

	assert.Contains(t, ass, ",Main,,0,0,430,,", "under a list of two lines")
	assert.Contains(t, ass, ",Main,,0,0,505,,", "under a list of three lines")

	end := -1.0

	for _, line := range strings.Split(ass, "\n") {
		if !strings.Contains(line, ",Shade,,") {
			continue
		}

		fields := strings.Split(line, ",")
		start := parseASSTime(t, fields[1])
		assert.GreaterOrEqual(t, start, end-1e-9, "the shades do not overlap: %s", line)
		end = parseASSTime(t, fields[2])
	}

	assert.Positive(t, end, "shaded")
}

// parseASSTime reads an ASS time H:MM:SS.CC in seconds.
func parseASSTime(t *testing.T, value string) float64 {
	t.Helper()

	parts := strings.Split(value, ":")
	require.Len(t, parts, 3)

	hours, err := strconv.Atoi(parts[0])
	require.NoError(t, err)

	minutes, err := strconv.Atoi(parts[1])
	require.NoError(t, err)

	seconds, err := strconv.ParseFloat(parts[2], 64)
	require.NoError(t, err)

	return float64(hours*3600+minutes*60) + seconds
}
