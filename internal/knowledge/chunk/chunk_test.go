package chunk

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []Chunk
	}{
		{
			name:     "empty text",
			input:    " \n\n \n",
			expected: []Chunk{},
		},
		{
			name:  "text without headings",
			input: "Первый абзац.\n\nВторой абзац.",
			expected: []Chunk{
				{Heading: "", Text: "Первый абзац.\n\nВторой абзац.", Tokens: 10},
			},
		},
		{
			name: "sections keep the heading path",
			input: "# Бренд\n\nВступление.\n\n## Питание\n\nГоворим о питании без запретов.\n\n" +
				"### Белок\n\nБелок в каждом приёме пищи.\n\n## Тренировки\n\nТри раза в неделю.",
			expected: []Chunk{
				{Heading: "Бренд", Text: "Бренд\n\nВступление.", Tokens: 6},
				{
					Heading: "Бренд > Питание",
					Text:    "Бренд > Питание\n\nГоворим о питании без запретов.",
					Tokens:  16,
				},
				{
					Heading: "Бренд > Питание > Белок",
					Text:    "Бренд > Питание > Белок\n\nБелок в каждом приёме пищи.",
					Tokens:  18,
				},
				{
					Heading: "Бренд > Тренировки",
					Text:    "Бренд > Тренировки\n\nТри раза в неделю.",
					Tokens:  13,
				},
			},
		},
		{
			name:  "heading without body is skipped, deeper levels are reset",
			input: "# A\n## B\n### C\n## D\n\ntext",
			expected: []Chunk{
				{Heading: "A > D", Text: "A > D\n\ntext", Tokens: 4},
			},
		},
		{
			name:  "hash inside code fence is not a heading",
			input: "## Код\n\n```\n# comment\n```",
			expected: []Chunk{
				{Heading: "Код", Text: "Код\n\n```\n# comment\n```", Tokens: 8},
			},
		},
		{
			name:  "unclosed fence does not hide later headings",
			input: "## Код\n\n```go\nx := 1\n\n## Питание\n\nБелок.",
			expected: []Chunk{
				{Heading: "Код", Text: "Код\n\n```go\nx := 1", Tokens: 6},
				{Heading: "Питание", Text: "Питание\n\nБелок.", Tokens: 5},
			},
		},
		{
			name:  "windows line endings and trailing hashes",
			input: "## Голос ##\r\n\r\nНа ты, тепло.\r\n",
			expected: []Chunk{
				{Heading: "Голос", Text: "Голос\n\nНа ты, тепло.", Tokens: 7},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Split(tt.input)

			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestSplitLongSection(t *testing.T) {
	t.Parallel()

	const headingPrefix = "Вода\n\n"

	paragraph := strings.Repeat("Пейте воду до тренировки и после неё. ", 20) // ~760 runes
	text := "## Вода\n\n" + strings.Repeat(paragraph+"\n\n", 10)

	chunks := Split(text)

	require.Greater(t, len(chunks), 1)

	for i, c := range chunks {
		assert.Equal(t, "Вода", c.Heading)
		assert.True(t, strings.HasPrefix(c.Text, headingPrefix), "chunk %d keeps the heading", i)
		assert.LessOrEqual(t, utf8.RuneCountInString(c.Text), TargetRunes+OverlapRunes+utf8.RuneCountInString(headingPrefix))
	}

	// Each next chunk starts with the tail of the previous one.
	for i := 1; i < len(chunks); i++ {
		prev := strings.TrimPrefix(chunks[i-1].Text, headingPrefix)
		next := strings.TrimPrefix(chunks[i].Text, headingPrefix)
		overlap := strings.SplitN(next, "\n\n", 2)[0]

		assert.True(t, strings.HasSuffix(prev, overlap), "chunk %d overlaps the previous one", i)
	}
}

func TestHeadingPath(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("я", MaxHeadingRunes+10)
	mid := strings.Repeat("б", MaxHeadingRunes-12)

	tests := []struct {
		name     string
		input    []string
		expected string
	}{
		{name: "empty levels skipped", input: []string{"A", "", "C"}, expected: "A > C"},
		{name: "top levels dropped", input: []string{"Бренд", "Раздел", mid}, expected: "Раздел > " + mid},
		{name: "single long heading cut", input: []string{"A", long}, expected: string([]rune(long)[:MaxHeadingRunes-1]) + "…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := headingPath(tt.input)

			assert.Equal(t, tt.expected, got)
			assert.LessOrEqual(t, utf8.RuneCountInString(got), MaxHeadingRunes)
		})
	}
}

func TestSplitLong(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		limit    int
		expected []string
	}{
		{name: "short text is kept", input: "Один. Два.", limit: 20, expected: []string{"Один. Два."}},
		{
			name:     "split by sentences",
			input:    "Первое предложение. Второе! Третье?",
			limit:    20,
			expected: []string{"Первое предложение.", "Второе! Третье?"},
		},
		{
			name:     "long sentence is split by words",
			input:    "раз два три четыре пять",
			limit:    10,
			expected: []string{"раз два", "три четыре", "пять"},
		},
		{
			name:     "huge word is split by runes",
			input:    "абвгдеёжзи",
			limit:    4,
			expected: []string{"абвг", "деёж", "зи"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, splitLong(tt.input, tt.limit))
		})
	}
}

func TestTailWords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		limit    int
		expected string
	}{
		{name: "zero limit", input: "a b c", limit: 0, expected: ""},
		{name: "short text is whole", input: "a b", limit: 10, expected: "a b"},
		{name: "partial word is dropped", input: "один два три", limit: 6, expected: "три"},
		{name: "cut at a boundary", input: "один два три", limit: 8, expected: "два три"},
		{name: "single long word", input: "сверхдлинноеслово", limit: 5, expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, tailWords(tt.input, tt.limit))
		})
	}
}

func TestTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "first h1", input: "intro\n## Sub\n# Голос бренда\n# Other", expected: "Голос бренда"},
		{name: "no h1", input: "## Sub\ntext", expected: ""},
		{name: "empty", input: "", expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, Title(tt.input))
		})
	}
}

func TestEstimateTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{name: "empty", input: "", expected: 0},
		{name: "rounds up", input: "абвг", expected: 2},
		{name: "exact", input: "абв", expected: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, EstimateTokens(tt.input))
		})
	}
}
