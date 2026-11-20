package roughcut

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizeWord(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "case and punctuation", input: " Давайте,", expected: "давайте"},
		{name: "yo is e", input: "Ещё", expected: "еще"},
		{name: "hyphen inside", input: "э-э", expected: "ээ"},
		{name: "digits", input: "10.", expected: "10"},
		{name: "only punctuation", input: "—", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, normalizeWord(tt.input))
		})
	}
}

func TestFillerSet(t *testing.T) {
	t.Parallel()

	fillers := newFillerSet([]string{testFiller, "Эм", unitFiller, "хм", " "})

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "filler", input: testFiller, expected: true},
		{name: "stretched filler", input: "Эээээ...", expected: true},
		{name: "hyphenated filler", input: "э-э", expected: true},
		{name: "stretched hum", input: "ммм", expected: true},
		{name: "mixed filler", input: "эммм", expected: true},
		{name: "hm", input: "Хм,", expected: true},
		{name: "particle nu is a word", input: "ну", expected: false},
		{name: "conjunction a is a word", input: "а", expected: false},
		{name: "stretched a is not in the list", input: "ааа", expected: false},
		{name: "pronoun eto is a word", input: "это", expected: false},
		{name: "empty", input: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, fillers.has(normalizeWord(tt.input)))
		})
	}
}

func TestIsTruncation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		fragment string
		word     string
		expected bool
	}{
		{name: "broken off word", fragment: "при", word: "приступим", expected: true},
		{name: "two letters are enough", fragment: "но", word: "носков", expected: true},
		{name: "one letter is not", fragment: "п", word: "приступим", expected: false},
		{name: "same word", fragment: "присед", word: "присед", expected: false},
		{name: "other word", fragment: "при", word: "делаем", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, isTruncation(tt.fragment, tt.word))
		})
	}
}
