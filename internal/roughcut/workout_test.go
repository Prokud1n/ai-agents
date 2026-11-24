package roughcut

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildPlanWorkout: a workout keeps the whole source — pauses, the filler and the failed take stay — and cuts only
// what the editor chose: words, fragments by time, accepted candidates. The candidates are the same as in a talk plan.
//
// editSpeech: держим 0.5–0.9, спину 0.95–1.35, ровно 1.4–1.8 | держим 2.4–2.8 … ровно 3.3–3.7 || эээ 5.7–6.1,
// делаем 6.15–6.55, по 6.6–7.0, десять 7.05–7.45, приседаний 7.5–7.9; the source lasts 8.95 s.
func TestBuildPlanWorkout(t *testing.T) {
	t.Parallel()

	src := speech(editSpeech)

	tests := []struct {
		name     string
		edit     Edit
		keep     []Interval
		cuts     []Cut
		takes    int
		expected string
	}{
		{
			name:     "nothing cut: pauses, the filler and the repeat stay",
			edit:     Edit{Mode: WorkoutMode},
			keep:     []Interval{{Start: 0, End: 8.95}},
			cuts:     []Cut{},
			expected: "держим спину ровно держим спину ровно эээ делаем по десять приседаний",
		},
		{
			name:     "a removed word: with its margins, up to the kept neighbours",
			edit:     Edit{Mode: WorkoutMode, Remove: []WordRange{{From: 6, To: 6}}},
			keep:     []Interval{{Start: 0, End: 5.62}, {Start: 6.15, End: 8.95}},
			cuts:     []Cut{{Start: 5.62, End: 6.15, Reason: EditCutReason, Text: "эээ"}},
			expected: "держим спину ровно держим спину ровно делаем по десять приседаний",
		},
		{
			name: "cuts by time: the start and past the end",
			edit: Edit{Mode: WorkoutMode, CutTime: []Interval{{Start: 7.95, End: 9.5}, {Start: 0, End: 0.3}}},
			keep: []Interval{{Start: 0.3, End: 7.95}},
			cuts: []Cut{
				{Start: 0, End: 0.3, Reason: EditCutReason},
				{Start: 7.95, End: 8.95, Reason: EditCutReason},
			},
			expected: "держим спину ровно держим спину ровно эээ делаем по десять приседаний",
		},
		{
			name:     "an accepted repeat is cut as a take",
			edit:     Edit{Mode: WorkoutMode, Accept: []int{0}},
			keep:     []Interval{{Start: 0, End: 0.42}, {Start: 1.92, End: 8.95}},
			cuts:     []Cut{{Start: 0.42, End: 1.92, Reason: RetakeCutReason, Text: "держим спину ровно"}},
			takes:    1,
			expected: "держим спину ровно эээ делаем по десять приседаний",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plan, err := BuildPlanEdited(src, src.Duration, testSettings(), &tt.edit)
			require.NoError(t, err)

			assert.Equal(t, WorkoutMode, plan.Mode)
			assert.Equal(t, tt.keep, plan.Keep)
			assert.Equal(t, tt.cuts, plan.Cuts)
			assert.Len(t, plan.Takes, tt.takes)
			assert.Equal(t, tt.expected, keptText(src, plan.Keep))

			if len(tt.edit.Accept) == 0 {
				require.Len(t, plan.Candidates, 1, "the candidates are found as in a talk plan")
				assert.Equal(t, 0, plan.Candidates[0].ID)
			}
		})
	}
}

// TestBuildPlanWorkoutWithoutWords: a workout shown in silence is kept whole; a talk plan of it keeps nothing.
func TestBuildPlanWorkoutWithoutWords(t *testing.T) {
	t.Parallel()

	src := speech("")

	workout, err := BuildPlanEdited(src, 30, testSettings(), &Edit{Mode: WorkoutMode})
	require.NoError(t, err)
	assert.Equal(t, []Interval{{Start: 0, End: 30}}, workout.Keep)
	assert.InDelta(t, 30, workout.Duration, 1e-9)

	talk, err := BuildPlanEdited(src, 30, testSettings(), &Edit{})
	require.NoError(t, err)
	assert.Empty(t, talk.Keep)
}

// TestBuildPlanTalkCutTime: a cut by time in a talking-head plan removes what was kept there and is logged as an edit.
func TestBuildPlanTalkCutTime(t *testing.T) {
	t.Parallel()

	src := speech(editSpeech)

	plan, err := BuildPlanEdited(src, src.Duration, testSettings(), &Edit{CutTime: []Interval{{Start: 6, End: 7}}})
	require.NoError(t, err)

	assert.Empty(t, plan.Mode, "a talk plan without a mode stays as it was")
	assert.Equal(t, "держим спину ровно держим спину ровно десять приседаний", keptText(src, plan.Keep))

	var edited []Cut

	for _, c := range plan.Cuts {
		if c.Reason == EditCutReason {
			edited = append(edited, c)
		}
	}

	assert.Equal(t, []Cut{{Start: 6.1, End: 7, Reason: EditCutReason, Text: "делаем по"}}, edited)
}

func TestRemapTitles(t *testing.T) {
	t.Parallel()

	keep := []Interval{{Start: 0, End: 2}, {Start: 4, End: 6}} // the reel is 4 s

	tests := []struct {
		name     string
		input    []Title
		expected []Title
	}{
		{
			name:  "until the next title, the last to the end; ordered by time",
			input: []Title{{Text: "Б", Start: 4.5}, {Text: "А", Start: 0.5}},
			expected: []Title{
				{Text: "А", Start: 0.5, End: 2.5}, // the cut 2–4 is skipped
				{Text: "Б", Start: 2.5, End: 4},
			},
		},
		{
			name:     "an explicit end",
			input:    []Title{{Text: "А", Start: 0, End: 1}},
			expected: []Title{{Text: "А", Start: 0, End: 1}},
		},
		{
			name:     "a title that starts in a cut starts where the reel goes on",
			input:    []Title{{Text: "А", Start: 3, End: 5}},
			expected: []Title{{Text: "А", Start: 2, End: 3}},
		},
		{
			name:     "a title whose time is cut is not shown",
			input:    []Title{{Text: "А", Start: 2.5, End: 3.5}},
			expected: []Title{},
		},
		{name: "no titles", expected: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, RemapTitles(tt.input, keep))
		})
	}
}

func TestTimelineMapping(t *testing.T) {
	t.Parallel()

	keep := []Interval{{Start: 1, End: 3}, {Start: 5, End: 6}}

	assert.InDelta(t, 5.5, SourceTime(keep, 2.5), 1e-9, "the second kept interval")
	assert.InDelta(t, 6, SourceTime(keep, 10), 1e-9, "past the end of the reel")
	assert.InDelta(t, 7, SourceTime(nil, 7), 1e-9)
	assert.InDelta(t, 2, ReelTime(keep, 4), 1e-9, "a cut moment is where the reel goes on")
	assert.InDelta(t, 1.5, ReelTime(keep, 2.5), 1e-9)
	assert.Equal(t, []Interval{{Start: 2, End: 3}, {Start: 5, End: 5.5}}, SourceIntervals(keep, 1, 2.5))
	assert.Equal(t, []Interval{{Start: 0, End: 1}, {Start: 2, End: 3}}, NormalizeIntervals(
		[]Interval{{Start: 2.5, End: 3}, {Start: -1, End: 1}, {Start: 2, End: 2.6}, {Start: 4, End: 5}}, 3))
}

func TestDecisionsLogOfWorkout(t *testing.T) {
	t.Parallel()

	src := speech(editSpeech)
	edit := &Edit{
		Mode: WorkoutMode, Mute: true,
		Titles: []Title{{Text: "Приседания — 10 раз", Start: 6.15}, {Text: "Спина", Start: 0.5, End: 3}},
	}

	plan, err := BuildPlanEdited(src, src.Duration, testSettings(), edit)
	require.NoError(t, err)

	plan.Edit = edit
	log := DecisionsLog(&plan)

	assert.Contains(t, log, "Режим: тренировка")
	assert.Contains(t, log, "Звук исходника убран")
	assert.Contains(t, log, "- с 0:06.2 до следующей подписи «Приседания — 10 раз»")
	assert.Contains(t, log, "- с 0:00.5 до 0:03.0 «Спина»")
}
