package subtitles

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// fiveMythsHook is a hook one word longer than the trainer's: three lines at full size before T3.23; twoClauseHook is a
// hook of two clauses, as long as the one the editor's model wrote for a short reel: three lines over the face before
// T3.27.
const (
	fiveMythsHook = "развеиваю пять мифов об онлайн-тренировках"
	twoClauseHook = "ролик для тех, кто считает, что онлайн-тренировки не для них"
	// longHook is about 75 characters: two lines at about half the size.
	longHook = "ролик для всех тех, кто давно считает, что онлайн-тренировки дома не для них"
)

// talkSegment is a Whisper segment of the synthetic talking head: its text with punctuation and the pause after it. A
// word followed by «|» is followed by a pause inside the sentence (the speaker takes a breath).
type talkSegment struct {
	text  string
	pause float64
}

// talkHead makes a transcript of natural speech: a word takes 0.18 s plus 0.035 s a letter, 0.06 s between words,
// 0.32 s at a «|», the segment's pause after it.
func talkHead(segments []talkSegment) *transcription.Result {
	const (
		wordBase   = 0.18
		perLetter  = 0.035
		wordGap    = 0.06
		breathTime = 0.32
	)

	result := &transcription.Result{}
	at := 0.0

	for _, seg := range segments {
		start := at
		fields := strings.Fields(seg.text)
		text := make([]string, 0, len(fields))

		for i, f := range fields {
			breath := strings.HasSuffix(f, "|")
			f = strings.TrimSuffix(f, "|")
			text = append(text, f)

			for _, w := range splitWords(f) {
				end := at + wordBase + perLetter*float64(utf8.RuneCountInString(w))
				result.Words = append(result.Words, transcription.Word{Word: w, Start: at, End: end})
				at = end
			}

			switch {
			case i+1 == len(fields):
			case breath:
				at += breathTime
			default:
				at += wordGap
			}
		}

		result.Segments = append(result.Segments, transcription.Segment{Start: start, End: at, Text: strings.Join(text, " ")})
		at += seg.pause
	}

	result.Duration = at + 1

	return result
}

// syntheticTalk is about a minute of a talking head: phrases of 4–12 words, pauses of 0.15–0.6 s, the punctuation of
// Whisper (two sentences in a segment, commas, a question, a capital it did not end a sentence before, short
// sentences, breaths after «и» and before the end of a sentence, an ordinal sentence, a chain of one-word sentences,
// a question with a period, a pause inside «онлайн | тренировки», a short sentence before a longer one).
var syntheticTalk = []talkSegment{
	{text: "Привет! Поговорим о том, почему тренировки дома работают.", pause: 0.45},
	{text: "Многие думают, что без тренера рядом ничего не получится.", pause: 0.3},
	{text: "Но это не так, и я покажу почему.", pause: 0.6},
	{text: "Первое.", pause: 0.25},
	{text: "Онлайн тренер видит технику по видео и| поправляет её сразу после подхода.", pause: 0.4},
	{text: "Кто то спросит А зачем, если спортзал есть рядом с домом.", pause: 0.2},
	{text: "А мне все равно, где вы тренируетесь, если вы делаете это регулярно.", pause: 0.5},
	{text: "Второе.", pause: 0.35},
	{text: "Я работаю с людьми из разных стран, и| каждый из них занимается в своём темпе.", pause: 0.15},
	{text: "Думаете, онлайн тренировки менее эффективны?", pause: 0.3},
	{text: "Нет, если есть план, обратная связь и| ваше желание.", pause: 0.45},
	{text: "И третье.", pause: 0.2},
	{text: "Еще никто не пожалел, что начал заниматься со мной онлайн.", pause: 0.6},
	{text: "Расскажите, что вам мешает начать, и| мы вместе подберём простую программу.", pause: 0.3},
	{text: "Подскажу. Поправлю. Поддержу.", pause: 0.3},
	{text: "А вдруг онлайн| тренировки без тренажеров ничего не дадут.", pause: 0.4},
	{text: "Я веду и оффлайн занятия. Оставьте отзыв под видео. И я отвечу каждому из вас лично.", pause: 0.3},
}

// TestSyntheticTalkPhrases: on a talking head of about a minute the trainer's subtitles are whole phrases: no subtitle has
// the end of one sentence and the start of the next one (T3.27), a short one (fewer than three words or shown less
// than 1.5 s) is a sentence of its own («второе», «и третье»), the others are shown at least 1.5 s (the last aside),
// at most two lines of at most 30 characters, no line ending with a preposition, a conjunction or «не», the
// orthography fixed, a chain of one-word sentences with commas.
func TestSyntheticTalkPhrases(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	result := talkHead(syntheticTalk)
	require.InDelta(t, 60, result.Duration, 10, "about a minute")

	cues := speechCues(&CreateSubtitlesData{Transcript: result}, &style, nil, 0, 0)
	require.NotEmpty(t, cues)

	short := 0
	all := make([]string, 0, len(cues))

	for i, c := range cues {
		text := cueString(c)
		all = append(all, text)

		assert.Zero(t, sentenceCrossings(cues, i), "the end of a sentence with the start of the next one: %q", text)

		if len(c.tokens()) < trainerPhraseMinWords || (i+1 < len(cues) && c.End-c.Start < trainerPhraseMinDuration) {
			short++

			assert.True(t, cueIsSentence(cues, i), "a short subtitle is a sentence of its own: %q", text)
		}

		assert.LessOrEqual(t, len(c.Lines), 2, text)

		for j, line := range c.Lines {
			assert.LessOrEqual(t, lineWidth(lineWidths(line)), 30, text)

			if j+1 < len(c.Lines) {
				assert.False(t, isProclitic(line[len(line)-1].Norm), "a line ends with a function word: %q", text)
			}
		}
	}

	t.Logf("subtitles: %d, short ones (sentences of their own): %d\n%s", len(cues), short, strings.Join(all, "\n"))

	assert.Equal(t, 5, short, "short subtitles: привет, кто-то спросит, первое, второе, и третье")

	joined := strings.Join(all, " | ")
	for _, want := range []string{
		"онлайн-тренер", "кто-то", "всё равно", "ещё никто", "онлайн-тренировки", "эффективны?", "| второе |",
		"| и третье |", "подскажу, поправлю, поддержу", "а вдруг онлайн-тренировки", "тренажёров", "дадут?",
		"офлайн-занятия",
	} {
		assert.Contains(t, joined, want)
	}

	assert.NotContains(t, joined, " А ", "a capital in the middle of a sentence")
}

// sentenceCrossings counts the ends of sentences inside the subtitle i that are not the end of a short sentence of its
// own going with the whole next one: one that starts the subtitle when the subtitle starts and ends sentences.
func sentenceCrossings(cues []cue, i int) int {
	tokens := cues[i].tokens()
	n := 0

	for j, tk := range tokens[:len(tokens)-1] {
		if sentenceEnds(tk) && (!cueIsSentence(cues, i) || j >= maxEnumeratorWords) {
			n++
		}
	}

	return n
}

// cueIsSentence: the subtitle i starts and ends with sentences: the one before it ends a sentence, and so does it.
func cueIsSentence(cues []cue, i int) bool {
	tokens := cues[i].tokens()
	if !sentenceEnds(tokens[len(tokens)-1]) {
		return false
	}

	if i == 0 {
		return true
	}

	prev := cues[i-1].tokens()

	return sentenceEnds(prev[len(prev)-1])
}

// TestSyntheticTalkOldPieces: the phrase style of before keeps its short pieces (the phrases are the trainer's only).
func TestSyntheticTalkOldPieces(t *testing.T) {
	t.Parallel()

	style := PhraseStyle()
	cues := speechCues(&CreateSubtitlesData{Transcript: talkHead(syntheticTalk)}, &style, nil, 0, 0)

	assert.Greater(t, shortCueCount(cues, trainerPhraseMinWords), 2, "the pieces of before")
	assert.Contains(t, cueTexts(cues), "Онлайн тренер", "no orthography")
}

// shortCueCount counts the subtitles of fewer than words words.
func shortCueCount(cues []cue, words int) int {
	n := 0

	for _, c := range cues {
		if len(c.tokens()) < words {
			n++
		}
	}

	return n
}

// cueTexts is the text of all the subtitles.
func cueTexts(cues []cue) string {
	texts := make([]string, 0, len(cues))
	for _, c := range cues {
		texts = append(texts, cueString(c))
	}

	return strings.Join(texts, " | ")
}

// timed makes the tokens of one segment said as talkHead says it (a «|» is a breath).
func timed(text string) []token {
	return tokensFromTranscript(talkHead([]talkSegment{{text: text}}))
}

// TestPhraseGroups: a subtitle is a phrase: split at a sentence end or a pause, a long one at a comma or a conjunction
// into pieces of at least four words, a short piece glued to its neighbor in its sentence first.
func TestPhraseGroups(t *testing.T) {
	t.Parallel()

	p := trainerPhrases()
	limits := layoutLimits{maxLines: 2, maxLineChars: 30}

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "two sentences",
			input:    "Сегодня делаем присед со штангой. Держим спину ровной все подходы.",
			expected: []string{"Сегодня делаем присед со штангой.", "Держим спину ровной все подходы."},
		},
		{
			name:  "a long phrase at a comma",
			input: "Многие мои подопечные тренируются дома без зала, и у них всё получается с первого месяца.",
			expected: []string{
				"Многие мои подопечные тренируются дома без зала,", "и у них всё получается с первого месяца.",
			},
		},
		{
			name:     "a head after a breath is glued forward",
			input:    "И| каждый из нас может начать прямо сегодня утром.",
			expected: []string{"И каждый из нас может начать прямо сегодня утром."},
		},
		{
			name:     "a tail after a breath is glued back",
			input:    "Мы начинаем с простого упражнения| сегодня.",
			expected: []string{"Мы начинаем с простого упражнения сегодня."},
		},
		{
			name:     "an ordinal said quickly goes with the next sentence",
			input:    "Первое. Тренер видит технику по видео.",
			expected: []string{"Первое: тренер видит технику по видео."},
		},
		{
			name:  "an ordinal said quickly goes with the first piece of a long sentence",
			input: "Второе. Они видят технику по видео, и поправляют её сразу после подхода.",
			expected: []string{
				"Второе: они видят технику по видео,", "и поправляют её сразу после подхода.",
			},
		},
		{
			name:     "a short sentence goes with the whole next one only",
			input:    "Привет. Тренер видит технику по видео, и поправляет её сразу после подхода.",
			expected: []string{"Привет.", "Тренер видит технику по видео,", "и поправляет её сразу после подхода."},
		},
		{
			name:     "the end of a sentence is not glued to the next one",
			input:    "Мы занимаемся по видео дома каждый день вместе. Да. Это работает.",
			expected: []string{"Мы занимаемся по видео дома каждый день вместе.", "Да. Это работает."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			groups := phraseGroups(buildUnits(timed(tt.input), limits.maxLineChars), limits, &p, nil)

			got := make([]string, 0, len(groups))
			for _, g := range groups {
				got = append(got, unitsText(g))
			}

			assert.Equal(t, tt.expected, got)
		})
	}
}

// TestBestCutNotAfterPreposition: a long phrase is not cut between a preposition and its word, even where that is the
// most even cut («три раза в | неделю»).
func TestBestCutNotAfterPreposition(t *testing.T) {
	t.Parallel()

	p := trainerPhrases()
	limits := layoutLimits{maxLines: 1, maxLineChars: 30}
	groups := phraseGroups(buildUnits(timed("Тренируйтесь три раза в| неделю по сорок минут подряд."), 30), limits, &p, nil)

	got := make([]string, 0, len(groups))
	for _, g := range groups {
		got = append(got, unitsText(g))
	}

	assert.Equal(t, []string{"Тренируйтесь три раза в неделю", "по сорок минут подряд."}, got)
	assert.Equal(t, brokenCut, cutSense(tok("в", 0, 0.1), tok("неделю", 0.5, 0.9)))
	assert.Equal(t, brokenCut, cutSense(tok("могу", 0, 0.1), tok("ли", 0.5, 0.9)))
	assert.Equal(t, 0, cutSense(tok("неделю", 0, 0.1), tok("по", 0.5, 0.9)))
}

// TestSplitPhrasesAfterConjunction: a pause right after «и» does not end the phrase, unless it is longer than hold_gap.
func TestSplitPhrasesAfterConjunction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		pause    float64
		expected int
	}{
		{name: "a breath", pause: 0.4, expected: 1},
		{name: "a long pause", pause: 1.2, expected: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			at := 0.3 + tt.pause
			tokens := []token{
				tok("мы", 0, 0.1), tok("начнём", 0.15, 0.25), tok("и", 0.27, 0.3),
				tok("продолжим", at, at+0.4), withBreak(tok("завтра.", at+0.45, at+0.8), strongBreak),
			}

			assert.Len(t, splitPhrases(buildUnits(tokens, 30), trainerPhraseGap, trainerPhraseHoldGap), tt.expected)
		})
	}
}

// TestPhraseGroupsBarrier: a short piece is not glued across a section heading.
func TestPhraseGroupsBarrier(t *testing.T) {
	t.Parallel()

	p := trainerPhrases()
	limits := layoutLimits{maxLines: 2, maxLineChars: 30}
	tokens := timed("Первое. Тренер видит технику по видео.")

	groups := phraseGroups(buildUnits(tokens, limits.maxLineChars), limits, &p, []float64{tokens[1].Start})

	require.Len(t, groups, 2)
	assert.Equal(t, "Первое.", unitsText(groups[0]))
}

func unitsText(units []unit) string {
	words := make([]string, 0, len(units))
	for _, u := range units {
		for _, tk := range u.tokens {
			words = append(words, tk.Text)
		}
	}

	return strings.Join(words, " ")
}

// TestPhraseLines: the lines are cut by sense: not after a preposition, a conjunction or «не», after a comma when it
// is as good, otherwise as even as possible.
func TestPhraseLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []token
		expected []string
	}{
		{
			name:     "one line",
			input:    seq("держим", "спину", "ровной"),
			expected: []string{"держим спину ровной"},
		},
		{
			name: "not after a conjunction cut off by a pause",
			input: []token{
				tok("каждый", 0, 0.3), tok("из", 0.35, 0.4), tok("нас", 0.45, 0.6), tok("сможет", 0.65, 0.9),
				tok("начать", 0.95, 1.2), tok("и", 1.25, 1.3), tok("продолжить", 1.7, 2.1),
				tok("без", 2.15, 2.3), tok("перерыва", 2.35, 2.8),
			},
			expected: []string{"каждый из нас сможет начать", "и продолжить без перерыва"},
		},
		{
			name: "after a comma",
			input: []token{
				tok("если", 0, 0.2), tok("есть", 0.25, 0.4), withBreak(tok("план,", 0.45, 0.7), softBreak),
				tok("обратная", 0.75, 1.1), tok("связь", 1.15, 1.4), tok("и", 1.45, 1.5), tok("желание", 1.55, 1.9),
			},
			expected: []string{"если есть план,", "обратная связь и желание"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			lines := phraseLines(buildUnits(tt.input, 30), 30)

			got := make([]string, 0, len(lines))
			for _, line := range lines {
				got = append(got, strings.Join(texts(line), " "))
			}

			assert.Equal(t, tt.expected, got)
		})
	}
}

// TestPhraseTiming: a phrase stays at least min_duration and until the next one across a pause up to hold_gap.
func TestPhraseTiming(t *testing.T) {
	t.Parallel()

	p := trainerPhrases()
	timing := phraseTiming(&p)
	cues := []cue{{Start: 1, End: 1.6}, {Start: 3.5, End: 5}, {Start: 5.6, End: 7}, {Start: 10, End: 10.5}}

	got := timeCuesWith(cues, timing)

	require.Len(t, got, 4)
	assert.InDelta(t, 2.45, got[0].End, 1e-9, "min_duration")
	assert.InDelta(t, 5.55, got[1].End, 1e-9, "held until the next one")
	assert.InDelta(t, 7.1, got[2].End, 1e-9, "not across a long pause")
	assert.InDelta(t, 11.45, got[3].End, 1e-9)
}

// TestValidatePhrases: the values of the phrases and the fade-in are checked.
func TestValidatePhrases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		expectedErr string
	}{
		{name: "the default", input: ""},
		{name: "a pause too long", input: "layout:\n  phrases:\n    gap: 3\n", expectedErr: "layout.phrases.gap"},
		{name: "no words", input: "layout:\n  phrases:\n    min_words: 0\n", expectedErr: "min_words"},
		{name: "durations", input: "layout:\n  phrases:\n    max_duration: 2\n", expectedErr: "max_duration"},
		{name: "a hold too long", input: "layout:\n  phrases:\n    hold_gap: 5\n", expectedErr: "hold_gap"},
		{
			name: "a short sentence held longer than a phrase", input: "layout:\n  phrases:\n    min_duration_short: 2\n",
			expectedErr: "min_duration_short",
		},
		{name: "off, not checked", input: "layout:\n  phrases:\n    enabled: false\n    gap: 3\n"},
		{name: "a fade too long", input: "layout:\n  fade_in: 900\n", expectedErr: "layout.fade_in"},
		{name: "a hook fit too small", input: "hook:\n  fit_scale: 40\n", expectedErr: "hook.fit_scale"},
		{name: "a lead too long", input: "section:\n  lead: 3\n", expectedErr: "section.lead"},
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

// TestFixOrthography: the hyphens and «ё» of the dictionary, the capital of a sentence Whisper did not end.
func TestFixOrthography(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "кто-то", input: "Кто то позвонит завтра.", expected: "Кто-то позвонит завтра."},
		{name: "что-нибудь", input: "Скажите что нибудь.", expected: "Скажите что-нибудь."},
		{name: "то, что stays", input: "Лучше, чем то, что было.", expected: "Лучше, чем то, что было."},
		{name: "онлайн-тренировки", input: "Онлайн тренировки работают.", expected: "Онлайн-тренировки работают."},
		{name: "онлайн-тренер", input: "Я онлайн тренер.", expected: "Я онлайн-тренер."},
		{name: "онлайн alone", input: "Работаю онлайн с вами.", expected: "Работаю онлайн с вами."},
		{name: "всё равно", input: "Мне все равно.", expected: "Мне всё равно."},
		{name: "все stays", input: "Все люди разные.", expected: "Все люди разные."},
		{name: "это всё", input: "Вот и все.", expected: "Вот и всё."},
		{name: "ещё", input: "Еще раз.", expected: "Ещё раз."},
		{name: "её stays", input: "Спросите ее.", expected: "Спросите ее."},
		{name: "тренажёр", input: "Тренажеры и тренажерами, тренажерный зал.", expected: "Тренажёры и тренажёрами, тренажёрный зал."},
		{name: "офлайн", input: "Офлайн и оффлайн тренировки.", expected: "Офлайн и офлайн-тренировки."},
		{name: "онлайн-тренировка across a pause", input: "Мне онлайн| тренировка нравится.", expected: "Мне онлайн-тренировка нравится."},
		{name: "а что если", input: "А что если не получится.", expected: "А что если не получится?"},
		{name: "а вдруг", input: "А вдруг станет хуже", expected: "А вдруг станет хуже?"},
		{name: "почему", input: "Почему это работает.", expected: "Почему это работает?"},
		{name: "можно ли", input: "Можно ли заниматься дома.", expected: "Можно ли заниматься дома?"},
		{name: "а если", input: "А если нет времени.", expected: "А если нет времени?"},
		{name: "а если честно", input: "А если честно, это сложно.", expected: "А если честно, это сложно."},
		{name: "а если with a comma", input: "А если болит колено, делайте мягче.", expected: "А если болит колено, делайте мягче."},
		{name: "a statement with если", input: "Если болит колено, делайте мягче.", expected: "Если болит колено, делайте мягче."},
		{name: "почему with its answer", input: "Почему так, потому что спина устаёт.", expected: "Почему так, потому что спина устаёт."},
		{name: "как stays", input: "Как я уже говорила.", expected: "Как я уже говорила."},
		{name: "an exclamation stays", input: "А вдруг получится!", expected: "А вдруг получится!"},
		{name: "вряд ли stays", input: "Вряд ли это поможет.", expected: "Вряд ли это поможет."},
		{name: "навряд ли stays", input: "Навряд ли он придёт.", expected: "Навряд ли он придёт."},
		{name: "едва ли stays", input: "Едва ли это сложно.", expected: "Едва ли это сложно."},
		{name: "то ли stays", input: "То ли дождь, то ли снег.", expected: "То ли дождь, то ли снег."},
		{name: "чуть ли не stays", input: "Чуть ли не каждый день.", expected: "Чуть ли не каждый день."},
		{name: "не ли stays", input: "Не ли это странно.", expected: "Не ли это странно."},
		{name: "сколько себя помню stays", input: "Сколько себя помню, я бегала.", expected: "Сколько себя помню, я бегала."},
		{name: "сколько раз stays", input: "Сколько раз я это слышала.", expected: "Сколько раз я это слышала."},
		{name: "сколько нужно gets ?", input: "Сколько нужно заниматься.", expected: "Сколько нужно заниматься?"},
		{name: "сколько раз в gets ?", input: "Сколько раз в неделю бегать.", expected: "Сколько раз в неделю бегать?"},
		{name: "разве что stays", input: "Разве что утром.", expected: "Разве что утром."},
		{name: "разве gets ?", input: "Разве это сложно.", expected: "Разве это сложно?"},
		{name: "зачем with a comma stays", input: "Зачем это нужно, я расскажу.", expected: "Зачем это нужно, я расскажу."},
		{name: "откуда with a comma stays", input: "Откуда силы берутся, непонятно.", expected: "Откуда силы берутся, непонятно."},
		{name: "а что если честно stays", input: "А что если честно, мне лень.", expected: "А что если честно, мне лень."},
		{name: "а если, что gets ?", input: "А если болит колено, что делать.", expected: "А если болит колено, что делать?"},
		{name: "а почему gets ?", input: "А почему это важно.", expected: "А почему это важно?"},
		{name: "ну и зачем gets ?", input: "Ну и зачем это.", expected: "Ну и зачем это?"},
		{name: "ЕЩЁ in capitals", input: "ЕЩЕ РАЗ.", expected: "ЕЩЁ РАЗ."},
		{name: "четвёртое", input: "Четвертое правило.", expected: "Четвёртое правило."},
		{name: "четверть stays", input: "Четверть часа.", expected: "Четверть часа."},
		{name: "всё-таки", input: "Все таки начнём.", expected: "Всё-таки начнём."},
		{name: "из-за", input: "Из за спины.", expected: "Из-за спины."},
		{name: "по-моему before a comma", input: "По моему, хорошо.", expected: "По-моему, хорошо."},
		{name: "по моему опыту", input: "По моему опыту так.", expected: "По моему опыту так."},
		{name: "во-первых", input: "Во первых, вода.", expected: "Во-первых, вода."},
		{name: "a question stays", input: "Кто то спросит?", expected: "Кто-то спросит?"},
		{name: "потому что то", input: "Потому что то упражнение сложное.", expected: "Потому что то упражнение сложное."},
		{name: "так что то", input: "Так что то видео пригодится.", expected: "Так что то видео пригодится."},
		{name: ", чем то", input: "Сложнее, чем то упражнение.", expected: "Сложнее, чем то упражнение."},
		{name: ", как то", input: "Делайте, как то видео показывает.", expected: "Делайте, как то видео показывает."},
		{name: "как-то without a comma", input: "Я как то пробовала.", expected: "Я как-то пробовала."},
		{name: "онлайн-тренинг", input: "Мой онлайн тренинг готов.", expected: "Мой онлайн-тренинг готов."},
		{name: "a verb after онлайн", input: "Я онлайн тренируюсь.", expected: "Я онлайн тренируюсь."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, strings.Join(texts(fixOrthography(timed(tt.input))), " "))
		})
	}
}

// TestCompoundAcrossSegments: the parts of «онлайн-тренировка» are joined across the end of a Whisper segment that the
// next one goes on with a small letter (a slow question under a heading), not across the end of a sentence, a next
// segment with a capital (a new sentence: no «?» after the answer either) or a long pause.
func TestCompoundAcrossSegments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []token
		expected string
	}{
		{
			name:     "the next segment goes on",
			input:    tokensFromTranscript(speech("А что если онлайн", "тренировка хуже, чем оффлайн.")),
			expected: "А что если онлайн-тренировка хуже, чем офлайн?",
		},
		{
			name:     "a sentence ended",
			input:    tokensFromTranscript(speech("Я работаю онлайн.", "Тренировка начинается.")),
			expected: "Я работаю онлайн. Тренировка начинается.",
		},
		{
			name:     "the next segment with a capital",
			input:    tokensFromTranscript(speech("Мы работаем онлайн", "Тренировки идут по вечерам.")),
			expected: "Мы работаем онлайн Тренировки идут по вечерам.",
		},
		{
			name:     "no «?» after the answer",
			input:    tokensFromTranscript(speech("А что если вы спросите", "Отвечу по порядку.")),
			expected: "А что если вы спросите? Отвечу по порядку.",
		},
		{
			name:     "a pause over compoundMaxGap",
			input:    []token{tok("онлайн", 0, 0.5), withBreak(tok("тренировка.", 1.6, 2), strongBreak)},
			expected: "онлайн тренировка.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, strings.Join(texts(fixOrthography(tt.input)), " "))
		})
	}
}

// TestTextOrthography: the hook and the exercise titles written by the editor's model get the dictionary (a word in
// capitals stays in capitals), only in the styles with the orthography.
func TestTextOrthography(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	assert.Equal(t, []string{"ОНЛАЙН-ТРЕНИРОВКИ", "БЕЗ", "ТРЕНАЖЁРОВ"}, hookWords("онлайн тренировки без тренажеров", &style))
	assert.Equal(t, []string{"ЕЩЁ", "РАЗ"}, textOrthography([]string{"ЕЩЕ", "РАЗ"}))
	assert.Contains(t, titleEvents([]Title{{Text: "Еще 12 раз", Start: 1, End: 2}}, &style), "ЕЩЁ 12 РАЗ")

	words := WordsStyle()
	kept := hookWords("еще", &words)
	require.Len(t, kept, 1)
	assert.Equal(t, "еще", kept[0], "only the styles with the orthography")
}

// TestSectionQuestion: the first sentence of a section that starts with «а если» gets «?» even with a comma (the
// heading asks it); the same sentence elsewhere does not.
func TestSectionQuestion(t *testing.T) {
	t.Parallel()

	tokens := fixOrthography(tokensFromTranscript(speech("Мы начинаем.", "А если болит колено, делайте мягче.")))
	heading := []noteBlock{{start: tokens[2].Start, end: 10}}

	assert.Equal(t, "мягче?", sectionQuestions(tokens, heading)[len(tokens)-1].Text)
	assert.Equal(t, "мягче.", sectionQuestions(tokens, nil)[len(tokens)-1].Text)
	assert.Equal(t, "мягче.", sectionQuestions(tokens, []noteBlock{{start: tokens[0].Start, end: 10}})[len(tokens)-1].Text)
}

// TestFixOrthographyCapitals: a function word with a capital in the middle of a sentence starts one (and gets a small
// letter in the trainer's style); a name keeps its capital and the sentence.
func TestFixOrthographyCapitals(t *testing.T) {
	t.Parallel()

	tokens := fixOrthography(tokensFromTranscript(speech("Что то спросит Ну тогда поедем в Казань")))

	require.Len(t, tokens, 7)
	assert.Equal(t, "что-то", lowerFirstLetter(tokens[0].Text))
	assert.Equal(t, strongBreak, tokens[1].Break, "«Ну» starts a sentence")
	assert.Equal(t, noBreak, tokens[5].Break, "«Казань» is a name")
	assert.Equal(t, "ну", lowerSentenceStarts(tokens)[2].Text)

	merged := fixOrthography(tokensFromTranscript(speech("Кто то.")))
	require.Len(t, merged, 1)
	assert.InDelta(t, 0, merged[0].Start, 1e-9)
	assert.InDelta(t, 0.65, merged[0].End, 1e-9, "the time of both words")
	assert.Equal(t, strongBreak, merged[0].Break)
}

// TestHideHookRepeat: a phrase that repeats the hook right after it is not shown; one that only shares a word, or comes
// later, is.
func TestHideHookRepeat(t *testing.T) {
	t.Parallel()

	hook := fiveMythsHook
	phrase := func(start float64, text string) cue {
		words := strings.Fields(text)
		line := make([]token, 0, len(words))

		for i, w := range words {
			line = append(line, tok(w, start+float64(i)*0.3, start+float64(i)*0.3+0.25))
		}

		return cue{Start: start, End: start + float64(len(words))*0.3, Lines: [][]token{line}}
	}

	tests := []struct {
		name     string
		input    cue
		expected int
	}{
		{name: "the hook said again", input: phrase(2.6, "развеиваю пять мифов об онлайн тренировках"), expected: 0},
		{name: "under the hook", input: phrase(0.5, "развеиваю пять мифов об онлайн-тренировках"), expected: 0},
		{name: "a word in common", input: phrase(2.6, "онлайн это удобно и быстро"), expected: 1},
		{name: "function words only in common", input: phrase(2.6, "об этом и о том"), expected: 1},
		{name: "the hook in a longer phrase", input: phrase(2.6, "сегодня я развеиваю мифы об онлайн тренировках, "+
			"которые мешают вам начать заниматься дома"), expected: 1},
		{name: "later", input: phrase(5, "развеиваю пять мифов об онлайн тренировках"), expected: 1},
		{name: "the topic in common", input: phrase(2.6, "а если онлайн-тренировка менее эффективна"), expected: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Len(t, hideHookRepeat([]cue{tt.input}, hook, 2.5), tt.expected)
			assert.Len(t, hideHookRepeat([]cue{tt.input}, "", 2.5), 1, "no hook")
		})
	}
}

// TestLeadUnderSections: the question under a heading is shown on time, but not before section.lead after the heading,
// and never dropped; a subtitle running into the heading ends at it.
func TestLeadUnderSections(t *testing.T) {
	t.Parallel()

	heading := []noteBlock{{start: 10, end: 14}}

	tests := []struct {
		name     string
		input    []cue
		expected []cue
	}{
		{
			name:     "the question after the reading on time",
			input:    []cue{{Start: 8, End: 10.5}, {Start: 10.6, End: 13.5}},
			expected: []cue{{Start: 8, End: 10}, {Start: 10.6, End: 13.5}},
		},
		{
			name:     "the question on the heading (its reading cut out) a moment after it",
			input:    []cue{{Start: 9.95, End: 13}, {Start: 13, End: 15}},
			expected: []cue{{Start: 10.2, End: 13}, {Start: 13, End: 15}},
		},
		{
			name:     "a short question is not lost",
			input:    []cue{{Start: 10, End: 10.5}, {Start: 12, End: 13}},
			expected: []cue{{Start: 10.2, End: 10.7}, {Start: 12, End: 13}},
		},
		{
			name:     "the next subtitle close to the heading",
			input:    []cue{{Start: 10, End: 10.4}, {Start: 10.6, End: 12}},
			expected: []cue{{Start: 10.2, End: 10.6}, {Start: 10.6, End: 12}},
		},
		{
			name:     "two subtitles start in the lead",
			input:    []cue{{Start: 10, End: 10.1}, {Start: 10.1, End: 12}},
			expected: []cue{{Start: 10.2, End: 12}},
		},
		{
			name:     "a phrase left too short before the heading goes",
			input:    []cue{{Start: 9.6, End: 11}, {Start: 11, End: 13}},
			expected: []cue{{Start: 11, End: 13}},
		},
		{name: "apart", input: []cue{{Start: 1, End: 3}}, expected: []cue{{Start: 1, End: 3}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, leadUnderSections(tt.input, heading, trainerSectionLead, nil))
		})
	}
}

// TestLeadFromJoint: the heading stands 0.2 s after the joint the question starts at (on the late start Whisper gives
// its first word): the question shows at most 0.3 s after the joint, not before the heading; a joint further before the
// heading is not its own, and the lead counts from the heading.
func TestLeadFromJoint(t *testing.T) {
	t.Parallel()

	heading := []noteBlock{{start: 10.2, end: 14}}
	question := []cue{{Start: 10.15, End: 13}}

	got := leadUnderSections(question, heading, trainerSectionLead, []float64{0, 10})
	require.Len(t, got, 1)
	assert.InDelta(t, 10.2, got[0].Start, 1e-9, "with the heading")
	assert.LessOrEqual(t, got[0].Start-10, 0.3, "at most 0.3 s after the voice")

	early := leadUnderSections(question, []noteBlock{{start: 10.05, end: 14}}, trainerSectionLead, []float64{10})
	assert.InDelta(t, 10.2, early[0].Start, 1e-9, "lead after the joint")

	far := leadUnderSections(question, heading, trainerSectionLead, []float64{9.5})
	assert.InDelta(t, 10.4, far[0].Start, 1e-9, "lead after the heading")

	assert.Equal(t, []float64{0, 2, 2.5}, cutJoints(&ffmpeg.Cut{Segments: []ffmpeg.Segment{
		{Start: 5, End: 7}, {Start: 10, End: 10.5}, {Start: 20, End: 21},
	}}))
	assert.Nil(t, cutJoints(nil))
}

// TestTrainerSectionLead: in the document the question under a heading is shown with its speech and the heading stays
// to its end — when the reading «страх номер один» is said (hidden: the heading stands alone while it is said) and when
// the editor cut it out of the sound and the heading starts on the first word of the question (the subtitle a moment
// after the heading).
func TestTrainerSectionLead(t *testing.T) {
	t.Parallel()

	const question = "А что если онлайн-тренировка менее эффективна для меня?"

	tests := []struct {
		name     string
		input    *transcription.Result
		at       string
		expected []string
	}{
		{
			name: "the reading said", input: speech(hookPhrase, fear1Phrase, question), at: "Страх",
			// the reading 1.9..2.95 (hidden), the question 3.45..6.25 on time
			expected: []string{"Dialogue: 0,0:00:03.40,0:00:06.30,Main,", "Dialogue: 2,0:00:01.90,0:00:06.35,Section,"},
		},
		{
			name: "the reading cut out", input: speech(hookPhrase, question), at: "А",
			// the question 1.9..4.7: from 2.1 s (section.lead 0.2 s: at most 0.3 s after the voice)
			expected: []string{"Dialogue: 0,0:00:02.10,0:00:04.75,Main,", "Dialogue: 2,0:00:01.90,0:00:04.80,Section,"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			style := DefaultStyle()
			notes := []Note{{Kind: SectionNote, Text: fear1, Start: wordStart(t, tt.input, tt.at)}}
			ass, cues := buildDocument(&CreateSubtitlesData{Transcript: tt.input, Hook: trainerHook, Notes: notes}, &style)

			assert.Equal(t, 1, cues, "only the question")
			assert.NotContains(t, ass, "номер один")

			for _, want := range tt.expected {
				assert.Contains(t, ass, want)
			}
		})
	}
}

// TestFadeIns: a subtitle after a gap fades in (at most over half of its time), one right after another does not.
func TestFadeIns(t *testing.T) {
	t.Parallel()

	cues := []cue{{Start: 0, End: 2}, {Start: 2, End: 4}, {Start: 4.5, End: 6}, {Start: 7, End: 7.2}}

	assert.Equal(t, []int{200, 0, 200, 100}, fadeIns(cues, 200))
	assert.Equal(t, []int{0, 0, 0, 0}, fadeIns(cues, 0))
	assert.Empty(t, fadePrefix(0))
	assert.Equal(t, `{\fad(200,0)}`, fadePrefix(200))
}

// TestTrainerHookTwoLines: the trainer's hook takes two lines made smaller rather than three; with the fit of before
// the same hook takes three; a hook of two clauses takes two lines too (T3.27), smaller.
func TestTrainerHookTwoLines(t *testing.T) {
	t.Parallel()

	const hook = fiveMythsHook

	style := DefaultStyle()
	assert.Equal(t, 2, style.hookLineCount(hook))
	assert.Equal(t, 2, style.hookLineCount(trainerHook))

	words := hookWords(hook, &style)
	sel := selectionOf(words, selectLines(words, &style), &style)
	assert.GreaterOrEqual(t, sel.scale, trainerHookFitScale)
	assert.Less(t, sel.scale, fullScaleX, "made smaller")

	before := style
	before.Hook.FitScale = 0
	assert.Equal(t, 3, before.hookLineCount(hook))

	// A hook of two clauses: two lines made smaller still (at least minHookShrinkScale), its bottom above the face.
	assert.Equal(t, 2, style.hookLineCount(twoClauseHook))

	long := hookWords(twoClauseHook, &style)
	lines := selectLines(long, &style)
	sel = selectionOf(long, lines, &style)
	require.Len(t, lines, 2)
	assert.GreaterOrEqual(t, fitScaleOf(long, lines, &style), minHookShrinkScale, "the lines fit between the margins")
	assert.Less(t, sel.scale, trainerHookFitScale)
	assert.LessOrEqual(t, sel.center.y+float64(2*style.Hook.Size*sel.scale)/percentBase/2, float64(longHookBottom))

	// About 75 characters: still two lines, at trainerTwoLineScale at the least.
	t.Logf("%d characters", utf8.RuneCountInString(longHook))
	assert.Equal(t, 2, style.hookLineCount(longHook))

	longest := hookWords(longHook, &style)
	lines = selectLines(longest, &style)
	require.Len(t, lines, 2)
	assert.GreaterOrEqual(t, fitScaleOf(longest, lines, &style), trainerTwoLineScale)
	assert.Equal(t, 3, before.hookLineCount(longHook), "three lines with the fit of before")
}

// TestTrainerDocumentHookRepeat: in the document the phrase that says the hook again right after it is not shown, and
// the subtitles fade in.
func TestTrainerDocumentHookRepeat(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	result := speech("Хотите заниматься дома без зала?", "Развеиваю пять мифов об онлайн тренировках.",
		"Мои подопечные тренируются дома и получают результат.")

	ass, _ := buildDocument(&CreateSubtitlesData{Transcript: result, Hook: fiveMythsHook}, &style)

	assert.NotContains(t, ass, "развеиваю пять", "the hook is not repeated")
	assert.Contains(t, ass, `,Main,,0,0,0,,{\fad(200,0)}мои подопечные`)
}
