package subtitles

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEscapeText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "cyrillic as is", input: "Привет, ёжик!", expected: "Привет, ёжик!"},
		{name: "braces would start an override block", input: "{\\an7}жирный", expected: "\\{an7\\}жирный"},
		{name: "backslash escapes are dropped", input: "a\\Nb \\n \\h", expected: "aNb n h"},
		{name: "backslash before a brace", input: "\\{", expected: "\\{"},
		{name: "control characters become spaces", input: "a\nb\tc", expected: "a b c"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, escapeText(tt.input))
		})
	}
}

func TestFormatTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    float64
		expected string
	}{
		{name: "zero", input: 0, expected: "0:00:00.00"},
		{name: "negative is zero", input: -1, expected: "0:00:00.00"},
		{name: "rounded to centiseconds", input: 1.236, expected: "0:00:01.24"},
		{name: "minutes", input: 125.5, expected: "0:02:05.50"},
		{name: "hours", input: 3725.01, expected: "1:02:05.01"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, formatTime(tt.input))
		})
	}
}

func TestCueText(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	upper := DefaultStyle()
	upper.Text.Uppercase = true
	upper.Accent.Uppercase = false

	sample := cue{Lines: [][]token{
		{tok(wDelaemCap, 0, 1), withAccent(tok("3", 1, 2), numberAccent), withAccent(tok(wPodhoda, 2, 3), numberAccent)},
		{tok("по", 3, 4), tok("{10}", 4, 5)},
	}}

	tests := []struct {
		name     string
		style    *Style
		current  int
		expected string
	}{
		{
			name: "accent run, reset on the next line", style: &style, current: -1,
			expected: `Делаем {\rAccent}3 ПОДХОДА\N{\r}по \{10\}`,
		},
		{
			name: "karaoke word in the main style", style: &style, current: 0,
			expected: `{\1c&H5A8CFF&\1a&H00&}Делаем {\rAccent}3 ПОДХОДА\N{\r}по \{10\}`,
		},
		{
			name: "karaoke word inside an accent run", style: &style, current: 2,
			expected: `Делаем {\rAccent}3 {\rAccent\1c&H5A8CFF&\1a&H00&}ПОДХОДА\N{\r}по \{10\}`,
		},
		{
			name: "karaoke word on the second line", style: &style, current: 4,
			expected: `Делаем {\rAccent}3 ПОДХОДА\N{\r}по {\1c&H5A8CFF&\1a&H00&}\{10\}`,
		},
		{
			name: "uppercase main text, accent as spoken", style: &upper, current: -1,
			expected: `ДЕЛАЕМ {\rAccent}3 подхода\N{\r}ПО \{10\}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, cueText(sample, tt.style, tt.current))
		})
	}
}

func TestKaraokeEvents(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	style.Accent.Uppercase = false

	c := cue{Start: 0.95, End: 2.6, Lines: [][]token{{tok(wordOne, 1, 1.3), tok(wordTwo, 1.5, 1.8), tok(wTri, 1.8, 2.5)}}}

	got := karaokeEvents(c, &style)

	expected := []event{
		{start: 0.95, end: 1.5, text: `{\1c&H5A8CFF&\1a&H00&}раз {\r}два три`},
		{start: 1.5, end: 1.8, text: `раз {\1c&H5A8CFF&\1a&H00&}два {\r}три`},
		{start: 1.8, end: 2.6, text: `раз два {\1c&H5A8CFF&\1a&H00&}три`},
	}

	require.Len(t, got, len(expected))

	for i := range expected {
		assert.InDelta(t, expected[i].start, got[i].start, 1e-9)
		assert.InDelta(t, expected[i].end, got[i].end, 1e-9)
		assert.Equal(t, expected[i].text, got[i].text)
	}
}

func TestRenderASS(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	bottom := DefaultStyle()
	bottom.Layout.Position = BottomPosition
	middle := DefaultStyle()
	middle.Layout.Position = MiddlePosition

	cues := []cue{{Start: 0.95, End: 2.1, Lines: [][]token{{tok("Привет", 1, 2)}}}}

	tests := []struct {
		name     string
		style    *Style
		expected []string
	}{
		{
			name:  "header and top styles",
			style: &style,
			expected: []string{
				"[Script Info]\n", "ScriptType: v4.00+\n", "PlayResX: 1080\nPlayResY: 1920\n", "WrapStyle: 0\n",
				"ScaledBorderAndShadow: yes\n",
				"Style: Main,Manrope Medium,56,&H00E1F0F5,&H00E1F0F5,&H00000000,&H99000000,0,0,0,0,100,100,0,0,1,0,2,8,140,140,260,1\n",
				"Style: Accent,Fira Sans Extra Condensed ExtraBold,64,&H004BB9F2,&H004BB9F2,&H00000000,&H99000000,0,0,0,0,100,100,0,0,1,0,2,8,140,140,260,1\n",
				"[Events]\n" + eventFormat + "\n",
				"Dialogue: 0,0:00:00.95,0:00:02.10,Main,,0,0,0,,Привет\n",
			},
		},
		{
			name:     "bottom",
			style:    &bottom,
			expected: []string{",2,140,140,440,1\n"},
		},
		{
			name:     "middle",
			style:    &middle,
			expected: []string{",5,140,140,0,1\n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := renderASS(cues, tt.style, false)
			for _, part := range tt.expected {
				assert.Contains(t, got, part)
			}
		})
	}
}

func TestBuildASS(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	result := speech(wPrivetCapExcl, "Делаем 3 подхода по 10 повторений, держим спину.")

	tests := []struct {
		name     string
		accents  []string
		karaoke  bool
		expected []string
		cues     int
	}{
		{
			name: "automatic accents",
			expected: []string{
				",,Привет!\n",
				// a long phrase: the accent on its own line
				`,,Делаем\N{\rAccent}3 ПОДХОДА\N{\r}по 10 повторений` + "\n",
				// no longest-word accent right after an accented subtitle
				",,держим спину\n",
			},
			cues: 3,
		},
		{
			name:     "explicit accents",
			accents:  []string{"10 повторений"},
			expected: []string{`,,Делаем 3 подхода\Nпо {\rAccent}10 ПОВТОРЕНИЙ` + "\n", ",,держим спину\n"},
			cues:     3,
		},
		{
			name:     "karaoke has an event per word",
			karaoke:  true,
			expected: []string{`,,{\1c&H5A8CFF&\1a&H00&}Привет!` + "\n"},
			cues:     3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, cues := buildASS(result, &style, tt.accents, tt.karaoke)

			assert.Equal(t, tt.cues, cues)

			for _, part := range tt.expected {
				assert.Contains(t, got, part)
			}

			if tt.karaoke {
				words, events := len(result.Words), strings.Count(got, "Dialogue:")
				assert.Equal(t, words, events, "one event per spoken word")
			}
		})
	}
}
