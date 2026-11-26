package subtitles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	styleFile       = "../../assets/subtitles/style.yaml"
	wordsStyleFile  = "../../assets/subtitles/style-words.yaml"
	phraseStyleFile = "../../assets/subtitles/style-phrase.yaml"
)

// The commented style file in assets is the reference for brand book edits: it must stay equal to the defaults.
func TestStyleFileIsDefault(t *testing.T) {
	t.Parallel()

	style, err := LoadStyle(styleFile)

	require.NoError(t, err)
	assert.Equal(t, DefaultStyle(), style)
}

// The reference reels' look of T3.17 is kept as a file: it must stay equal to WordsStyle and be valid.
func TestWordsStyleFile(t *testing.T) {
	t.Parallel()

	style, err := LoadStyle(wordsStyleFile)

	require.NoError(t, err)
	assert.Equal(t, WordsStyle(), style)
	assert.Equal(t, WordMode, style.Layout.Mode)
	assert.True(t, style.Handle.Enabled)
	assert.False(t, style.Topbar.Enabled)
	assert.False(t, style.Section.Enabled)
}

// The phrase layout of before T3.17 is kept as a file: it must stay equal to PhraseStyle and be valid.
func TestPhraseStyleFile(t *testing.T) {
	t.Parallel()

	style, err := LoadStyle(phraseStyleFile)

	require.NoError(t, err)
	assert.Equal(t, PhraseStyle(), style)
	assert.Equal(t, PhraseMode, style.Layout.Mode)
	assert.True(t, style.Vignette.Enabled)
}

// TestParseStyle: a file over the reference reels' style (WordsStyle) changes only what it sets, and an invalid one
// fails with the reason.
func TestParseStyle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		modify      func(s *Style)
		expectedErr string
	}{
		{name: "empty file is the default", input: "", modify: func(*Style) {}},
		{
			name:  "partial override keeps the rest",
			input: "text:\n  font: Montserrat\n  size: 48\nlayout:\n  position: bottom\naccent:\n  words: [присед, становая тяга]\n",
			modify: func(s *Style) {
				s.Text.Font, s.Text.Size = "Montserrat", 48
				s.Layout.Position = BottomPosition
				s.Accent.Words = []string{wPrised, "становая тяга"}
			},
		},
		{name: "unknown field", input: "text:\n  colour: \"#FFFFFF\"\n", expectedErr: "field colour not found"},
		{
			name:  "titles at the top above bottom subtitles",
			input: "layout:\n  mode: phrase\n  position: bottom\ntitle:\n  position: top\n  margin: 300\n",
			modify: func(s *Style) {
				s.Layout.Mode, s.Layout.Position = PhraseMode, BottomPosition
				s.Title.Position, s.Title.Margin = TopPosition, 300
			},
		},
		{
			name:        "titles over the subtitles",
			input:       "layout:\n  mode: phrase\ntitle:\n  position: top\n  margin: 300\n",
			expectedErr: "overlap the subtitles",
		},
		{name: "titles over the hook", input: "title:\n  position: top\n  margin: 300\n", expectedErr: "overlaps the titles"},
		{name: "titles in the Instagram caption", input: "title:\n  margin: 300\n", expectedErr: "between the Instagram zones"},
		{name: "unknown title position", input: "title:\n  position: left\n", expectedErr: "title.position"},
		{name: "bad title box", input: "title:\n  box: black\n", expectedErr: "title.box"},
		{name: "comma in a title font", input: "title:\n  font: \"Fira, Sans\"\n", expectedErr: "title.font"},
		{name: "bad color", input: "text:\n  color: white\n", expectedErr: "text.color must be #RRGGBB"},
		{name: "text in the Instagram top bar", input: "layout:\n  margin_top: 100\n", expectedErr: "layout.margin_top"},
		{name: "text under the buttons", input: "layout:\n  margin_side: 40\n", expectedErr: "layout.margin_side"},
		{name: "text under the caption", input: "layout:\n  margin_bottom: 200\n", expectedErr: "layout.margin_bottom"},
		{name: "unknown position", input: "layout:\n  position: left\n", expectedErr: "layout.position"},
		{
			name:  "the phrase layout by two fields",
			input: "layout:\n  mode: phrase\nvignette:\n  enabled: true\n",
			modify: func(s *Style) {
				s.Layout.Mode, s.Vignette.Enabled = PhraseMode, true
			},
		},
		{
			name:  "the phrase layout at the bottom",
			input: "layout:\n  mode: phrase\n  position: bottom\nvignette:\n  enabled: true\n",
			modify: func(s *Style) {
				s.Layout.Mode, s.Layout.Position, s.Vignette.Enabled = PhraseMode, BottomPosition, true
			},
		},
		{name: "unknown mode", input: "layout:\n  mode: karaoke\n", expectedErr: "layout.mode must be word or phrase"},
		{name: "the word plate over the hook", input: "word_box:\n  y: 400\n", expectedErr: overlapsHook},
		{name: "the handle under the buttons", input: "handle:\n  y: 1200\n", expectedErr: "above the buttons"},
		{name: "comma in a font name", input: "accent:\n  font: \"Fira, Sans\"\n", expectedErr: "accent.font"},
		{name: "font size typo", input: "text:\n  size: 800\n", expectedErr: "text.size"},
		{name: "too many lines", input: "layout:\n  max_lines: 5\n", expectedErr: "layout.max_lines"},
		{name: "control character in a font name", input: "text:\n  font: \"Manrope\\r\"\n", expectedErr: "text.font"},
		{name: "control character in a dictionary word", input: "accent:\n  words: [\"присед\\u0007\"]\n", expectedErr: "accent.words[0]"},
		{name: "control character in a color", input: "text:\n  color: \"#FFFFFF\\n\"\n", expectedErr: "text.color"},
		{name: "translucent text", input: "text:\n  color: \"#FFFFFF40\"\n", expectedErr: "at least half opaque"},
		{name: "translucent accent", input: "accent:\n  color: \"#F2B94B7F\"\n", expectedErr: "accent.color must be at least"},
		{name: "line longer than fits", input: "layout:\n  mode: phrase\n  max_line_chars: 40\n", expectedErr: "at most 28"},
		{
			name:        "subtitles do not fit above the caption",
			input:       "text:\n  size: 120\naccent:\n  size: 150\nlayout:\n  mode: phrase\n  margin_top: 960\n  max_line_chars: 10\n",
			expectedErr: "do not fit",
		},
		{name: "vignette opacity", input: "vignette:\n  opacity: 2\n", expectedErr: "vignette.fade must be"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base := WordsStyle()
			got, err := parseStyleOver(&base, []byte(tt.input))
			if tt.expectedErr != "" {
				require.ErrorIs(t, err, ErrInvalidStyle)
				assert.Contains(t, err.Error(), tt.expectedErr)

				return
			}

			require.NoError(t, err)

			expected := WordsStyle()
			tt.modify(&expected)
			assert.Equal(t, expected, got)
		})
	}
}

func TestAssColor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "white", input: "#FFFFFF", expected: "&H00FFFFFF"},
		{name: "yellow is BGR", input: "#FFD400", expected: "&H0000D4FF"},
		{name: "lower case", input: "#ffd400", expected: "&H0000D4FF"},
		{name: "half transparent black", input: "#00000080", expected: "&H7F000000"},
		{name: "fully transparent", input: "#12345600", expected: "&HFF563412"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, assColor(tt.input))
		})
	}
}

func TestLineChars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		modify   func(s *Style)
		expected int
	}{
		{name: "default limit", modify: func(*Style) {}, expected: 22},
		{name: "estimate: 800 px between margins, 56 px font, 0.5 em", modify: func(s *Style) { s.Layout.MaxLineChars = 0 }, expected: 28},
		{
			name:     "estimate for a larger font",
			modify:   func(s *Style) { s.Layout.MaxLineChars, s.Text.Size = 0, 80 },
			expected: 20,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			style := PhraseStyle()
			tt.modify(&style)

			assert.Equal(t, tt.expected, style.lineChars())
		})
	}
}

// TestValidateFitMiddle: a centered block must stay out of both zones, 2·min(960−250, 1500−960) = 1080 px.
// Font sizes are capped at 200, so the limit is checked on the fit alone.
func TestValidateFitMiddle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		size      int
		expectErr bool
	}{
		{name: "fits: 3 lines of 280 px (1050 px)", size: 280},
		{name: "too tall: 3 lines of 300 px (1125 px)", size: 300, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			style := PhraseStyle()
			style.Layout.Position = MiddlePosition
			style.Layout.MaxLines = 2
			style.Layout.MaxLineChars = 1
			style.Text.Size, style.Accent.Size = tt.size, tt.size

			errs := validateFit(&style)
			if !tt.expectErr {
				assert.Empty(t, errs)

				return
			}

			require.Len(t, errs, 1)
			assert.Contains(t, errs[0].Error(), "1080 px")
		})
	}
}
