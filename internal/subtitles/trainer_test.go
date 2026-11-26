package subtitles

import (
	"slices"
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
		{name: "a heading longer than allowed", input: "section:\n  min_duration: 7\n", expectedErr: "section.min_duration"},
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
		{name: "въебал", input: "въебал", expected: "въе*ал"},
		{name: "взъебка", input: "взъебка", expected: "взъ*бка"},
		{name: "выблядок", input: "выблядок", expected: "выб*ядок"},
		{name: "ёб alone", input: "ёб", expected: "*б"},
		{name: "долбоёб", input: "долбоёб", expected: "долбо*б"},
		{name: "долбоёбы", input: "долбоёбы", expected: "долбо*бы"},
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
		"страхуйте", "небо", "10", "Ебола", "ебонит", "ребёнок", "небось",
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
		Transcript: speech("Первое: мои подопечные приходят к целям.", "В Москве и Тбилиси, блять, 10 человек.",
			"МФЦ рядом.", "PDF пришлю."),
	}, &style)

	assert.Equal(t, 4, cues)
	assert.Contains(t, ass, ",,МФЦ рядом", "an abbreviation keeps its capitals")
	assert.Contains(t, ass, ",,PDF пришлю")
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
		{name: "no headings", expected: 2.5},
		{name: "a heading after min_duration", notes: []Note{{Kind: SectionNote, Text: fear1, Start: 2.1}}, expected: 2.1},
		{name: "a heading too early", notes: []Note{{Kind: SectionNote, Text: fear1, Start: 1}}, expected: 1.8},
		{name: "a heading late", notes: []Note{{Kind: SectionNote, Text: fear1, Start: 9}}, expected: 2.5},
		{
			name:     "the first heading counts, not a callout",
			notes:    []Note{{Kind: CalloutNote, Text: "6 лет", Start: 1}, {Kind: SectionNote, Text: "2", Start: 2.2}},
			expected: 2.2,
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
			name:     "until the phrase after the one that reads the heading out",
			notes:    []Note{{Kind: SectionNote, Text: fear1, Start: 5}, {Kind: SectionNote, Text: fear2, Start: 12}},
			reelEnd:  20,
			expected: []noteBlock{block(5, 9.6, fear1Caps), block(12, 14.5, fear2Caps)},
		},
		{
			name:     "after the hook",
			notes:    []Note{{Kind: SectionNote, Text: fear1, Start: 5}},
			hookEnd:  6,
			expected: []noteBlock{block(6, 9.6, fear1Caps)},
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
			expected: []noteBlock{block(5.2, 9.6, fear2Caps)},
		},
		{name: "an empty heading", notes: []Note{{Kind: SectionNote, Text: " \u200b ", Start: 5}}, expected: []noteBlock{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, sectionBlocks(tt.notes, tt.hookEnd, tt.reelEnd, result, &style))
		})
	}

	long := &transcription.Result{Segments: []transcription.Segment{{Start: 3, End: 12, Text: "Долгий вопрос."}}}
	assert.Equal(t, []noteBlock{block(3, 9, fear1Caps)},
		sectionBlocks([]Note{{Kind: SectionNote, Text: fear1, Start: 3}}, 0, 0, long, &style), "at most max_duration")
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
	assert.Contains(t, ass, "Dialogue: 2,0:00:01.23,0:00:02.50,Hook,", "the hook goes at the heading")

	words := WordsStyle()
	ass, _ = buildDocument(&CreateSubtitlesData{Transcript: result, Hook: trainerHook, Notes: notes}, &words)

	assert.NotContains(t, ass, ",Section,,")
	assert.Contains(t, ass, ",Hook,,0,0,0,,{\\fad(150,150)}страх 1\n", "a callout in the words' style")
}

// TestSelectEvents: the select animation is the text, the frame and the cursor at its corner, standing small at the
// right, 30 px lower, for 0.95 s, then moving and growing together by straight pieces that slow down, its right edge
// held (the frame never leaves the screen), then the frame goes 0.18 s after the growth and the cursor at 1.67 s; the text stays. The frame is measured
// by the advances of the font: 16 px around the text.
func TestSelectEvents(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	events := strings.Split(strings.TrimSuffix(hookEvent(trainerHook, 4, &style), "\n"), "\n")
	const lines = `РАЗВЕИВАЮ СТРАХИ ОБ\NОНЛАЙН-ТРЕНИРОВКАХ`

	require.Len(t, events, 3*(1+animSteps+1), "the text, the frame and the cursor: still, growing, still")
	assert.Equal(t, `Dialogue: 2,0:00:00.00,0:00:00.95,Hook,,0,0,0,,{\an5\pos(857,562)\fscx35\fscy35\fad(150,0)}`+lines,
		events[0], "two lines as in the trainer's reel at full size, «об» at the end of the first")
	assert.Equal(t, `Dialogue: 2,0:00:00.95,0:00:00.99,Hook,,0,0,0,,{\an5\move(857,562,737.2,550.7,0,40)\fscx35\fscy35`+
		`\t(0,40,\fscx59.6\fscy59.6)}`+lines, events[1], "fast at the start of the growth")
	assert.Equal(t, `Dialogue: 2,0:00:01.23,0:00:04.00,Hook,,0,0,0,,{\an5\pos(540,532)\fscx100\fscy100\fad(0,150)}`+lines,
		events[animSteps+1])

	frame := events[animSteps+2]
	assert.True(t, strings.HasPrefix(frame, `Dialogue: 2,0:00:00.00,0:00:00.95,Hook,,0,0,0,,{\an5\pos(857,562)\fscx35\fscy35`+
		`\bord0\shad0\1c&HFFFFFF&\1a&H00&\fad(150,0)\p1}m 6 6 l 981.4 6 `), frame)
	assert.Equal(t, 6, strings.Count(frame, "m "), "the ring (two contours) and four handles")
	assert.True(t, strings.HasPrefix(events[2*animSteps+3], `Dialogue: 2,0:00:01.23,0:00:01.41,Hook,`))
	assert.Contains(t, events[2*animSteps+3], `\fad(0,120)\p1}`)

	assert.True(t, strings.HasPrefix(events[2*animSteps+4], `Dialogue: 2,0:00:00.00,0:00:00.95,Hook,,0,0,0,,{\an7`+
		`\pos(1030.7,613.3)\bord2\shad0\1c&HFFFFFF&\3c&H000000&`))
	last := events[len(events)-1]
	assert.True(t, strings.HasPrefix(last, `Dialogue: 2,0:00:01.23,0:00:01.67,Hook,,0,0,0,,{\an7\pos(1030.7,673)`), last)
	assert.True(t, strings.HasSuffix(last, `\fad(0,120)\p1}`+cursorPath+`{\p0}`))

	for _, e := range events[2*animSteps+4:] {
		assert.NotRegexp(t, `\\(move|pos)\((10[4-9]\d|1[1-9]\d\d)`, e, "the cursor stays on the screen")
	}

	short := strings.Split(strings.TrimSuffix(hookEvent(trainerHook, 0.6, &style), "\n"), "\n")
	require.Len(t, short, 3, "a short hook: the text, the frame and the cursor standing small")
	assert.Contains(t, short[0], `\fscx35\fscy35\fad(150,150)}`)

	plain := style
	plain.Hook.Animation = NoHookAnimation
	assert.NotContains(t, hookEvent(trainerHook, 4, &plain), `\move`)
}

// TestHideUnderHook: while the hook is shown the trainer's subtitles are not: one under it goes, one that runs past it
// starts at its end (if enough of it is left).
func TestHideUnderHook(t *testing.T) {
	t.Parallel()

	cues := []cue{{Start: 0.2, End: 1.5}, {Start: 2.0, End: 3.2}, {Start: 2.0, End: 2.6}, {Start: 3.5, End: 5}}

	assert.Equal(t, []cue{{Start: 2.5, End: 3.2}, {Start: 3.5, End: 5}}, hideUnderHook(cues, 2.5))
	assert.Equal(t, cues, hideUnderHook(cues, 0), "no hook")
}

// TestHideReadings: only the words that read a drawn heading out go («страх номер один» for «СТРАХ 1», «во-первых»
// for «ПЕРВОЕ»), the rest of the phrase stays; a phrase at the heading that does not read it stays whole.
func TestHideReadings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		text     string
		heading  string
		expected string
	}{
		{
			name: "the reading and the question in one phrase", text: "Страх номер один: а что если не получится?",
			heading: fear1Caps, expected: "а что если не получится?",
		},
		{name: "an ordinal", text: "Страх первый. Дорого.", heading: fear1Caps, expected: "Дорого."},
		{name: "an adverb", text: "Во-первых, пейте воду.", heading: "ПЕРВОЕ", expected: "пейте воду."},
		{
			name: "a phrase that is not the reading", text: "Итак, начнём с простого.", heading: fear1Caps,
			expected: "Итак, начнём с простого.",
		},
		{
			name: "half of the heading is not enough with other words", text: "Страх мешает начать.",
			heading: "СТРАХ НОМЕР 1 ОНЛАЙН", expected: "Страх мешает начать.",
		},
		{
			name: "far from the heading", text: "Привет всем. Страх номер один.", heading: fear1Caps,
			expected: "Привет всем. Страх номер один.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tokens := tokensFromTranscript(speech(tt.text))
			heading := noteBlock{start: tokens[0].Start, end: 9, entries: []string{tt.heading}, lines: 1}

			kept := hideReadings(tokens, []noteBlock{heading})

			texts := make([]string, 0, len(kept))
			for _, k := range kept {
				texts = append(texts, k.Text)
			}

			assert.Equal(t, tt.expected, strings.Join(texts, " "))
			assert.Equal(t, tokens, hideReadings(tokens, nil), "no heading drawn")
		})
	}
}

// TestHideReadingsInDocument: in one phrase without a pause the words reading the heading go and the question is
// shown; a heading not drawn (a callout replaced it) hides nothing.
func TestHideReadingsInDocument(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	result := speech(hookPhrase, "Страх номер один: а что если онлайн-тренировка менее эффективна?")
	start := wordStart(t, result, "Страх")
	notes := []Note{{Kind: SectionNote, Text: fear1, Start: start}}

	ass, _ := buildDocument(&CreateSubtitlesData{Transcript: result, Hook: trainerHook, Notes: notes}, &style)

	assert.NotContains(t, strings.ToLower(ass), "номер один", "the reading is hidden")
	assert.Contains(t, ass, ",Main,,0,0,0,,а что если онлайн-тренировка", "the question is shown")
	assert.Contains(t, ass, ",Section,", "the heading is drawn")

	dropped := append(slices.Clone(notes), Note{Kind: CalloutNote, Text: calloutYears, Start: start + 0.2})

	ass, _ = buildDocument(&CreateSubtitlesData{Transcript: result, Hook: trainerHook, Notes: dropped}, &style)

	assert.NotContains(t, ass, ",Section,", "the callout replaced the heading")
	assert.Contains(t, ass, "страх номер один", "a heading not drawn hides nothing")
}

// wordStart is the start of the first word of the transcript written so.
func wordStart(t *testing.T, result *transcription.Result, word string) float64 {
	t.Helper()

	for _, w := range result.Words {
		if w.Word == word {
			return w.Start
		}
	}

	require.Failf(t, "no word", "%q", word)

	return 0
}

// TestTrainerDocumentNoDoubles: in the trainer's document nothing of the speech stands under the hook or reads the
// heading out: the first subtitle is the question, from the heading's start on.
func TestTrainerDocumentNoDoubles(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	result := speech(hookPhrase, fear1Phrase, "А что если онлайн-тренировка менее эффективна?")
	notes := []Note{{Kind: SectionNote, Text: fear1, Start: 1.9}}

	ass, cues := buildDocument(&CreateSubtitlesData{Transcript: result, Hook: trainerHook, Notes: notes}, &style)

	assert.Equal(t, 1, cues)
	assert.NotContains(t, ass, "развеиваю страхи об", "the hook's phrase is not doubled")
	assert.NotContains(t, ass, "страх номер один", "the heading is not read out in the subtitles")
	assert.Contains(t, ass, ",Main,,0,0,0,,а что если онлайн-тренировка")
	assert.Contains(t, ass, "Dialogue: 2,0:00:01.90,0:00:05.65,Section,", "the heading stays to the end of the question")
}

// TestSeparateBlocks: a note that starts while a heading is shown replaces it; a note shown when a heading starts ends
// there; what is left too short is not drawn.
func TestSeparateBlocks(t *testing.T) {
	t.Parallel()

	heading := func(start, end float64) noteBlock {
		return noteBlock{start: start, end: end, entries: []string{fear1Caps}, lines: 1, fadeIn: true, fadeOut: true}
	}
	note := func(start, end float64) noteBlock {
		return noteBlock{start: start, end: end, entries: []string{"6 ЛЕТ"}, lines: 1, fadeIn: true, fadeOut: true}
	}

	tests := []struct {
		name             string
		notes, sections  []noteBlock
		expectedNotes    []noteBlock
		expectedSections []noteBlock
	}{
		{
			name: "a callout replaces the heading", notes: []noteBlock{note(7, 9)}, sections: []noteBlock{heading(5, 10)},
			expectedNotes: []noteBlock{note(7, 9)}, expectedSections: []noteBlock{heading(5, 7)},
		},
		{
			name: "a callout ends at the heading", notes: []noteBlock{note(4, 6.5)}, sections: []noteBlock{heading(5, 9)},
			expectedNotes: []noteBlock{note(4, 5)}, expectedSections: []noteBlock{heading(5, 9)},
		},
		{
			name: "a heading left too short", notes: []noteBlock{note(5.2, 7)}, sections: []noteBlock{heading(5, 9)},
			expectedNotes: []noteBlock{note(5.2, 7)}, expectedSections: []noteBlock{},
		},
		{
			name: "apart in time", notes: []noteBlock{note(1, 3)}, sections: []noteBlock{heading(5, 9)},
			expectedNotes: []noteBlock{note(1, 3)}, expectedSections: []noteBlock{heading(5, 9)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			notes, sections := separateBlocks(tt.notes, tt.sections)
			assert.Equal(t, tt.expectedNotes, notes)
			assert.Equal(t, tt.expectedSections, sections)
		})
	}
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
