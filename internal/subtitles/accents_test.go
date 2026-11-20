package subtitles

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func accentsOf(tokens []token) []accentKind {
	out := make([]accentKind, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, t.Accent)
	}

	return out
}

func TestMarkAccents(t *testing.T) {
	t.Parallel()

	words := func(ws ...string) []token {
		out := make([]token, 0, len(ws))
		for i, w := range ws {
			out = append(out, tok(w, float64(i), float64(i)+1))
		}

		return out
	}

	tests := []struct {
		name       string
		input      []token
		explicit   []string
		dictionary []string
		expected   []accentKind
	}{
		{
			name:     "number with its unit",
			input:    words(wDelaem, "10", wPovtoreniy),
			expected: []accentKind{noAccent, numberAccent, numberAccent},
		},
		{
			name:     "number word with its unit",
			input:    words(wTri, wPodhoda),
			expected: []accentKind{numberAccent, numberAccent},
		},
		{
			name:     "two number groups in a row get one accent",
			input:    words("3", "подхода", "10", "повторений"),
			expected: []accentKind{numberAccent, numberAccent, noAccent, noAccent},
		},
		{
			name:     "compound number with its unit",
			input:    words("двадцать", wPyat, wPovtoreniy),
			expected: []accentKind{numberAccent, numberAccent, numberAccent},
		},
		{
			name:     "counting words is not a quantity",
			input:    words(wordOne, wordTwo, wTri, wChetyre, wPyat, "шесть", wSem, wVosem, wordHold),
			expected: []accentKind{noAccent, noAccent, noAccent, noAccent, noAccent, noAccent, noAccent, noAccent, noAccent},
		},
		{
			name:     "counting digits is not a quantity",
			input:    words("1", "2", "3", "4", wPodhoda),
			expected: []accentKind{noAccent, noAccent, noAccent, noAccent, noAccent},
		},
		{
			name:     "number before a preposition is not accented",
			input:    words("один", "из", "вариантов"),
			expected: []accentKind{noAccent, noAccent, noAccent},
		},
		{
			name:     "number before a comma is not accented",
			input:    []token{withBreak(tok("10,", 0, 1), softBreak), tok("потом", 1, 2)},
			expected: []accentKind{noAccent, noAccent},
		},
		{
			name:       "dictionary stem matches inflected forms only",
			input:      words(wordHold, "ягодицами", wI, "прессинговать"),
			dictionary: []string{"Ягодиц", "пресс"},
			expected:   []accentKind{noAccent, dictionaryAccent, noAccent, noAccent},
		},
		{
			name:       "short entry matches only exactly",
			input:      words(wVes, "весь", "весна", "веса"),
			dictionary: []string{wVes},
			expected:   []accentKind{dictionaryAccent, noAccent, noAccent, noAccent},
		},
		{
			name:       "dictionary phrase",
			input:      words("становая", "тяга", wTyanem),
			dictionary: []string{"становая тяга"},
			expected:   []accentKind{dictionaryAccent, dictionaryAccent, noAccent},
		},
		{
			name:       "explicit list replaces the heuristics",
			input:      words(wDelaem, "10", wPovtoreniy, wordKnees),
			explicit:   []string{wordKnees},
			dictionary: []string{wPovtoreniy},
			expected:   []accentKind{noAccent, noAccent, noAccent, explicitAccent},
		},
		{
			name:     "explicit phrase with punctuation and yo",
			input:    []token{tok("Ещё", 0, 1), withBreak(tok("раз!", 1, 2), strongBreak)},
			explicit: []string{"ещё раз!"},
			expected: []accentKind{explicitAccent, explicitAccent},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			before := accentsOf(tt.input)
			got := markAccents(tt.input, tt.explicit, tt.dictionary)

			assert.Equal(t, tt.expected, accentsOf(got))
			assert.Equal(t, before, accentsOf(tt.input), "input is not changed")
		})
	}
}

func TestSelectAccents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     []token
		maxPerCue int
		longest   bool
		expected  []accentKind
	}{
		{
			name:      "longest meaningful word of 7+ letters",
			input:     []token{tok("потому", 0, 1), tok("правильно", 1, 2), tok(wordBack, 2, 3)},
			maxPerCue: 1, longest: true,
			expected: []accentKind{noAccent, longestAccent, noAccent},
		},
		{
			name:      "routine commands and stop words are not accented",
			input:     []token{tok("опускаемся", 0, 1), tok(wMedlenno, 1, 2), tok("которые", 2, 3)},
			maxPerCue: 1, longest: true,
			expected: []accentKind{noAccent, longestAccent, noAccent},
		},
		{
			name:      "first of equally long words",
			input:     []token{tok("лопатки", 0, 1), tok("коленям", 1, 2)},
			maxPerCue: 1, longest: true,
			expected: []accentKind{longestAccent, noAccent},
		},
		{
			name:      "words shorter than 7 letters are not accented",
			input:     []token{tok(wordHold, 0, 1), tok("колено", 1, 2)},
			maxPerCue: 1, longest: true,
			expected: []accentKind{noAccent, noAccent},
		},
		{
			name:      "longest word turned off",
			input:     []token{tok("приседания", 0, 1)},
			maxPerCue: 1,
			expected:  []accentKind{noAccent},
		},
		{
			name: "number beats dictionary and longest word",
			input: []token{
				withAccent(tok(wYagoditsy, 0, 1), dictionaryAccent), tok("приседаниями", 1, 2),
				withAccent(tok("3", 2, 3), numberAccent), withAccent(tok(wPodhoda, 3, 4), numberAccent),
			},
			maxPerCue: 1, longest: true,
			expected: []accentKind{noAccent, noAccent, numberAccent, numberAccent},
		},
		{
			name: "limit of two groups",
			input: []token{
				withAccent(tok("3", 0, 1), numberAccent), withAccent(tok(wPodhoda, 1, 2), numberAccent),
				withAccent(tok(wYagoditsy, 2, 3), dictionaryAccent), withAccent(tok("пресс", 3, 4), dictionaryAccent),
			},
			maxPerCue: 2, longest: true,
			// adjacent dictionary words are one group
			expected: []accentKind{numberAccent, numberAccent, dictionaryAccent, dictionaryAccent},
		},
		{
			name:      "zero limit turns automatic accents off",
			input:     []token{withAccent(tok(wYagoditsy, 0, 1), dictionaryAccent), tok("приседаниями", 1, 2)},
			maxPerCue: 0, longest: true,
			expected: []accentKind{noAccent, noAccent},
		},
		{
			name:      "explicit accents are not limited",
			input:     []token{withAccent(tok(wordKnees, 0, 1), explicitAccent), withAccent(tok("наружу", 1, 2), explicitAccent)},
			maxPerCue: 0,
			expected:  []accentKind{explicitAccent, explicitAccent},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, accentsOf(selectAccents(tt.input, tt.maxPerCue, tt.longest)))
		})
	}
}
