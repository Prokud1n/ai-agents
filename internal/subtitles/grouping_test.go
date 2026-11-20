package subtitles

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

// seq makes tokens spoken one after another without pauses (0.2 s each, 0.05 s apart).
func seq(words ...string) []token {
	out := make([]token, 0, len(words))
	at := 0.0

	for _, w := range words {
		t := tok(w, at, at+0.2)
		t.Break = breakAfter(w)
		out = append(out, t)
		at += 0.25
	}

	return out
}

func unitTexts(units []unit) []string {
	out := make([]string, 0, len(units))
	for _, u := range units {
		out = append(out, strings.Join(texts(u.tokens), " "))
	}

	return out
}

func TestBuildUnits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []token
		maxChars int
		expected []string
	}{
		{name: "preposition goes with the next word", input: seq(wordHold, wV, "руках"), expected: []string{wordHold, "в руках"}},
		{name: "chain of function words", input: seq(wI, wNe, wV, "зал"), expected: []string{"и не в зал"}},
		{name: "number with its unit", input: seq(wDelaem, "10", wPovtoreniy), expected: []string{wDelaem, "10 повторений"}},
		{name: "particle stays with the previous word", input: seq(wordHold, "же", wordBack), expected: []string{"держим же", wordBack}},
		{name: "nothing glued across a comma", input: seq("и,", "наконец"), expected: []string{"и,", "наконец"}},
		{name: "counting words are not glued", input: seq(wordOne, wordTwo, wTri, wChetyre), expected: []string{wordOne, wordTwo, wTri, wChetyre}},
		{name: "counting digits are not glued", input: seq("1", "2", "3", "4"), expected: []string{"1", "2", "3", "4"}},
		{name: "compound number with its unit", input: seq("двадцать", wPyat, wPovtoreniy), expected: []string{"двадцать пять повторений"}},
		{name: "last count goes with the next word", input: seq(wSem, wVosem, wordHold), expected: []string{wSem, "восемь держим"}},
		{
			name: "unit longer than a line is split into words", input: seq(wI, wNe, wV, "спортзал"), maxChars: 10,
			expected: []string{wI, wNe, wV, "спортзал"},
		},
		{
			name:     "nothing glued across a pause",
			input:    []token{tok(wV, 0, 0.2), tok("зале", 0.6, 0.9)},
			expected: []string{wV, "зале"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			maxChars := tt.maxChars
			if maxChars == 0 {
				maxChars = 40
			}

			assert.Equal(t, tt.expected, unitTexts(buildUnits(tt.input, maxChars)))
		})
	}
}

func TestGroupCues(t *testing.T) {
	t.Parallel()

	limits := layoutLimits{maxLines: 2, maxLineChars: 20}

	tests := []struct {
		name     string
		input    []token
		limits   layoutLimits
		expected [][]string
	}{
		{
			name:     "sentence end always breaks",
			input:    seq(wPrivetCapExcl, wSegodnyaCap, "присед."),
			limits:   limits,
			expected: [][]string{{wPrivetCapExcl}, {wSegodnyaCap, "присед."}},
		},
		{
			name:     "pause breaks",
			input:    []token{tok(wordOne, 0, 0.3), tok(wordTwo, 0.35, 0.6), tok(wTri, 1.0, 1.3)},
			limits:   limits,
			expected: [][]string{{wordOne, wordTwo}, {wTri}},
		},
		{
			name:     "comma breaks once half a line is filled",
			input:    seq(wordLower, wMedlennoComma, wPodnimaemsya, wBystro, wI, wPlavno),
			limits:   limits,
			expected: [][]string{{wordLower, wMedlennoComma}, {wPodnimaemsya, wBystro, wI, wPlavno}},
		},
		{
			name:     "short tail after a comma is not split off",
			input:    seq(wRazCapComma, wDvaComma, "три,", "четыре!"),
			limits:   limits,
			expected: [][]string{{wRazCapComma, wDvaComma, "три,", "четыре!"}},
		},
		{
			name:     "tail that fits a line is not split off",
			input:    seq(wordLower, wMedlennoComma, wPodnimaemsya, wBystro),
			limits:   limits,
			expected: [][]string{{wordLower, wMedlennoComma, wPodnimaemsya, wBystro}},
		},
		{
			name: "short tail that does not fit the lines stays apart",
			input: []token{
				tok(wordLower, 0, 0.5), tok(wMedlenno, 0.5, 1), tok(wPodnimaemsya, 1, 1.5), tok(wBystro, 1.5, 2),
				tok(wPlavno, 2, 2.3), tok(wRovno, 2.3, 2.5),
			},
			limits:   layoutLimits{maxLines: 2, maxLineChars: 20},
			expected: [][]string{{wordLower, wMedlenno, wPodnimaemsya, wBystro}, {wPlavno, wRovno}},
		},
		{
			name: "short head of a phrase is glued to the next subtitle",
			input: []token{
				withBreak(tok("Обязательно,", 0, 0.49), softBreak),
				tok(wordHold, 0.5, 0.8), tok(wordBack, 0.8, 1.1), tok("ровной", 1.1, 1.4), withBreak(tok("всегда!", 1.4, 1.7), strongBreak),
			},
			limits:   limits,
			expected: [][]string{{"Обязательно,", wordHold, wordBack, "ровной", "всегда!"}},
		},
		{
			name: "short tail left by the duration is glued back",
			input: []token{
				tok(wTyanem, 0, 1.2), tok(wNosok, 1.3, 2.5), tok("вверх", 2.6, 3.4), tok(wPlavno, 3.45, 3.7),
			},
			limits:   limits,
			expected: [][]string{{wTyanem, wNosok, "вверх", wPlavno}},
		},
		{
			name:     "early comma does not break",
			input:    seq("Да,", wordHold, wordBack),
			limits:   limits,
			expected: [][]string{{"Да,", wordHold, wordBack}},
		},
		{
			name:   "length breaks, preposition is not left behind",
			input:  seq(wordHold, wordBack, wRovnoy, wI, wNe, wZavalivaem, wordKnees, "внутрь"),
			limits: limits,
			// "держим спину ровной и не заваливаем" is 35 chars: two lines of 20 hold it only as
			// "держим спину ровной" + "и не заваливаем"; adding wordKnees would need a third line.
			expected: [][]string{{wordHold, wordBack, wRovnoy, wI, wNe, wZavalivaem}, {wordKnees, "внутрь"}},
		},
		{
			name:     "one line limit",
			input:    seq(wordHold, wordBack, wRovnoy, "всегда"),
			limits:   layoutLimits{maxLines: 1, maxLineChars: 14},
			expected: [][]string{{wordHold, wordBack}, {wRovnoy, "всегда"}},
		},
		{
			name: "duration breaks",
			input: []token{
				tok(wTyanem, 0, 1), tok(wNosok, 1.1, 2), tok("дышим", 2.1, 3), tok(wRovno, 3.1, 4),
			},
			limits:   limits,
			expected: [][]string{{wTyanem, wNosok, "дышим"}, {wRovno}},
		},
		{
			name:     "word longer than a line takes its own subtitle line",
			input:    seq("сверхдлинноесловобезпробелов", "да"),
			limits:   layoutLimits{maxLines: 1, maxLineChars: 10},
			expected: [][]string{{"сверхдлинноесловобезпробелов"}, {"да"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			groups := groupCues(buildUnits(tt.input, tt.limits.maxLineChars), tt.limits)

			got := make([][]string, 0, len(groups))
			for _, g := range groups {
				var words []string
				for _, u := range g {
					words = append(words, texts(u.tokens)...)
				}

				got = append(got, words)
			}

			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestSplitLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []token
		maxChars int
		expected []string
	}{
		{name: "fits one line", input: seq(wordHold, wordBack), maxChars: 20, expected: []string{"держим спину"}},
		{
			name:     "balanced two lines",
			input:    seq(wSegodnyaCap, wDelaem, wPrised, wSo, wShtangoy),
			maxChars: 20,
			expected: []string{"Сегодня делаем", "присед со штангой"},
		},
		{
			name:     "preposition starts the second line, not ends the first",
			input:    seq(wordHold, wordBack, wRovnoy, wI, wNe, wZavalivaem),
			maxChars: 20,
			expected: []string{"держим спину ровной", "и не заваливаем"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lines := splitLines(buildUnits(tt.input, tt.maxChars), tt.maxChars)
			assert.Equal(t, tt.expected, lineTexts([]cue{{Lines: lines}})[0])
		})
	}
}

func TestBalancedCuts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		widths   []int
		parts    int
		expected []int
	}{
		{name: "one part", widths: []int{5, 5}, parts: 1, expected: nil},
		{name: "two even parts", widths: []int{3, 3, 3, 3}, parts: 2, expected: []int{2}},
		{name: "long word alone", widths: []int{12, 2, 2, 2}, parts: 2, expected: []int{1}},
		{name: "three parts", widths: []int{3, 3, 4, 6, 4, 5}, parts: 3, expected: []int{2, 4}},
		{name: "more parts than words", widths: []int{3}, parts: 2, expected: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, balancedCuts(tt.widths, tt.parts))
		})
	}
}

func TestTrimEnd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "period", input: "колени.", expected: wordKnees},
		{name: "comma", input: "колени,", expected: wordKnees},
		{name: "question stays", input: "колени?", expected: "колени?"},
		{name: "ellipsis stays", input: "колени...", expected: "колени..."},
		{name: "lone punctuation stays", input: ".", expected: "."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			input := cue{Lines: [][]token{{tok("Держим", 0, 1)}, {tok(tt.input, 1, 2)}}}
			got := trimEnd(input)

			assert.Equal(t, tt.expected, got.Lines[1][0].Text)
			assert.Equal(t, tt.input, input.Lines[1][0].Text, "input is not changed")
		})
	}
}

// TestCountingFitsLines: counting and fast counting never make a subtitle taller than the lines allow.
func TestCountingFitsLines(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()

	fast := make([]token, 0, 12)
	for i := range 12 {
		at := float64(i) * 0.075
		fast = append(fast, tok(strconv.Itoa(i+1), at, at+0.07))
	}

	tests := []struct {
		name  string
		input []token
	}{
		{name: "counting words", input: seq(wordOne, wordTwo, wTri, wChetyre, wPyat, "шесть", wSem, wVosem, wordHold)},
		{name: "counting digits", input: seq("1", "2", "3", "4", "5", "6", "7", "8", "9", "10")},
		{name: "fast count of 12 in 0.9 s", input: fast},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			limits := layoutLimits{maxLines: style.Layout.MaxLines, maxLineChars: style.lineChars()}
			cues := buildCues(groupCues(buildUnits(tt.input, limits.maxLineChars), limits), &style, false)

			words := 0

			for _, c := range cues {
				assert.LessOrEqual(t, len(c.Lines), style.Layout.MaxLines)

				for _, line := range c.Lines {
					words += len(line)
					assert.LessOrEqual(t, lineWidth(lineWidths(line)), limits.maxLineChars)

					for _, tk := range line {
						assert.Equal(t, noAccent, tk.Accent, "counting is not accented")
					}
				}
			}

			assert.Len(t, tt.input, words)
		})
	}
}

func TestLayoutLines(t *testing.T) {
	t.Parallel()

	accented := func(words ...string) []token {
		out := seq(words...)
		for i := range out {
			out[i].Accent = numberAccent
		}

		return out
	}

	tests := []struct {
		name     string
		input    []token
		expected []string
	}{
		{
			name:     "short phrase stays on one line",
			input:    slices.Concat(seq(wDelaemCap), accented("3", wPodhoda)),
			expected: []string{"Делаем 3 подхода"},
		},
		{
			name:     "accent of a long phrase on its own line",
			input:    slices.Concat(seq(wDelaemCap, wTri, wPodhoda, "по"), accented("10", wPovtoreniy), seq(wI, wordHold, wordBack)),
			expected: []string{"Делаем три подхода", "по 10 повторений", "и держим спину"},
		},
		{
			name:     "accent at the end",
			input:    slices.Concat(seq(wSegodnyaCap, wDelaem, wPrised, wSo), accented(wShtangoy)),
			expected: []string{"Сегодня делаем присед", "со штангой"},
		},
		{
			name:     "without accents the lines are balanced",
			input:    seq(wSegodnyaCap, wDelaem, wPrised, wSo, wShtangoy),
			expected: []string{"Сегодня делаем", "присед со штангой"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lines := layoutLines(buildUnits(tt.input, 22), 22, 2)
			assert.Equal(t, tt.expected, lineTexts([]cue{{Lines: lines}})[0])
		})
	}
}

func TestSplitUnitLosesNumberAccent(t *testing.T) {
	t.Parallel()

	input := []token{
		withAccent(tok("двадцать", 0, 1), numberAccent), withAccent(tok("пять", 1, 2), numberAccent),
		withAccent(tok("повторений", 2, 3), numberAccent), withAccent(tok("ягодицы", 3, 4), dictionaryAccent),
	}

	units := buildUnits(input, 10)

	got := make([]accentKind, 0, len(units))
	for _, u := range units {
		got = append(got, accentsOf(u.tokens)...)
	}

	assert.Equal(t, []accentKind{noAccent, noAccent, noAccent, dictionaryAccent}, got)
}

func lineWidths(line []token) []int {
	out := make([]int, 0, len(line))
	for _, t := range line {
		out = append(out, utf8.RuneCountInString(t.Text))
	}

	return out
}
