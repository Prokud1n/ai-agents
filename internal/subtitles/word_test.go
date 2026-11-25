package subtitles

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// Texts of the word tests.
const (
	tenRepeats   = "10 повторений"
	wordHello    = "привет"
	overlapsHook = "overlaps the hook"
)

// TestWordCues: the word layout shows one word at a time, in small letters, without the punctuation of its sentence;
// a number with its unit is one cue, a word said too quickly is shown with the next one, never across a pause.
func TestWordCues(t *testing.T) {
	t.Parallel()

	quick := &transcription.Result{
		Segments: []transcription.Segment{{Start: 1, End: 1.8, Text: "Раз и два."}},
		Words: []transcription.Word{
			{Word: "Раз", Start: 1, End: 1.08}, {Word: "и", Start: 1.1, End: 1.2}, {Word: "два", Start: 1.5, End: 1.8},
		},
	}
	paused := &transcription.Result{
		Segments: []transcription.Segment{{Start: 1, End: 1.1, Text: "Да."}, {Start: 1.12, End: 1.4, Text: "Нет."}},
		Words:    []transcription.Word{{Word: "Да", Start: 1, End: 1.1}, {Word: "Нет", Start: 1.12, End: 1.4}},
	}

	tests := []struct {
		name     string
		input    *transcription.Result
		expected []string
	}{
		{
			name:     "a word at a time, small letters, no sentence punctuation",
			input:    speech("Привет, друзья!", "«Делаем» присед."),
			expected: []string{wordHello, "друзья!", wDelaem, wPrised},
		},
		{
			name:     "a number with its unit is one cue",
			input:    speech("Делаем 10 повторений и 3 подхода"),
			expected: []string{wDelaem, tenRepeats, wI, "3 подхода"},
		},
		{name: "a quick word goes with the next one", input: quick, expected: []string{"раз и", "два"}},
		{name: "not across the end of a sentence", input: paused, expected: []string{"да", "нет"}},
		{name: "no speech", input: &transcription.Result{Duration: 3}, expected: []string{}},
	}

	style := DefaultStyle()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := make([]string, 0, len(tt.expected))
			for _, w := range wordCues(tt.input, &style) {
				got = append(got, w.Text)
			}

			assert.Equal(t, tt.expected, got)
		})
	}
}

// TestTimeWords: a word appears leadIn before it is said and stays until the next one (no blinking); before a pause
// it stays hold after it, at least min_duration, at most maxWordShown however long Whisper timed it.
func TestTimeWords(t *testing.T) {
	t.Parallel()

	input := []wordCue{
		{Text: "a", Start: 1, End: 1.3},
		{Text: "b", Start: 1.35, End: 1.6, Pause: true},
		{Text: "c", Start: 3, End: 3.02, Pause: true},
		{Text: "d", Start: 5, End: 9, Pause: true},
	}
	expected := []wordCue{
		{Text: "a", Start: 0.95, End: 1.3},
		{Text: "b", Start: 1.3, End: 1.7, Pause: true},
		{Text: "c", Start: 2.95, End: 3.12, Pause: true},
		{Text: "d", Start: 4.95, End: 6.45, Pause: true},
	}

	got := timeWords(input, DefaultStyle().WordBox.MinDuration)
	require.Len(t, got, len(expected))

	for i := range expected {
		assert.Equal(t, expected[i].Text, got[i].Text)
		assert.InDelta(t, expected[i].Start, got[i].Start, 1e-9, expected[i].Text)
		assert.InDelta(t, expected[i].End, got[i].End, 1e-9, expected[i].Text)
	}

	assert.InDelta(t, 1.35, input[1].Start, 1e-9, "the input is not changed")
}

// TestWordEvents: a word is one line on the plate (\q2), a word wider than the plate is squeezed, markup is text.
func TestWordEvents(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()

	tests := []struct {
		name     string
		input    wordCue
		expected string
	}{
		{
			name:     "a word",
			input:    wordCue{Text: wordHello, Start: 1, End: 1.5},
			expected: "Dialogue: 0,0:00:01.00,0:00:01.50,Word,,0,0,0,,{\\q2}привет\n",
		},
		{
			name:     "a long word is squeezed",
			input:    wordCue{Text: "электрокардиографического", Start: 0, End: 1},
			expected: "Dialogue: 0,0:00:00.00,0:00:01.00,Word,,0,0,0,,{\\q2\\fscx88}электрокардиографического\n",
		},
		{
			name:     "markup is text",
			input:    wordCue{Text: "{\\an7}", Start: 0, End: 1},
			expected: "Dialogue: 0,0:00:00.00,0:00:01.00,Word,,0,0,0,,{\\q2}\\{an7\\}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, wordEvents([]wordCue{tt.input}, &style))
		})
	}
}

// TestWordStyleLine: black text on an opaque white plate (BorderStyle 3, the padding as its outline), placed by its
// top left corner inside the plate.
func TestWordStyleLine(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()

	assert.Equal(t, "Style: Word,Manrope Medium,44,&H00111111,&H00111111,&H00FFFFFF,&H00FFFFFF,0,0,0,0,100,100,0,0,"+
		"3,10,0,7,150,390,610,1", wordStyleLine(&style))
}

// TestValidateWordBox: the plate stays clear of the Instagram zones, the hook and the notes, the titles; its look is
// checked; the phrase layout does not place it.
func TestValidateWordBox(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		phrase      bool
		modify      func(s *Style)
		expectedErr string
	}{
		{name: "the default fits", modify: func(*Style) {}},
		{name: "over the hook", modify: func(s *Style) { s.WordBox.Y = 500 }, expectedErr: overlapsHook},
		{name: "under the buttons", modify: func(s *Style) { s.WordBox.X = 500 }, expectedErr: "Instagram zones"},
		{name: "over the titles", modify: func(s *Style) { s.WordBox.Y = 1300 }, expectedErr: "exercise titles"},
		{name: "translucent plate", modify: func(s *Style) { s.WordBox.Box = "#FFFFFF40" }, expectedErr: "half opaque"},
		{
			name:        "small and capital letters",
			modify:      func(s *Style) { s.WordBox.Uppercase = true },
			expectedErr: "exclude each other",
		},
		{name: "no min duration", modify: func(s *Style) { s.WordBox.MinDuration = 0 }, expectedErr: "min_duration"},
		{name: "the phrase layout does not place it", phrase: true, modify: func(s *Style) { s.WordBox.Y = 500 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			style := DefaultStyle()
			if tt.phrase {
				style = PhraseStyle()
			}

			tt.modify(&style)

			err := style.Validate()
			if tt.expectedErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrInvalidStyle)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

// TestBuildDocumentWordLayout: the word layout draws the words on their plate, the hook, the notes after it, the
// handle and the card; no phrase subtitles, no shade.
func TestBuildDocumentWordLayout(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	result := speech("Привет! Сегодня три ошибки в планке.", "Первая — прогиб в пояснице.")
	result.Duration = 10

	ass, cues := buildDocument(&CreateSubtitlesData{
		Transcript: result, Hook: hookPlank,
		Notes: []Note{
			{Kind: ListTitleNote, Text: "3 ошибки", Start: 1},
			{Kind: ListItemNote, Text: "1/ прогиб в пояснице", Start: 3},
		},
		Branding: &Branding{Handle: "@prokudina.ana", Icon: true, EndCard: true},
	}, &style)

	assert.Equal(t, strings.Count(ass, ",Word,,"), cues)
	assert.Equal(t, 9, cues, "a word or a number with its unit at a time")
	assert.Contains(t, ass, ",Word,,0,0,0,,{\\q2}привет!\n")
	assert.Contains(t, ass, ",Word,,0,0,0,,{\\q2}три ошибки\n")
	assert.NotContains(t, ass, ",Main,,")
	assert.NotContains(t, ass, "Style: Shade,")
	assert.Contains(t, ass, "Dialogue: 2,0:00:00.00,0:00:02.60,Hook,,0,0,0,,{\\fad(150,150)}Три ошибки в планке\n",
		"the hook until half a second after the first phrase")
	assert.Contains(t, ass, "Dialogue: 2,0:00:03.00,0:00:08.00,Hook,,0,0,0,,{\\fad(150,150)}3 ошибки\\N1/ прогиб в "+
		"пояснице\n", "the list waits for the hook; its title alone would be too short, it comes with the item")
	assert.Contains(t, ass, "Dialogue: 3,0:00:00.00,0:00:08.00,Handle,,0,0,0,,{\\q2\\fad(0,500)}")
	assert.Contains(t, ass, "Dialogue: 4,0:00:08.00,0:00:10.00,Card,,")
}
