package subtitles

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	// trainerHook is the hook of the trainer's own reel.
	trainerHook = "развеиваю страхи об онлайн-тренировках"
	tgLeft      = "tg: @ana_prokudina"
	speedRight  = "2x"
	fear1       = "страх 1"
	fear2       = "страх 2"
	fear1Caps   = "СТРАХ 1"
	fear2Caps   = "СТРАХ 2"
	hookPhrase  = "Развеиваю страхи об онлайн-тренировках."
	fear1Phrase = "Страх номер один."
)

// TestDefaultStyleIsTrainers: the built-in style is the trainer's own look (T3.20) and valid.
func TestDefaultStyleIsTrainers(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()

	require.NoError(t, style.Validate())
	assert.Equal(t, PhraseMode, style.Layout.Mode)
	assert.True(t, style.Layout.LowercaseSentences)
	assert.Equal(t, TextStyle{Font: regularFont, Size: 60, Color: white}, style.Text)
	assert.Zero(t, style.Accent.MaxPerCue, "no accents")
	assert.False(t, style.Accent.LongestWord)
	assert.False(t, style.Vignette.Enabled)
	assert.Equal(t, SelectHookAnimation, style.Hook.Animation)
	assert.Equal(t, SectionHookUntil, style.Hook.Until)
	assert.Equal(t, peach, style.Hook.Color)
	assert.True(t, style.Hook.Uppercase)
	assert.True(t, style.Section.Enabled)
	assert.Equal(t, peach, style.Section.Color)
	assert.True(t, style.Topbar.Enabled)
	assert.False(t, style.Handle.Enabled, "no Instagram handle and no end card")
}

// TestValidateTrainerStyle: the places of the trainer's style are checked: the top line, the hook, the headings and
// the subtitles stay clear of each other and of the Instagram zones.
func TestValidateTrainerStyle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		expectedErr string
	}{
		{name: "the default", input: ""},
		{name: "the top line over the hook", input: "topbar:\n  y: 440\n", expectedErr: "overlaps the hook and the screen notes"},
		{name: "the top line in the Instagram bar", input: "topbar:\n  y: 260\n", expectedErr: "below the Instagram top bar"},
		{name: "a heading over the subtitles", input: "section:\n  margin: 420\n", expectedErr: "overlap the subtitles"},
		{name: "unknown end of the hook", input: "hook:\n  until: never\n", expectedErr: "hook.until"},
		{name: "unknown animation", input: "hook:\n  animation: zoom\n", expectedErr: "hook.animation"},
		{name: "a hook that does not fit", input: "hook:\n  margin_side: 0\n", expectedErr: "does not fit 3 lines"},
		{name: "a typo in the right size", input: "topbar:\n  right_size: 300\n", expectedErr: "topbar.right_size"},
		{name: "a heading longer than allowed", input: "section:\n  min_duration: 5\n", expectedErr: "section.min_duration"},
		{name: "a translucent top line", input: "topbar:\n  color: \"#FFFFFF40\"\n", expectedErr: "topbar.color"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			base := DefaultStyle()
			_, err := parseStyleOver(&base, []byte(tt.input))
			if tt.expectedErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrInvalidStyle)
			assert.Contains(t, err.Error(), tt.expectedErr)
		})
	}
}

// TestCensor: obscene words get one letter masked on the screen; words that only contain the letters stay.
func TestCensor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "хуйня", input: "хуйня", expected: "х*йня"},
		{name: "блять with a comma", input: "Блять,", expected: "Б*ять,"},
		{name: "пиздец", input: "пиздец!", expected: "п*здец!"},
		{name: "a prefix", input: "нахуй", expected: "нах*й"},
		{name: "a hyphen", input: "ни-хуя", expected: "ни-х*я"},
		{name: "заебал", input: "заебал", expected: "зае*ал"},
		{name: "ё", input: "Ёбаный", expected: "Ё*аный"},
		{name: "the hard sign", input: "съебал", expected: "съе*ал"},
		{name: "сука alone", input: "Сука!", expected: "С*ка!"},
		{name: "распиздяй", input: "распиздяй", expected: "расп*здяй"},
		{name: "мудак", input: "мудак", expected: "м*дак"},
		{name: "a phrase", input: "это просто пиздец какой-то", expected: "это просто п*здец какой-то"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, censor(tt.input))
		})
	}

	// Words with the letters of a root inside stay as they are.
	for _, word := range []string{
		"употреблять", "оскорблять", "ребята", "сукно", "хлеба", "себе", "плохую", "тихую", "бляшки", "мудрый",
		"страхуйте", "небо", "10",
	} {
		t.Run(word, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, word, censor(word))
		})
	}
}

// TestTrainerSubtitles: the trainer's subtitles are phrases without accents, the first word of a sentence small, names
// keep their capital, the obscene words masked; no plate, no vignette.
func TestTrainerSubtitles(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	ass, cues := buildDocument(&CreateSubtitlesData{
		Transcript: speech("Первое: мои подопечные приходят к целям.", "В Москве и Тбилиси, блять, 10 человек."),
	}, &style)

	assert.Equal(t, 2, cues)
	assert.Contains(t, ass, "первое: мои подопечные")
	assert.NotContains(t, ass, "Первое")
	assert.Contains(t, ass, "в Москве и Тбилиси")
	assert.Contains(t, ass, "б*ять")
	assert.NotContains(t, ass, "блять")
	assert.NotContains(t, ass, `\rAccent`, "no accents")
	assert.NotContains(t, ass, "Style: Shade,")
	assert.Contains(t, ass, "Style: Main,Manrope,60,&H00FFFFFF,&H00FFFFFF,&H00000000,&H99000000,0,0,0,0,100,100,0,0,1,0,2,"+
		"8,140,140,455,1\n")
}

// TestHookUntilSection: the trainer's hook stays until the first section heading, within min_duration..max_duration,
// max_duration without headings.
func TestHookUntilSection(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	result := speech(hookPhrase, fear1Phrase)
	result.Duration = 30

	tests := []struct {
		name     string
		notes    []Note
		expected float64
	}{
		{name: "no headings", expected: 4},
		{name: "a heading after min_duration", notes: []Note{{Kind: SectionNote, Text: fear1, Start: 3}}, expected: 3},
		{name: "a heading too early", notes: []Note{{Kind: SectionNote, Text: fear1, Start: 1}}, expected: 2.5},
		{name: "a heading late", notes: []Note{{Kind: SectionNote, Text: fear1, Start: 9}}, expected: 4},
		{
			name:     "the first heading counts, not a callout",
			notes:    []Note{{Kind: CalloutNote, Text: "6 лет", Start: 1}, {Kind: SectionNote, Text: "2", Start: 3.5}},
			expected: 3.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.InDelta(t, tt.expected, hookEnd(trainerHook, result, tt.notes, &style), 1e-9)
		})
	}
}

// TestSectionBlocks: a heading waits for the hook, stays while the first phrase of its section is said (2.5–4 s, its
// own end when set) and the next one replaces it; none past the reel.
func TestSectionBlocks(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	result := &transcription.Result{Segments: []transcription.Segment{
		{Start: 5, End: 6, Text: fear1Phrase},
		{Start: 6.2, End: 9.5, Text: "А что если онлайн-тренировка менее эффективна?"},
		{Start: 12, End: 12.8, Text: "Страх номер два."},
		{Start: 13, End: 14, Text: "А что если я не справлюсь?"},
	}}
	block := func(start, end float64, text string) noteBlock {
		return noteBlock{start: start, end: end, entries: []string{text}, lines: 1, fadeIn: true, fadeOut: true}
	}

	tests := []struct {
		name     string
		notes    []Note
		hookEnd  float64
		reelEnd  float64
		expected []noteBlock
	}{
		{
			name:     "the first phrase of the section, at most max_duration",
			notes:    []Note{{Kind: SectionNote, Text: fear1, Start: 5}, {Kind: SectionNote, Text: fear2, Start: 12}},
			reelEnd:  20,
			expected: []noteBlock{block(5, 9, fear1Caps), block(12, 14.5, fear2Caps)},
		},
		{
			name:     "after the hook",
			notes:    []Note{{Kind: SectionNote, Text: fear1, Start: 5}},
			hookEnd:  6,
			expected: []noteBlock{block(6, 10, fear1Caps)},
		},
		{
			name: "the next one replaces it, the reel ends the last",
			notes: []Note{
				{Kind: SectionNote, Text: fear2, Start: 12}, {Kind: SectionNote, Text: "страх 3", Start: 13.5},
			},
			reelEnd:  15,
			expected: []noteBlock{block(12, 13.5, fear2Caps), block(13.5, 15, "СТРАХ 3")},
		},
		{
			name:     "its own end within min_duration",
			notes:    []Note{{Kind: SectionNote, Text: fear1, Start: 5, End: 6}},
			expected: []noteBlock{block(5, 7.5, fear1Caps)},
		},
		{
			name:     "too short to be drawn",
			notes:    []Note{{Kind: SectionNote, Text: fear1, Start: 5}, {Kind: SectionNote, Text: fear2, Start: 5.2}},
			expected: []noteBlock{block(5.2, 9.2, fear2Caps)},
		},
		{name: "an empty heading", notes: []Note{{Kind: SectionNote, Text: " \u200b ", Start: 5}}, expected: []noteBlock{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, sectionBlocks(tt.notes, tt.hookEnd, tt.reelEnd, result, &style))
		})
	}
}

// TestTrainerSectionsInDocument: the headings are drawn in the Section style one at a time, the other notes stay in the
// hook's style; in a style without headings they are callouts of the notes.
func TestTrainerSectionsInDocument(t *testing.T) {
	t.Parallel()

	result := speech(hookPhrase, fear1Phrase, "А что если онлайн менее эффективно?")
	notes := []Note{{Kind: SectionNote, Text: fear1, Start: 2.5}}

	trainer := DefaultStyle()
	ass, _ := buildDocument(&CreateSubtitlesData{Transcript: result, Hook: trainerHook, Notes: notes}, &trainer)

	assert.Contains(t, ass, "Style: Section,Fira Sans Extra Condensed ExtraBold,78,&H0080A0E8,")
	assert.Contains(t, ass, ",Section,,0,0,0,,{\\fad(150,150)}СТРАХ 1\n")
	assert.Contains(t, ass, "Dialogue: 2,0:00:00.00,0:00:02.50,Hook,", "the hook goes at the heading")

	words := WordsStyle()
	ass, _ = buildDocument(&CreateSubtitlesData{Transcript: result, Hook: trainerHook, Notes: notes}, &words)

	assert.NotContains(t, ass, ",Section,,")
	assert.Contains(t, ass, ",Hook,,0,0,0,,{\\fad(150,150)}страх 1\n", "a callout in the words' style")
}

// TestSelectEvents: the select animation is the text, the frame and the cursor moving and growing together from the
// right; the frame and the cursor go after the growth, the text stays; a long hook is made smaller as a whole.
func TestSelectEvents(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	got := hookEvent(trainerHook, 4, &style)
	events := strings.Split(strings.TrimSuffix(got, "\n"), "\n")

	require.Len(t, events, 3)
	assert.Equal(t, `Dialogue: 2,0:00:00.00,0:00:04.00,Hook,,0,0,0,,{\an5\move(802.1,529,540,529,250,750)`+
		`\fscx37.6\fscy37.6\t(250,750,\fscx94\fscy94)\fad(150,150)}РАЗВЕИВАЮ СТРАХИ ОБ\NОНЛАЙН-ТРЕНИРОВКАХ`, events[0],
		"two lines as in the trainer's reel, «об» at the end of the first rather than the hook made smaller")
	assert.True(t, strings.HasPrefix(events[1], `Dialogue: 2,0:00:00.00,0:00:01.05,Hook,,0,0,0,,{\an5\move(802.1,529,`+
		`540,529,250,750)\fscx40\fscy40\t(250,750,\fscx100\fscy100)\bord0\shad0\1c&HFFFFFF&\1a&H00&\fad(150,120)\p1}m `),
		events[1])
	assert.Equal(t, 6, strings.Count(events[1], "m "), "the ring (two contours) and four handles")
	assert.True(t, strings.HasPrefix(events[2], `Dialogue: 2,0:00:00.00,0:00:01.05,Hook,,0,0,0,,{\an7\move(993,581.3,`+
		`1012.8,655.1,250,750)\bord2\shad0\1c&HFFFFFF&\3c&H000000&`), events[2])
	assert.True(t, strings.HasSuffix(events[2], `\p1}`+cursorPath+`{\p0}`))

	short := hookEvent(trainerHook, 0.6, &style)
	assert.Contains(t, short, `\t(250,600,\fscx94\fscy94)\fad(150,150)}`, "a short hook grows within its time")
	assert.Contains(t, short, "Dialogue: 2,0:00:00.00,0:00:00.60,Hook,,0,0,0,,{\\an5", "the frame goes with it")

	plain := style
	plain.Hook.Animation = NoHookAnimation
	assert.NotContains(t, hookEvent(trainerHook, 4, &plain), `\move`)
}

// TestTopbarEvents: the top line draws the texts that pass the check, the right one in its size, a long one squeezed,
// fading out with the card; nothing in a style without it.
func TestTopbarEvents(t *testing.T) {
	t.Parallel()

	trainer, words := DefaultStyle(), WordsStyle()
	left := "Dialogue: 3,0:00:00.00,0:00:10.00,Topbar,,0,0,0,,{\\an4\\pos(60,286)\\q2}tg: @ana_prokudina\n"
	right := "Dialogue: 3,0:00:00.00,0:00:10.00,Topbar,,0,0,0,,{\\an6\\pos(1020,286)\\q2\\fs58}2x\n"

	tests := []struct {
		name     string
		branding Branding
		fadeOut  int
		style    *Style
		expected string
	}{
		{name: "both sides", branding: Branding{TopbarLeft: tgLeft, TopbarRight: speedRight}, style: &trainer, expected: left + right},
		{name: "the left only", branding: Branding{TopbarLeft: tgLeft}, style: &trainer, expected: left},
		{
			name: "markup is not drawn", branding: Branding{TopbarLeft: "tg: {\\an7}", TopbarRight: speedRight},
			style: &trainer, expected: right,
		},
		{
			name: "a long text is squeezed", branding: Branding{TopbarLeft: strings.Repeat("а", 40)}, style: &trainer,
			expected: "Dialogue: 3,0:00:00.00,0:00:10.00,Topbar,,0,0,0,,{\\an4\\pos(60,286)\\q2\\fscx57}" +
				strings.Repeat("а", 40) + "\n",
		},
		{
			name: "fades out with the card", branding: Branding{TopbarRight: speedRight}, fadeOut: 500, style: &trainer,
			expected: "Dialogue: 3,0:00:00.00,0:00:10.00,Topbar,,0,0,0,,{\\an6\\pos(1020,286)\\q2\\fs58\\fad(0,500)}2x\n",
		},
		{name: "a style without it", branding: Branding{TopbarLeft: tgLeft}, style: &words, expected: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, topbarEvents(&tt.branding, 10, tt.fadeOut, tt.style))
		})
	}
}

// TestTrainerBranding: the trainer's style draws the top line but not the Instagram handle and the end card, even when
// they are set; the words' style draws the handle and the card, not the top line.
func TestTrainerBranding(t *testing.T) {
	t.Parallel()

	branding := &Branding{Handle: testHandle, Icon: true, EndCard: true, TopbarLeft: tgLeft, TopbarRight: speedRight}
	trainer, words := DefaultStyle(), WordsStyle()

	got := brandEvents(branding, 10, &trainer)
	assert.Contains(t, got, ",Topbar,")
	assert.NotContains(t, got, ",Handle,")
	assert.NotContains(t, got, ",Card,")

	start, duration := cardTimes(branding, 10, &trainer)
	assert.Zero(t, start+duration, "no card: the speech is not cut by it")

	got = brandEvents(branding, 10, &words)
	assert.NotContains(t, got, ",Topbar,")
	assert.Contains(t, got, ",Handle,")
	assert.Contains(t, got, ",Card,")
}
