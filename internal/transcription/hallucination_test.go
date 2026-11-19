package transcription

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Prokud1n/ai-agents/internal/providers/whisper"
)

func TestDropHallucinations(t *testing.T) {
	t.Parallel()

	const speechText = " Держим спину."

	speech := whisper.Segment{Start: 0, End: 2, Text: speechText, NoSpeechProb: 0.01, AvgLogprob: -0.3, CompressionRatio: 1.1}
	// Quiet but confidently recognized speech is kept: no_speech_prob alone is not enough.
	quietSpeech := whisper.Segment{Start: 2, End: 4, Text: " Тихо.", NoSpeechProb: 0.7, AvgLogprob: -0.5, CompressionRatio: 1}
	music := whisper.Segment{Start: 4, End: 8, Text: " Субтитры сделал DimaTorzok", NoSpeechProb: 0.8, AvgLogprob: -1.2, CompressionRatio: 1}
	loop := whisper.Segment{Start: 8, End: 12, Text: " раз раз раз раз раз раз", NoSpeechProb: 0.1, AvgLogprob: -0.2, CompressionRatio: 3.2}

	words := []whisper.Word{
		{Word: "Держим", Start: 0.1, End: 0.8}, {Word: wordBack, Start: 0.8, End: 1.5},
		{Word: "Тихо", Start: 2.5, End: 3},
		{Word: "Субтитры", Start: 4.5, End: 5.5},
		{Word: wordOne, Start: 8.1, End: 8.4},
	}

	tests := []struct {
		name     string
		input    whisper.Transcript
		expected whisper.Transcript
	}{
		{
			name:     "nothing to drop",
			input:    whisper.Transcript{Text: speechText, Words: words[:2], Segments: []whisper.Segment{speech}},
			expected: whisper.Transcript{Text: speechText, Words: words[:2], Segments: []whisper.Segment{speech}},
		},
		{
			name: "music and a repetition loop are dropped with their words",
			input: whisper.Transcript{
				Language: langRussian, Text: "all", Words: words,
				Segments: []whisper.Segment{speech, quietSpeech, music, loop},
			},
			expected: whisper.Transcript{
				Language: langRussian, Text: "Держим спину. Тихо.", Words: words[:3],
				Segments: []whisper.Segment{speech, quietSpeech},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, dropHallucinations(&tt.input))
		})
	}
}

func TestLanguages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		input         string
		expectedISO   string
		expectedCode  string
		expectedValid bool
	}{
		{name: "code", input: langRU, expectedISO: langRU, expectedCode: langRU, expectedValid: true},
		{name: "upper case code with spaces", input: " EN ", expectedISO: "en", expectedCode: "en", expectedValid: true},
		{name: "whisper name", input: langRussian, expectedISO: langRU, expectedCode: langRussian},
		{name: "three-letter code", input: "Cantonese", expectedISO: "yue", expectedCode: "cantonese"},
		{name: "unknown", input: "klingon", expectedISO: "", expectedCode: "klingon"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code, ok := normalizeLanguage(tt.input)

			assert.Equal(t, tt.expectedISO, isoLanguage(tt.input))
			assert.Equal(t, tt.expectedCode, code)
			assert.Equal(t, tt.expectedValid, ok)
		})
	}
}
