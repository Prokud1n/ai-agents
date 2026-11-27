package subtitles

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLeadLateWordAtJoint: the question under a heading at a joint is shown lead after the joint even when Whisper
// starts its first word later than that (T3.31: 0.35 s after the voice before); the speech after a reading of the
// heading, or a heading without a joint, keeps its own start.
func TestLeadLateWordAtJoint(t *testing.T) {
	t.Parallel()

	heading := []noteBlock{{start: 10.2, end: 14}}

	tests := []struct {
		name     string
		input    cue
		joints   []float64
		expected float64
	}{
		{name: "the late first word at the joint", input: cue{Start: 10.4, End: 13}, joints: []float64{0, 10}, expected: 10.2},
		{name: "the latest one pulled", input: cue{Start: 10.5, End: 13}, joints: []float64{10}, expected: 10.2},
		{name: "the speech after a reading", input: cue{Start: 10.9, End: 13}, joints: []float64{10}, expected: 10.9},
		{name: "no joint", input: cue{Start: 10.45, End: 13}, expected: 10.45},
		{name: "a joint too far before", input: cue{Start: 10.45, End: 13}, joints: []float64{9.5}, expected: 10.45},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := leadUnderSections([]cue{tt.input}, heading, trainerSectionLead, tt.joints)
			require.Len(t, got, 1)
			assert.InDelta(t, tt.expected, got[0].Start, 1e-9)
			assert.InDelta(t, tt.input.End, got[0].End, 1e-9)
		})
	}

	// Only the first subtitle of the section: the next one keeps its start.
	two := leadUnderSections([]cue{{Start: 10.4, End: 11}, {Start: 11.1, End: 12}}, heading, trainerSectionLead,
		[]float64{10})
	require.Len(t, two, 2)
	assert.InDelta(t, 10.2, two[0].Start, 1e-9)
	assert.InDelta(t, 11.1, two[1].Start, 1e-9)
}

// TestSentencesAtJoints: a word with a capital that starts a fragment of the cut right after a word of another one
// without punctuation starts a sentence (and gets a small letter in the trainer's style); not without a joint, after a
// preposition (a name), after a comma or a word in capitals.
func TestSentencesAtJoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []token
		joints   []float64
		expected string
	}{
		{
			name:     "a capital after a joint",
			input:    []token{tok("этот", 0, 0.3), tok("ролик", 0.35, 0.7), tok("Разбираю", 0.85, 1.3), tok("мифы", 1.35, 1.6)},
			joints:   []float64{0, 0.8},
			expected: "этот ролик. разбираю мифы",
		},
		{
			name:     "no joint",
			input:    []token{tok("этот", 0, 0.3), tok("ролик", 0.35, 0.7), tok("Разбираю", 0.85, 1.3), tok("мифы", 1.35, 1.6)},
			joints:   []float64{0},
			expected: "этот ролик Разбираю мифы",
		},
		{
			name:     "a name after a preposition",
			input:    []token{tok("живу", 0, 0.3), tok("в", 0.35, 0.4), tok("Казани", 0.85, 1.3)},
			joints:   []float64{0, 0.8},
			expected: "живу в Казани",
		},
		{
			name:     "a comma before the capital",
			input:    []token{tok("живу", 0, 0.3), withBreak(tok("там,", 0.35, 0.6), softBreak), tok("Казань", 0.85, 1.3)},
			joints:   []float64{0, 0.8},
			expected: "живу там, Казань",
		},
		{
			name:     "a word in capitals",
			input:    []token{tok("смотрите", 0, 0.3), tok("ролик", 0.35, 0.7), tok("МФЦ", 0.85, 1.3)},
			joints:   []float64{0, 0.8},
			expected: "смотрите ролик МФЦ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			before := slices.Clone(tt.input)
			got := lowerSentenceStarts(sentencesAtJoints(tt.input, tt.joints))

			words := make([]string, 0, len(got))
			for _, tk := range got {
				w := tk.Text
				if tk.Break == strongBreak && !strings.HasSuffix(w, ".") {
					w += "."
				}

				words = append(words, w)
			}

			assert.Equal(t, tt.expected, strings.Join(words, " "))
			assert.Equal(t, before, tt.input, "the input is not changed")
		})
	}
}

// TestTrainerCapitalAfterJoint: in the trainer's subtitles of a cut the word Whisper capitalized after the removed take
// is small and starts its own subtitle.
func TestTrainerCapitalAfterJoint(t *testing.T) {
	t.Parallel()

	style := DefaultStyle()
	result := talkHead([]talkSegment{{text: "Тогда посмотрите этот ролик Разбираю мифы о домашних тренировках."}})
	at := wordStart(t, result, "Разбираю")

	cues := speechCues(&CreateSubtitlesData{Transcript: result, joints: []float64{0, at}}, &style, nil, 0, 0)
	text := cueTexts(cues)

	assert.NotContains(t, text, "Разбираю")
	assert.Contains(t, text, "тогда посмотрите этот ролик | разбираю мифы о домашних тренировках")

	plain := cueTexts(speechCues(&CreateSubtitlesData{Transcript: result}, &style, nil, 0, 0))
	assert.Contains(t, plain, "Разбираю", "without a cut the capital stays: it may be a name")
}

// TestMarkLeaningSubjects: a subject pronoun after «и», «а», «но» leans on the next word when the sentence goes on.
func TestMarkLeaningSubjects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []token
		expected []bool
	}{
		{name: "и я", input: seq("пишите", "и", "я", "отвечу"), expected: []bool{false, false, true, false}},
		{name: "а мы", input: seq("а", "мы", "начнём"), expected: []bool{false, true, false}},
		{name: "но они", input: seq("но", "они", "придут"), expected: []bool{false, true, false}},
		{name: "the end of the speech", input: seq("и", "я"), expected: []bool{false, false}},
		{
			name:     "the end of a sentence",
			input:    []token{tok("и", 0, 0.1), withBreak(tok("я.", 0.15, 0.3), strongBreak), tok("Дальше", 0.5, 0.8)},
			expected: []bool{false, false, false},
		},
		{
			name:     "a comma after the conjunction",
			input:    []token{withBreak(tok("и,", 0, 0.1), softBreak), tok("я", 0.15, 0.3), tok("тоже", 0.35, 0.6)},
			expected: []bool{false, false, false},
		},
		{name: "an object", input: seq("и", "меня", "позвали"), expected: []bool{false, false, false}},
		{name: "after another word", input: seq("потом", "я", "приду"), expected: []bool{false, false, false}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := markLeaningSubjects(tt.input)

			leans := make([]bool, 0, len(got))
			for _, tk := range got {
				leans = append(leans, tk.leans)
			}

			assert.Equal(t, tt.expected, leans)
		})
	}
}

// TestTrainerNotOnSubject: in the trainer's subtitles neither a line nor a subtitle ends on «и я» when the sentence goes
// on — with a breath after the pronoun (a phrase would end there) and without one (a line would).
func TestTrainerNotOnSubject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "a breath after it", input: "Оставляйте вопросы в комментариях и я| буду разбирать каждый из них подробно."},
		{name: "a line", input: "Пишите свои вопросы в комментарии и я отвечу на каждый."},
		{name: "а мы", input: "Многие сдаются через неделю а мы| продолжаем тренироваться каждый день."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			style := DefaultStyle()
			cues := speechCues(&CreateSubtitlesData{Transcript: talkHead([]talkSegment{{text: tt.input}})}, &style, nil, 0, 0)
			require.NotEmpty(t, cues)

			for _, c := range cues {
				for _, line := range c.Lines {
					last := line[len(line)-1]
					assert.False(t, subjectPronouns[last.Norm] && last.Break == noBreak, "a line ends on a pronoun: %q",
						cueString(c))
					assert.False(t, holdsNext(last) && last.Break == noBreak, "a line ends on a function word: %q",
						cueString(c))
				}
			}

			t.Logf("subtitles: %s", cueTexts(cues))
		})
	}
}

// TestAsideCommas: «если честно», «честно говоря», «кстати» get their commas where they surely are asides; a
// conditional «если честно …», the adverb «кстати», a comparative «короче» and an aside in the middle without a comma
// stay as they are.
func TestAsideCommas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "если честно at the end", input: "Мне все равно если честно.", expected: "Мне всё равно, если честно."},
		{name: "честно говоря at the end", input: "Это было сложно честно говоря.", expected: "Это было сложно, честно говоря."},
		{name: "честно говоря at the start", input: "Честно говоря я устала.", expected: "Честно говоря, я устала."},
		{name: "кстати at the start", input: "Кстати это важно.", expected: "Кстати, это важно."},
		{name: "a comma after it", input: "Это кстати, важно.", expected: "Это, кстати, важно."},
		{name: "a comma there", input: "Мне все равно, если честно.", expected: "Мне всё равно, если честно."},
		{name: "a conditional stays", input: "Если честно ответите, получите приз.", expected: "Если честно ответите, получите приз."},
		{name: "the adverb stays", input: "Это было очень кстати.", expected: "Это было очень кстати."},
		{name: "the middle stays", input: "Это кстати важно.", expected: "Это кстати важно."},
		{name: "честно говоря in the middle stays", input: "Он отвечал честно говоря правду.", expected: "Он отвечал честно говоря правду."},
		{name: "короче stays", input: "Сделай ролик короче.", expected: "Сделай ролик короче."},
		{name: "a question stays", input: "А что если честно.", expected: "А что если честно."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, strings.Join(texts(fixOrthography(timed(tt.input))), " "))
		})
	}
}

// TestPhraseLinesNotOneWord: a word with a comma is not left alone on a line when the other line takes it.
func TestPhraseLinesNotOneWord(t *testing.T) {
	t.Parallel()

	input := []token{
		withBreak(tok("думаете,", 0, 0.4), softBreak), tok("онлайн-тренировки", 0.5, 1.2), tok("не", 1.25, 1.3),
		tok("для", 1.35, 1.5), withBreak(tok("вас?", 1.55, 1.8), strongBreak),
	}

	lines := phraseLines(buildUnits(input, 30), 30)

	got := make([]string, 0, len(lines))
	for _, line := range lines {
		got = append(got, strings.Join(texts(line), " "))
	}

	assert.Equal(t, []string{"думаете, онлайн-тренировки", "не для вас?"}, got)
}
