package editplan

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

func TestApplyRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    Input
		expected OutcomeKind
	}{
		{name: "cannot", input: Input{Decision: CannotDecision, Reason: "музыка"}, expected: CannotOutcome},
		{name: "unclear", input: Input{Decision: UnclearDecision, Reason: "какой дубль?"}, expected: UnclearOutcome},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outcome, err := Apply(testRequest(2, nil), &tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, outcome.Kind)
			assert.Equal(t, tt.input.Reason, outcome.Reason)
			assert.Empty(t, outcome.Changes)
		})
	}
}

// TestApplyRejectsInvalidEdit: an edit outside the transcript or the plan, or one that leaves nothing, goes back to the
// model with the reason.
func TestApplyRejectsInvalidEdit(t *testing.T) {
	t.Parallel()

	edit := func(change func(in *Input)) Input {
		in := Input{Decision: EditDecision, Reason: "правка"}
		change(&in)

		return in
	}

	tests := []struct {
		name     string
		version  int
		input    Input
		expected string
	}{
		{name: "unknown decision", version: 2, input: Input{Decision: "music"}, expected: "decision должен быть"},
		{
			name: "a word past the transcript", version: 2,
			input:    edit(func(in *Input) { in.Remove = []roughcut.WordRange{{From: 10, To: 11}} }),
			expected: "вне расшифровки",
		},
		{
			name: "a reversed range", version: 2,
			input:    edit(func(in *Input) { in.Restore = []roughcut.WordRange{{From: 4, To: 3}} }),
			expected: "вне расшифровки",
		},
		{
			name: "an unknown candidate", version: 1,
			input:    edit(func(in *Input) { in.Candidates = []CandidateDecision{{ID: 7, Cut: true}} }),
			expected: "спорного повтора с id 7 нет",
		},
		{
			name: "a candidate decided twice", version: 1,
			input:    edit(func(in *Input) { in.Candidates = []CandidateDecision{{ID: 0, Cut: true}, {ID: 0}} }),
			expected: "решён дважды",
		},
		{
			name: "everything cut", version: 2,
			input:    edit(func(in *Input) { in.Remove = []roughcut.WordRange{{From: 0, To: 10}} }),
			expected: "от ролика остаётся",
		},
		{
			name: "a trim longer than the reel", version: 2,
			input:    edit(func(in *Input) { in.TrimStart, in.TrimEnd = 3, 4 }),
			expected: "не меньше всего видео",
		},
		{
			name: "not a number", version: 2, input: edit(func(in *Input) { in.TrimEnd = math.NaN() }),
			expected: "неотрицательные",
		},
		{
			name: "the same word cut and brought back", version: 2,
			input: edit(func(in *Input) {
				in.Remove = []roughcut.WordRange{{From: 7, To: 8}}
				in.Restore = []roughcut.WordRange{{From: 8, To: 8}}
			}),
			expected: "и вырезано, и возвращено",
		},
		{
			name: "markup in a subtitle word", version: 2,
			input:    edit(func(in *Input) { in.FixWords = []roughcut.WordFix{{Index: 7, Text: `{\b1}делаем`}} }),
			expected: "без переносов строк",
		},
		{
			name: "an empty subtitle word", version: 2,
			input:    edit(func(in *Input) { in.FixWords = []roughcut.WordFix{{Index: 7, Text: " "}} }),
			expected: "от 1 до 40 символов",
		},
		{
			name: "a sentence instead of a word", version: 2,
			input:    edit(func(in *Input) { in.FixWords = []roughcut.WordFix{{Index: 7, Text: strings.Repeat("а", 41)}} }),
			expected: "от 1 до 40 символов",
		},
		{
			name: "a subtitle word past the transcript", version: 2,
			input:    edit(func(in *Input) { in.FixWords = []roughcut.WordFix{{Index: 11, Text: "да"}} }),
			expected: "слова с индексом 11 нет",
		},
		{
			name: "too many ranges", version: 2,
			input:    edit(func(in *Input) { in.Remove = make([]roughcut.WordRange, maxRanges+1) }),
			expected: "слишком много отрезков",
		},
		{
			name: "a subtitle word as it is", version: 2,
			input:    edit(func(in *Input) { in.FixWords = []roughcut.WordFix{{Index: 7, Text: "делаем"}} }),
			expected: "ничего не меняет: в субтитрах уже «делаем»",
		},
		{
			name: "an invisible character in a subtitle word", version: 2,
			input:    edit(func(in *Input) { in.FixWords = []roughcut.WordFix{{Index: 7, Text: "дела\u200bем"}} }),
			expected: "невидимых символов",
		},
		{
			name: "a revision that changes nothing", version: 2,
			input:    edit(func(in *Input) { in.Restore = []roughcut.WordRange{{From: 8, To: 8}} }),
			expected: "ничего не меняет",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Apply(testRequest(tt.version, nil), &tt.input)

			var check *CheckError
			require.ErrorAs(t, err, &check)
			assert.Contains(t, check.Message, tt.expected)
		})
	}
}

// TestApplyFirstVersion: the disputed repeats are decided with an explanation in the decisions log; a caption that is
// only a description changes nothing and is valid.
func TestApplyFirstVersion(t *testing.T) {
	t.Parallel()

	req := testRequest(1, nil)
	req.Caption = "рилс про спину"

	outcome, err := Apply(req, &Input{
		Decision: EditDecision, Reason: "подпись — описание ролика",
		Candidates: []CandidateDecision{{ID: 0, Cut: true, Reason: "сказала заново\nполучше"}},
	})
	require.NoError(t, err)

	assert.Equal(t, EditOutcome, outcome.Kind)
	assert.Equal(t, []int{0}, outcome.Edit.Accept)
	assert.Empty(t, outcome.Edit.Remove)
	assert.Equal(t, 1, outcome.Cut)
	assert.Zero(t, outcome.Kept)
	assert.Empty(t, outcome.Changes, "the first version counts the repeats itself")
	assert.Less(t, outcome.Duration, req.Plan.Duration)
	assert.Equal(t, []string{
		"Подпись тренера к исходнику: подпись — описание ролика",
		"Повтор 0: первая фраза вырезана, оставлен повтор — сказала заново получше",
	}, outcome.Edit.Notes)

	trimmed, err := Apply(req, &Input{Decision: EditDecision, Reason: "без начала", TrimStart: 2})
	require.NoError(t, err)
	assert.Equal(t, []roughcut.WordRange{{From: 0, To: 2}}, trimmed.Edit.Remove, "the caption counts on the source")
	assert.Equal(t, []string{"Обрезал начало: убрал первые 1,8 с."}, trimmed.Changes)

	nothing, err := Apply(req, &Input{Decision: EditDecision, Reason: "подпись — описание ролика"})
	require.NoError(t, err)
	assert.Equal(t, EditOutcome, nothing.Kind)
	assert.InDelta(t, req.Plan.Duration, nothing.Duration, 1e-9)
}

// TestApplyRevision: a revision adds to the edit of the previous versions; the trainer is told what changed.
// noReasonNote is the note of a revision without the model's reason.
const noReasonNote = "Правка тренера к версии 1: —"

func TestApplyRevision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		previous        *roughcut.Edit
		input           Input
		expectedEdit    roughcut.Edit
		expectedChanges []string
	}{
		{
			name:  "the start of the reel trimmed",
			input: Input{Decision: EditDecision, TrimStart: 1.5},
			expectedEdit: roughcut.Edit{
				Remove: []roughcut.WordRange{{From: 0, To: 2}}, Notes: []string{noReasonNote},
			},
			expectedChanges: []string{"Обрезал начало: убрал первые 1,4 с."},
		},
		{
			name:  "the end trimmed",
			input: Input{Decision: EditDecision, TrimEnd: 1.5},
			expectedEdit: roughcut.Edit{
				Remove: []roughcut.WordRange{{From: 8, To: 10}}, Notes: []string{noReasonNote},
			},
			expectedChanges: []string{"Обрезал конец: убрал последние 1,4 с."},
		},
		{
			name:     "the filler brought back, the previous cut kept",
			previous: &roughcut.Edit{Remove: []roughcut.WordRange{{From: 0, To: 2}}},
			input: Input{
				Decision: EditDecision, Reason: "вернуть эээ", Restore: []roughcut.WordRange{{From: 6, To: 6}},
			},
			expectedEdit: roughcut.Edit{
				Remove: []roughcut.WordRange{{From: 0, To: 2}}, Restore: []roughcut.WordRange{{From: 6, To: 6}},
				Notes: []string{"Правка тренера к версии 1: вернуть эээ"},
			},
			expectedChanges: []string{"Вернул фрагмент «эээ»."},
		},
		{
			name:     "a previous cut brought back",
			previous: &roughcut.Edit{Remove: []roughcut.WordRange{{From: 7, To: 10}}},
			input:    Input{Decision: EditDecision, Restore: []roughcut.WordRange{{From: 7, To: 10}}},
			expectedEdit: roughcut.Edit{
				Restore: []roughcut.WordRange{{From: 7, To: 10}}, Notes: []string{noReasonNote},
			},
			expectedChanges: []string{"Вернул фрагмент «делаем по десять приседаний»."},
		},
		{
			name: "the second take kept and a word corrected",
			previous: &roughcut.Edit{
				Remove: []roughcut.WordRange{{From: 10, To: 10}}, Words: []roughcut.WordFix{{Index: 8, Text: "на"}},
			},
			input: Input{
				Decision: EditDecision, Candidates: []CandidateDecision{{ID: 0, Cut: true, Reason: "второй дубль"}},
				FixWords: []roughcut.WordFix{{Index: 8, Text: " по "}, {Index: 9, Text: "10"}},
			},
			expectedEdit: roughcut.Edit{
				Accept: []int{0}, Remove: []roughcut.WordRange{{From: 10, To: 10}},
				Words: []roughcut.WordFix{{Index: 8, Text: "по"}, {Index: 9, Text: "10"}},
				Notes: []string{
					noReasonNote, "Повтор 0: первая фраза вырезана, оставлен повтор — второй дубль",
				},
			},
			expectedChanges: []string{
				"Оставил второй дубль фразы «держим спину ровно».",
				"Исправил в субтитрах: «на» → «по».",
				"Исправил в субтитрах: «десять» → «10».",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := testRequest(2, tt.previous)

			outcome, err := Apply(req, &tt.input)
			require.NoError(t, err)

			assert.Equal(t, EditOutcome, outcome.Kind)
			assert.Equal(t, tt.expectedEdit, outcome.Edit)
			assert.Equal(t, tt.expectedChanges, outcome.Changes)
		})
	}
}

func TestDescribeChangesLimit(t *testing.T) {
	t.Parallel()

	req := testRequest(2, nil)
	in := &Input{Decision: EditDecision}

	for i := range 8 {
		in.FixWords = append(in.FixWords, roughcut.WordFix{Index: i, Text: "слово"})
	}

	lines := describeChanges(req, in, &trim{})

	assert.Len(t, lines, maxChangeLines)
	assert.Equal(t, "И ещё изменений: 3.", lines[maxChangeLines-1])
}

func TestSecondsText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    float64
		expected string
	}{
		{input: 3, expected: "первые 3 с"},
		{input: 2.5, expected: "первые 2,5 с"},
		{input: 1.04, expected: "первую секунду"},
		{input: 0.04, expected: "первые 0,1 с"},
		{input: 1.26, expected: "первые 1,3 с"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, secondsText(tt.input, "первую секунду", "первые"))
		})
	}
}

func TestOneLine(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "повтор оставлен намеренно", oneLine("повтор\n оставлен\u200b\u202eнамеренно\x00"))
	assert.Len(t, []rune(oneLine(strings.Repeat("а", maxReasonRunes*2))), maxReasonRunes+1)
}
