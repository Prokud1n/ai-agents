package roughcut

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// editSpeech has one disputed repeat (words 0–2 said again as 3–5) and a filler (word 6).
const editSpeech = "держим спину ровно. | держим спину ровно. || эээ делаем по десять приседаний."

func TestBuildPlanEdited(t *testing.T) {
	t.Parallel()

	src := speech(editSpeech)

	tests := []struct {
		name               string
		edit               Edit
		expected           string
		expectedCandidates []int
	}{
		{
			name:               "no changes: the automatic plan",
			expected:           "держим спину ровно держим спину ровно делаем по десять приседаний",
			expectedCandidates: []int{0},
		},
		{
			name:               "the first words removed",
			edit:               Edit{Remove: []WordRange{{From: 0, To: 2}}},
			expected:           "держим спину ровно делаем по десять приседаний",
			expectedCandidates: []int{0},
		},
		{
			name:               "the filler brought back",
			edit:               Edit{Restore: []WordRange{{From: 6, To: 6}}},
			expected:           allWordsKept,
			expectedCandidates: []int{0},
		},
		{
			name:     "the second take kept and the end cut",
			edit:     Edit{Accept: []int{0}, Remove: []WordRange{{From: 9, To: 10}}},
			expected: "держим спину ровно делаем по",
		},
		{
			name:               "a word cut and brought back is cut",
			edit:               Edit{Remove: []WordRange{{From: 7, To: 7}}, Restore: []WordRange{{From: 7, To: 7}}},
			expected:           "держим спину ровно держим спину ровно по десять приседаний",
			expectedCandidates: []int{0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plan, err := BuildPlanEdited(src, src.Duration, testSettings(), &tt.edit)
			require.NoError(t, err)

			assert.Equal(t, tt.expected, keptText(src, plan.Keep))

			ids := make([]int, 0, len(plan.Candidates))
			for _, c := range plan.Candidates {
				ids = append(ids, c.ID)
			}

			if tt.expectedCandidates == nil {
				tt.expectedCandidates = []int{}
			}

			assert.Equal(t, tt.expectedCandidates, ids, "the IDs do not depend on the edit")
		})
	}
}

func TestBuildPlanEditedRejectsInvalidEdit(t *testing.T) {
	t.Parallel()

	src := speech(editSpeech)

	tests := []struct {
		name string
		edit Edit
	}{
		{name: "past the last word", edit: Edit{Remove: []WordRange{{From: 10, To: 11}}}},
		{name: "negative", edit: Edit{Restore: []WordRange{{From: -1, To: 0}}}},
		{name: "reversed", edit: Edit{Remove: []WordRange{{From: 3, To: 2}}}},
		{name: "unknown candidate", edit: Edit{Accept: []int{5}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := BuildPlanEdited(src, src.Duration, testSettings(), &tt.edit)
			require.ErrorIs(t, err, ErrInvalidPlan)
		})
	}
}

func TestCandidateWords(t *testing.T) {
	t.Parallel()

	src := speech(editSpeech)
	plan := BuildPlan(src, src.Duration, testSettings())

	require.Len(t, plan.Candidates, 1)
	assert.Equal(t, WordRange{From: 0, To: 2}, plan.Candidates[0].EarlierWords)
	assert.Equal(t, WordRange{From: 3, To: 5}, plan.Candidates[0].LaterWords)
}

func TestFixWords(t *testing.T) {
	t.Parallel()

	src := speech("привет, это Прокудино. | держим планку!")

	tests := []struct {
		name          string
		fixes         []WordFix
		expectedWord  string
		expectedFirst string
		expectedText  string
	}{
		{
			name:          "punctuation around the word stays",
			fixes:         []WordFix{{Index: 2, Text: " Прокудина "}},
			expectedWord:  "Прокудина",
			expectedFirst: " привет, это Прокудина.",
			expectedText:  "привет, это Прокудина. держим планку!",
		},
		{
			name:          "invalid fixes are skipped",
			fixes:         []WordFix{{Index: 9, Text: "x"}, {Index: 2, Text: "  "}},
			expectedWord:  "Прокудино",
			expectedFirst: " привет, это Прокудино.",
			expectedText:  "привет, это Прокудино. | держим планку!",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fixed := FixWords(src, tt.fixes)

			assert.Equal(t, tt.expectedWord, fixed.Words[2].Word)
			assert.Equal(t, tt.expectedFirst, fixed.Segments[0].Text)
			assert.Equal(t, tt.expectedText, fixed.Text)
			assert.Equal(t, "Прокудино", src.Words[2].Word, "the source is not changed")
		})
	}
}

func TestDecisionsLogOfEdit(t *testing.T) {
	t.Parallel()

	src := speech(editSpeech)
	edit := Edit{Remove: []WordRange{{From: 8, To: 10}}, Notes: []string{"Правка тренера к версии 1: без конца"}}

	plan, err := BuildPlanEdited(src, src.Duration, testSettings(), &edit)
	require.NoError(t, err)

	plan.Edit = &edit
	log := DecisionsLog(&plan)

	assert.Contains(t, log, "Вырезано по правкам: 1")
	assert.Contains(t, log, "«по десять приседаний»")
	assert.Contains(t, log, "Решения монтажёра: 1\n- Правка тренера к версии 1: без конца")
}

// TestDecisionsLogCountsDecisionsOnly: the details of a decision (the length of a shortened reel, the phrases left
// out) are listed after the decisions and are not counted as decisions.
func TestDecisionsLogCountsDecisionsOnly(t *testing.T) {
	t.Parallel()

	src := speech(editSpeech)
	edit := Edit{
		Remove: []WordRange{{From: 8, To: 10}}, Notes: []string{"Отбор под рилс: главное"},
		Details: []string{"Длина рилс: 0:03.0 (рекомендуется 2–4 с)", "Не вошло в рилс: слова 8–10 «по десять приседаний»"},
	}

	plan, err := BuildPlanEdited(src, src.Duration, testSettings(), &edit)
	require.NoError(t, err)

	plan.Edit = &edit
	log := DecisionsLog(&plan)

	assert.Contains(t, log, "Решения монтажёра: 1\n- Отбор под рилс: главное\n")
	assert.Contains(t, log, "Подробности:\n- Длина рилс: 0:03.0 (рекомендуется 2–4 с)\n- Не вошло в рилс: слова 8–10")
}
