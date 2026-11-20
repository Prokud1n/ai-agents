package roughcut

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

func TestRemapTranscript(t *testing.T) {
	t.Parallel()

	src := &transcription.Result{
		Language: testLanguage,
		Duration: 10,
		Text:     "Привет, это тест. Ну, э, второе предложение.",
		Segments: []transcription.Segment{
			{Start: 1, End: 2.5, Text: " Привет, это тест."},
			{Start: 5, End: 8, Text: " Ну, э, второе предложение."},
		},
		Words: []transcription.Word{
			word("Привет", 1, 1.4), word("это", 1.5, 1.8), word("тест", 1.9, 2.4),
			word("Ну", 5, 5.3), word("э", 5.5, 5.9), word("второе", 6.2, 6.8), word("предложение", 6.9, 7.8),
		},
	}

	tests := []struct {
		name     string
		input    []Interval
		expected transcription.Result
	}{
		{
			name:  "words are shifted, a fully kept segment keeps its text",
			input: []Interval{{Start: 0.9, End: 2.5}, {Start: 4.9, End: 5.4}, {Start: 6.1, End: 7.9}},
			expected: transcription.Result{
				Language: testLanguage,
				Duration: 3.9,
				Text:     "Привет, это тест. Ну, второе предложение.",
				Segments: []transcription.Segment{
					{Start: 0.1, End: 1.5, Text: " Привет, это тест."},
					{Start: 1.7, End: 3.8, Text: "Ну, второе предложение."},
				},
				Words: []transcription.Word{
					word("Привет", 0.1, 0.5), word("это", 0.6, 0.9), word("тест", 1, 1.5),
					word("Ну", 1.7, 2), word("второе", 2.2, 2.8), word("предложение", 2.9, 3.8),
				},
			},
		},
		{
			name:  "a word cut in the middle is clipped to the interval",
			input: []Interval{{Start: 1.6, End: 2.2}},
			expected: transcription.Result{
				Language: testLanguage,
				Duration: 0.6,
				Text:     "Это тест.",
				Segments: []transcription.Segment{{Start: 0, End: 0.6, Text: "Это тест."}},
				Words:    []transcription.Word{word("это", 0, 0.2), word("тест", 0.3, 0.6)},
			},
		},
		{
			name:  "segments without kept words are dropped",
			input: []Interval{{Start: 6, End: 8}},
			expected: transcription.Result{
				Language: testLanguage,
				Duration: 2,
				Text:     "Второе предложение.",
				Segments: []transcription.Segment{{Start: 0.2, End: 1.8, Text: "Второе предложение."}},
				Words:    []transcription.Word{word("второе", 0.2, 0.8), word("предложение", 0.9, 1.8)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, RemapTranscript(src, tt.input))
		})
	}
}

func TestRemapTranscriptPunctuation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *transcription.Result
		keep     []Interval
		expected string
	}{
		{
			name: "question mark of a cut word moves to the word before",
			input: &transcription.Result{
				Segments: []transcription.Segment{{Start: 0, End: 3, Text: " Вы готовы, э?"}},
				Words:    []transcription.Word{word("Вы", 0, 0.5), word("готовы", 0.6, 1.2), word("э", 1.5, 2)},
			},
			keep:     []Interval{{Start: 0, End: 1.3}},
			expected: "Вы готовы?",
		},
		{
			name: "the start of the second sentence is cut: its first kept word gets the capital",
			input: &transcription.Result{
				Segments: []transcription.Segment{{Start: 0, End: 4, Text: " Готовы. Ну, э, смотрите сюда."}},
				Words: []transcription.Word{
					word("Готовы", 0, 0.5), word("Ну", 1, 1.2), word("э", 1.3, 1.5), word("смотрите", 2, 2.5), word("сюда", 2.6, 3),
				},
			},
			keep:     []Interval{{Start: 0, End: 0.6}, {Start: 1.9, End: 3.1}},
			expected: "Готовы. Смотрите сюда.",
		},
		{
			name: "a dash between words is dropped, the rest stays",
			input: &transcription.Result{
				Segments: []transcription.Segment{{Start: 0, End: 3, Text: " Присед — э, база."}},
				Words:    []transcription.Word{word("Присед", 0, 0.5), word("э", 0.8, 1), word("база", 1.5, 2)},
			},
			keep:     []Interval{{Start: 0, End: 0.6}, {Start: 1.4, End: 2.1}},
			expected: "Присед база.",
		},
		{
			name: "words that do not match the text fall back to plain words",
			input: &transcription.Result{
				Segments: []transcription.Segment{{Start: 0, End: 3, Text: " Делаем 10 раз, э."}},
				Words:    []transcription.Word{word("Делаем", 0, 0.5), word("десять", 0.6, 1), word("раз", 1.1, 1.4), word("э", 1.6, 2)},
			},
			keep:     []Interval{{Start: 0, End: 1.5}},
			expected: "Делаем десять раз",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := RemapTranscript(tt.input, tt.keep)

			assert.Equal(t, tt.expected, got.Text)
		})
	}
}
