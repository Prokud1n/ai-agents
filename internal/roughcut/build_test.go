package roughcut

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const timeEpsilon = 1e-6

func words(ws ...transcription.Word) *transcription.Result {
	return &transcription.Result{Words: ws}
}

func word(text string, start, end float64) transcription.Word {
	return transcription.Word{Word: text, Start: start, End: end}
}

func TestBuildPlanIntervals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		input        *transcription.Result
		duration     float64
		expectedKeep []Interval
		expectedCuts []Cut
	}{
		{
			name:         "short pauses stay, long ones are cut down to the margins",
			input:        words(word("раз", 1, 1.5), word("два", 1.8, 2.3), word("три", 4, 4.5)),
			duration:     6,
			expectedKeep: []Interval{{Start: 0.92, End: 2.42}, {Start: 3.92, End: 4.62}},
			expectedCuts: []Cut{
				{Start: 0, End: 0.92, Reason: PauseCutReason},
				{Start: 2.42, End: 3.92, Reason: PauseCutReason},
				{Start: 4.62, End: 6, Reason: PauseCutReason},
			},
		},
		{
			name:         "pause exactly at the threshold stays",
			input:        words(word("раз", 0, 1), word("два", 1.4, 2)),
			duration:     2.1,
			expectedKeep: []Interval{{Start: 0, End: 2.1}},
			expectedCuts: []Cut{},
		},
		{
			name:         "filler is cut even inside a short pause",
			input:        words(word("смотрите", 1, 1.6), word("эээ", 1.7, 1.9), word("сюда", 2, 2.5)),
			duration:     2.62,
			expectedKeep: []Interval{{Start: 0.92, End: 1.7}, {Start: 1.92, End: 2.62}},
			expectedCuts: []Cut{
				{Start: 0, End: 0.92, Reason: PauseCutReason},
				{Start: 1.7, End: 1.92, Reason: FillerCutReason, Text: "эээ"},
			},
		},
		{
			name:         "leading filler is cut with the silence before it",
			input:        words(word("Ммм,", 0.2, 0.6), word("привет", 1, 1.5)),
			duration:     1.62,
			expectedKeep: []Interval{{Start: 0.92, End: 1.62}},
			expectedCuts: []Cut{{Start: 0, End: 0.92, Reason: FillerCutReason, Text: "Ммм,"}},
		},
		{
			name:         "trailing filler is cut to the end",
			input:        words(word("всё", 0, 0.5), word("эм", 0.6, 0.9)),
			duration:     3,
			expectedKeep: []Interval{{Start: 0, End: 0.6}},
			expectedCuts: []Cut{{Start: 0.6, End: 3, Reason: FillerCutReason, Text: "эм"}},
		},
		{
			name:         "margins never reach outside the source",
			input:        words(word("да", 0.02, 0.5)),
			duration:     0.55,
			expectedKeep: []Interval{{Start: 0, End: 0.55}},
			expectedCuts: []Cut{},
		},
		{
			name:         "no words keeps nothing",
			input:        words(word("э", 0.5, 1)),
			duration:     2,
			expectedKeep: []Interval{},
			expectedCuts: []Cut{{Start: 0, End: 2, Reason: FillerCutReason, Text: testFiller}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plan := BuildPlan(tt.input, tt.duration, testSettings())

			assert.Equal(t, tt.expectedKeep, nonNil(plan.Keep))
			assert.Equal(t, tt.expectedCuts, nonNilCuts(plan.Cuts))
			assert.InDelta(t, totalDuration(tt.expectedKeep), plan.Duration, timeEpsilon)
			assert.InDelta(t, tt.duration, plan.SourceDuration, timeEpsilon)
		})
	}
}

func nonNil(keep []Interval) []Interval {
	if keep == nil {
		return []Interval{}
	}

	return keep
}

func nonNilCuts(cuts []Cut) []Cut {
	if cuts == nil {
		return []Cut{}
	}

	return cuts
}

// The acceptance criterion: no pause between kept words is longer than MaxPause after the cut.
func TestBuildPlanNoLongPauses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		maxPause float64
	}{
		{name: "default threshold", input: "раз два | три || четыре | | пять эээ шесть || семь", maxPause: 0.4},
		{name: "threshold below the margins", input: "раз | два || три четыре | пять", maxPause: 0.1},
		{name: "retakes and fillers", input: "давайте при | давайте приступим || э | к разминке", maxPause: 0.4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			settings := testSettings()
			settings.MaxPause = tt.maxPause
			src := speech(tt.input)

			plan := BuildPlan(src, src.Duration, settings)
			out := RemapTranscript(src, plan.Keep)

			require.NotEmpty(t, out.Words)
			assert.LessOrEqual(t, out.Words[0].Start, tt.maxPause+timeEpsilon)
			assert.LessOrEqual(t, out.Duration-out.Words[len(out.Words)-1].End, tt.maxPause+timeEpsilon)

			for i := 1; i < len(out.Words); i++ {
				gap := out.Words[i].Start - out.Words[i-1].End
				assert.LessOrEqual(t, gap, tt.maxPause+timeEpsilon, "gap before %q", out.Words[i].Word)
			}

			assert.LessOrEqual(t, plan.Settings.PadBefore+plan.Settings.PadAfter, tt.maxPause+timeEpsilon)
		})
	}
}

func TestBuildPlanDoesNotChangeInput(t *testing.T) {
	t.Parallel()

	src := speech("давайте при | давайте приступим э к разминке")
	before := *src
	before.Words = append([]transcription.Word(nil), src.Words...)
	settings := testSettings()

	BuildPlan(src, src.Duration, settings)

	assert.Equal(t, before.Words, src.Words)
	assert.Equal(t, testSettings(), settings)
}

func TestEffectiveSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		input          Settings
		expectedBefore float64
		expectedAfter  float64
	}{
		{name: "margins fit", input: Settings{MaxPause: 0.4, PadBefore: 0.08, PadAfter: 0.12}, expectedBefore: 0.08, expectedAfter: 0.12},
		{name: "margins scaled down", input: Settings{MaxPause: 0.1, PadBefore: 0.08, PadAfter: 0.12}, expectedBefore: 0.04, expectedAfter: 0.06},
		{name: "negative margins", input: Settings{MaxPause: 0.4, PadBefore: -1, PadAfter: -1}, expectedBefore: 0, expectedAfter: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := effectiveSettings(tt.input)

			assert.InDelta(t, tt.expectedBefore, got.PadBefore, timeEpsilon)
			assert.InDelta(t, tt.expectedAfter, got.PadAfter, timeEpsilon)
		})
	}
}

func TestBuildPlanDurationFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *transcription.Result
		expected float64
	}{
		{name: "transcript duration", input: &transcription.Result{Duration: 5, Words: []transcription.Word{word("да", 1, 2)}}, expected: 5},
		{name: "after the last word", input: words(word("да", 1, 2)), expected: 2.12},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.InDelta(t, tt.expected, BuildPlan(tt.input, 0, testSettings()).SourceDuration, timeEpsilon)
		})
	}
}
