package transcription

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Prokud1n/ai-agents/internal/providers/whisper"
)

func TestDropHallucinations(t *testing.T) {
	t.Parallel()

	const (
		speechText = " Держим спину."
		countText  = " Раз, два, три, четыре. Раз, два, три, четыре. Раз, два, три, четыре. Раз, два, три, четыре."
	)

	speech := whisper.Segment{Start: 0, End: 2, Text: speechText, NoSpeechProb: 0.01, AvgLogprob: -0.3, CompressionRatio: 1.1}
	// Quiet but confidently recognized speech is kept: no_speech_prob alone is not enough.
	quietSpeech := whisper.Segment{Start: 2, End: 4, Text: " Тихо.", NoSpeechProb: 0.7, AvgLogprob: -0.5, CompressionRatio: 1}
	music := whisper.Segment{Start: 4, End: 8, Text: " Субтитры сделал DimaTorzok", NoSpeechProb: 0.8, AvgLogprob: -1.2, CompressionRatio: 1}
	// A repetition loop decoded with low confidence: what Whisper produces when it gets stuck.
	stuckLoop := whisper.Segment{Start: 8, End: 12, Text: " раз раз раз раз раз раз", NoSpeechProb: 0.1, AvgLogprob: -1.4, CompressionRatio: 3.2}
	// A loop over silence, decoded confidently.
	silentLoop := whisper.Segment{Start: 12, End: 16, Text: " Спасибо. Спасибо. Спасибо.", NoSpeechProb: 0.9, AvgLogprob: -0.4, CompressionRatio: 2.6}
	// The trainer counting reps: compresses well, but is real, confidently decoded speech.
	count := whisper.Segment{Start: 16, End: 24, Text: countText, NoSpeechProb: 0.02, AvgLogprob: -0.25, CompressionRatio: 2.9}

	words := []whisper.Word{
		{Word: "Держим", Start: 0.1, End: 0.8}, {Word: wordBack, Start: 0.8, End: 1.5},
		{Word: "Тихо", Start: 2.5, End: 3},
		{Word: "Субтитры", Start: 4.5, End: 5.5},
		{Word: wordOne, Start: 8.1, End: 8.4},
		{Word: "Спасибо", Start: 12.5, End: 13},
		{Word: "Раз", Start: 16.1, End: 16.4}, {Word: "два", Start: 16.5, End: 16.8},
	}

	tests := []struct {
		name            string
		input           whisper.Transcript
		expected        whisper.Transcript
		expectedDropped []whisper.Segment
	}{
		{
			name:            "nothing to drop",
			input:           whisper.Transcript{Text: speechText, Words: words[:2], Segments: []whisper.Segment{speech}},
			expected:        whisper.Transcript{Text: speechText, Words: words[:2], Segments: []whisper.Segment{speech}},
			expectedDropped: []whisper.Segment{},
		},
		{
			name: "music and poorly decoded or silent loops are dropped with their words",
			input: whisper.Transcript{
				Language: langRussian, Text: "all", Words: words[:6],
				Segments: []whisper.Segment{speech, quietSpeech, music, stuckLoop, silentLoop},
			},
			expected: whisper.Transcript{
				Language: langRussian, Text: "Держим спину. Тихо.", Words: words[:3],
				Segments: []whisper.Segment{speech, quietSpeech},
			},
			expectedDropped: []whisper.Segment{music, stuckLoop, silentLoop},
		},
		{
			name: "confident count «раз, два, три, четыре» ×4 is kept despite the compression ratio",
			input: whisper.Transcript{
				Language: langRussian, Text: countText, Words: words[6:],
				Segments: []whisper.Segment{count},
			},
			expected: whisper.Transcript{
				Language: langRussian, Text: countText, Words: words[6:],
				Segments: []whisper.Segment{count},
			},
			expectedDropped: []whisper.Segment{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, dropped := dropHallucinations(&tt.input)

			assert.Equal(t, tt.expected, got)
			assert.Equal(t, tt.expectedDropped, dropped)
		})
	}
}

func TestIsHallucination(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    whisper.Segment
		expected bool
	}{
		{name: "clean speech", input: whisper.Segment{NoSpeechProb: 0.02, AvgLogprob: -0.3, CompressionRatio: 1.2}},
		{name: "count of reps", input: whisper.Segment{NoSpeechProb: 0.02, AvgLogprob: -0.25, CompressionRatio: 2.9}},
		{name: "rhythmic cues «и вверх, и вниз» ×5", input: whisper.Segment{NoSpeechProb: 0.05, AvgLogprob: -0.4, CompressionRatio: 3.56}},
		{name: "no speech alone", input: whisper.Segment{NoSpeechProb: 0.9, AvgLogprob: -0.5, CompressionRatio: 1}},
		{name: "low confidence alone", input: whisper.Segment{NoSpeechProb: 0.1, AvgLogprob: -1.2, CompressionRatio: 1}},
		{name: "silence with low confidence", input: whisper.Segment{NoSpeechProb: 0.7, AvgLogprob: -1.1, CompressionRatio: 1}, expected: true},
		{name: "loop with low confidence", input: whisper.Segment{NoSpeechProb: 0.1, AvgLogprob: -1.3, CompressionRatio: 2.5}, expected: true},
		{name: "loop over silence", input: whisper.Segment{NoSpeechProb: 0.8, AvgLogprob: -0.4, CompressionRatio: 2.6}, expected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, isHallucination(tt.input))
		})
	}
}

func TestTruncateText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		n        int
		expected string
	}{
		{name: "short", input: "Раз, два", n: 10, expected: "Раз, два"},
		{name: "cut by runes, not bytes", input: "Продолжение следует", n: 11, expected: "Продолжение…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, truncateText(tt.input, tt.n))
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
