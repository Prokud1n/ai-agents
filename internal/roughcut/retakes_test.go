package roughcut

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildPlanRetakes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		input              string
		expected           string
		expectedKinds      []TakeKind
		expectedCandidates []CandidateReason
	}{
		// Real restarts are cut.
		{
			name:          "word broken off and the phrase restarted",
			input:         "давайте при… | давайте приступим к разминке.",
			expected:      "давайте приступим к разминке",
			expectedKinds: []TakeKind{IncompleteTakeKind},
		},
		{
			name:          "restart inside a phrase",
			input:         "итак давайте при… | давайте приступим к разминке.",
			expected:      "итак давайте приступим к разминке",
			expectedKinds: []TakeKind{IncompleteTakeKind},
		},
		{
			name:          "series of restarts keeps the last take",
			input:         "давайте при… | давайте присту… | давайте приступим.",
			expected:      "давайте приступим",
			expectedKinds: []TakeKind{IncompleteTakeKind, IncompleteTakeKind},
		},
		{
			name:          "short start left hanging and said again in full",
			input:         "сегодня мы… | сегодня мы разберём присед.",
			expected:      "сегодня мы разберём присед",
			expectedKinds: []TakeKind{RepeatedTakeKind},
		},
		{
			name:          "phrase broken off after several words and said in full",
			input:         "колени смотрят в сторону но… | колени смотрят в сторону носков.",
			expected:      "колени смотрят в сторону носков",
			expectedKinds: []TakeKind{IncompleteTakeKind},
		},
		{
			name:          "broken take with a changed word",
			input:         "сегодня мы сделаем трен… | сегодня мы проведём тренировку на ноги.",
			expected:      "сегодня мы проведём тренировку на ноги",
			expectedKinds: []TakeKind{IncompleteTakeKind},
		},
		{
			name:          "filler at the break is removed with the failed take",
			input:         "держим спину ровной и э… | держим спину ровной и не сутулимся.",
			expected:      "держим спину ровной и не сутулимся",
			expectedKinds: []TakeKind{RepeatedTakeKind},
		},
		// Ordinary speech of a trainer is kept; complete repeats become candidates for the editor.
		{
			name:               "enumeration",
			input:              "делаем по десять приседаний. | делаем по десять выпадов. | делаем по десять отжиманий.",
			expected:           "делаем по десять приседаний делаем по десять выпадов делаем по десять отжиманий",
			expectedCandidates: []CandidateReason{RhythmCandidateReason, RhythmCandidateReason},
		},
		{
			name: "movement rhythm",
			input: "опускаемся вниз, поднимаемся вверх. | опускаемся вниз, поднимаемся вверх. | " +
				"опускаемся вниз, поднимаемся вверх.",
			expected: "опускаемся вниз поднимаемся вверх опускаемся вниз поднимаемся вверх " +
				"опускаемся вниз поднимаемся вверх",
			expectedCandidates: []CandidateReason{RhythmCandidateReason, RhythmCandidateReason},
		},
		{
			name:               "counting with movements",
			input:              "раз вниз, два вверх. | раз вниз, два вверх. | раз вниз, два вверх.",
			expected:           "раз вниз два вверх раз вниз два вверх раз вниз два вверх",
			expectedCandidates: []CandidateReason{RhythmCandidateReason, RhythmCandidateReason},
		},
		{
			name:               "breathing said again and continued",
			input:              "вдох, выдох. | вдох, выдох и опускаемся.",
			expected:           "вдох выдох вдох выдох и опускаемся",
			expectedCandidates: []CandidateReason{RepeatCandidateReason},
		},
		{
			name:               "breathing in one sentence with commas",
			input:              "вдох, выдох, | вдох, выдох и опускаемся.",
			expected:           "вдох выдох вдох выдох и опускаемся",
			expectedCandidates: []CandidateReason{RepeatCandidateReason},
		},
		{
			name:               "cue said twice",
			input:              "держим спину ровно. | держим спину ровно.",
			expected:           "держим спину ровно держим спину ровно",
			expectedCandidates: []CandidateReason{RepeatCandidateReason},
		},
		{
			name:               "countdown said again and continued",
			input:              "ещё десять секунд. | ещё десять секунд держим.",
			expected:           "ещё десять секунд ещё десять секунд держим",
			expectedCandidates: []CandidateReason{RepeatCandidateReason},
		},
		{
			name:               "whole phrase said again with one word changed",
			input:              "сегодня мы сделаем тренировку на ноги. | сегодня мы проведём тренировку на ноги.",
			expected:           "сегодня мы сделаем тренировку на ноги сегодня мы проведём тренировку на ноги",
			expectedCandidates: []CandidateReason{RepeatCandidateReason},
		},
		{
			name:               "emphasis",
			input:              "ещё раз! | ещё раз!",
			expected:           "ещё раз ещё раз",
			expectedCandidates: []CandidateReason{RepeatCandidateReason},
		},
		{
			name:     "word forms: one letter more",
			input:    "для ног | ноги ставим на ширине плеч",
			expected: "для ног ноги ставим на ширине плеч",
		},
		{
			name:     "word forms: a finished sentence before",
			input:    "это упражнение присед. | приседаем медленно.",
			expected: "это упражнение присед приседаем медленно",
		},
		{
			name:     "word forms: diminutive after a finished sentence",
			input:    "ягодичный мост. | мостик делаем так.",
			expected: "ягодичный мост мостик делаем так",
		},
		{
			name:     "word forms in one breath",
			input:    "в зал в зале есть всё",
			expected: "в зал в зале есть всё",
		},
		{
			name:     "similar phrases with different exercises",
			input:    "делаем десять приседаний | делаем десять выпадов",
			expected: "делаем десять приседаний делаем десять выпадов",
		},
		{
			name:     "different phrases with the same start",
			input:    "отдыхаем тридцать секунд | отдыхаем и переходим к планке",
			expected: "отдыхаем тридцать секунд отдыхаем и переходим к планке",
		},
		{
			name:     "counting repetitions",
			input:    "раз два три | раз два три четыре",
			expected: "раз два три раз два три четыре",
		},
		{
			name:     "word that starts like the next one",
			input:    "при приседании колени не выходят за носки",
			expected: "при приседании колени не выходят за носки",
		},
		{
			name:     "prepositions before longer words",
			input:    "за завтраком и на наш сайт",
			expected: "за завтраком и на наш сайт",
		},
		{
			name:     "repeated words inside a phrase",
			input:    "ноги на ширине плеч руки на ширине плеч",
			expected: "ноги на ширине плеч руки на ширине плеч",
		},
		{
			name:     "restart too far away",
			input:    "сегодня мы… || || || || || || || || || || сегодня мы разберём присед",
			expected: "сегодня мы сегодня мы разберём присед",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src := speech(tt.input)
			plan := BuildPlan(src, src.Duration, testSettings())

			assert.Equal(t, tt.expected, keptText(src, plan.Keep))
			require.Len(t, plan.Takes, len(tt.expectedKinds))

			for i, kind := range tt.expectedKinds {
				assert.Equal(t, kind, plan.Takes[i].Kind)
				assert.NotEmpty(t, plan.Takes[i].Explanation)
			}

			reasons := make([]CandidateReason, 0, len(plan.Candidates))
			for _, c := range plan.Candidates {
				reasons = append(reasons, c.Reason)
				assert.NotEmpty(t, c.Explanation)
			}

			assert.Equal(t, append([]CandidateReason{}, tt.expectedCandidates...), reasons)
		})
	}
}

func TestBuildPlanRetakeSimilarityThreshold(t *testing.T) {
	t.Parallel()

	const input = "сегодня мы сделаем трен… | сегодня мы проведём тренировку на ноги."

	tests := []struct {
		name       string
		input      float64
		expected   int
		similarity float64
	}{
		{name: "default threshold removes the broken take", input: 0.7, expected: 1, similarity: 0.75},
		{name: "strict threshold keeps both", input: 0.9, expected: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			settings := testSettings()
			settings.RetakeSimilarity = tt.input
			src := speech(input)

			plan := BuildPlan(src, src.Duration, settings)

			require.Len(t, plan.Takes, tt.expected)

			if tt.expected > 0 {
				assert.InDelta(t, tt.similarity, plan.Takes[0].Similarity, 0.001)
			}
		})
	}
}

func TestDescribeTakes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:  "broken-off take",
			input: "давайте при… | давайте приступим к разминке.",
			expected: []string{
				"Дубль 1 (0:00.5–0:01.4) оборван на «давайте при…», " +
					"оставлен дубль 2 (0:02.0–0:03.7) «давайте приступим к разминке».",
			},
		},
		{
			name:  "series points to the last take",
			input: "давайте при… | давайте присту… | давайте приступим.",
			expected: []string{
				"Дубль 1 (0:00.5–0:01.4) оборван на «давайте при…», оставлен дубль 3 (0:03.4–0:04.3) «давайте приступим».",
				"Дубль 2 (0:02.0–0:02.8) оборван на «давайте присту…», оставлен дубль 3 (0:03.4–0:04.3) «давайте приступим».",
			},
		},
		{
			name:  "repeated take",
			input: "сегодня мы… | сегодня мы разберём присед.",
			expected: []string{
				"Дубль 1 (0:00.5–0:01.4) «сегодня мы…» начат заново (повторено 100% слов), " +
					"оставлен дубль 2 (0:02.0–0:03.7) «сегодня мы разберём присед».",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			src := speech(tt.input)
			plan := BuildPlan(src, src.Duration, testSettings())

			explanations := make([]string, 0, len(plan.Takes))
			for _, take := range plan.Takes {
				explanations = append(explanations, take.Explanation)
			}

			assert.Equal(t, tt.expected, explanations)
		})
	}
}

func TestBuildPlanCandidates(t *testing.T) {
	t.Parallel()

	src := speech("держим спину ровно. | держим спину ровно. || делаем по десять приседаний. | делаем по десять выпадов.")
	plan := BuildPlan(src, src.Duration, testSettings())

	require.Len(t, plan.Candidates, 2)

	first := plan.Candidates[0]
	assert.Equal(t, 0, first.ID)
	assert.Equal(t, "держим спину ровно.", first.Earlier.Text, "the text keeps its punctuation")
	assert.Equal(t, WordRange{From: 0, To: 2}, first.EarlierWords)
	// The cut runs from the start of the clip to the margin before the repeat.
	assert.Equal(t, Interval{Start: 0, End: roundTime(src.Words[3].Start - testSettings().PadBefore)}, first.Cut)

	tests := []struct {
		name               string
		input              []int
		expected           string
		expectedCandidates []int
	}{
		{
			name:               "nothing accepted",
			expected:           "держим спину ровно держим спину ровно делаем по десять приседаний делаем по десять выпадов",
			expectedCandidates: []int{0, 1},
		},
		{
			name:               "the first repeat is cut",
			input:              []int{0},
			expected:           "держим спину ровно делаем по десять приседаний делаем по десять выпадов",
			expectedCandidates: []int{1},
		},
		{
			name:               "both are cut",
			input:              []int{1, 0},
			expected:           "держим спину ровно делаем по десять выпадов",
			expectedCandidates: []int{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := BuildPlanAccepting(src, src.Duration, testSettings(), tt.input)
			require.NoError(t, err)

			assert.Equal(t, tt.expected, keptText(src, got.Keep))

			ids := make([]int, 0, len(got.Candidates))
			for _, c := range got.Candidates {
				ids = append(ids, c.ID)
			}

			assert.Equal(t, tt.expectedCandidates, ids)
			assert.Len(t, got.Takes, len(tt.input))

			for _, take := range got.Takes {
				assert.Equal(t, AcceptedTakeKind, take.Kind)
				assert.Contains(t, take.Explanation, "по решению монтажёра")
			}
		})
	}

	_, err := BuildPlanAccepting(src, src.Duration, testSettings(), []int{2})
	require.ErrorIs(t, err, ErrInvalidPlan)
}
