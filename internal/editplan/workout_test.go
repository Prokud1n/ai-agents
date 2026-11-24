package editplan

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// testSpeech on the source: 0 держим 0.5 … 5 ровно 3.3–3.7, 6 эээ 5.7–6.1, 7 делаем 6.15, 8 по 6.6, 9 десять 7.05,
// 10 приседаний 7.5–7.9; the source lasts 8.95 s.
// Literals shared by the tests.
const (
	testReason   = "правка"
	noLineBreaks = "без переносов строк"
	titleLength  = "от 1 до 40 символов"
	squatsName   = "Приседания"
	bridgeTitle  = "Мост"
)

const (
	testSourceDuration = 8.95
	squatsTitle        = "Приседания — 10 раз"
)

func workoutEdit() *roughcut.Edit {
	return &roughcut.Edit{Mode: roughcut.WorkoutMode}
}

// TestApplyWorkoutFirstVersion: the mode is chosen or kept, a workout keeps the whole video and is trimmed and cut by
// time, titles start at a word or at a second of the source, the sound can be removed, exercises without a time are
// returned for the description.
func TestApplyWorkoutFirstVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		base     *roughcut.Edit
		input    Input
		edit     roughcut.Edit
		duration float64
		changes  []string
		titles   []string
		untimed  []string
	}{
		{
			name:  "a talk plan becomes a workout with a title at the word",
			input: Input{Mode: roughcut.WorkoutMode, AddTitles: []TitleInput{{Text: " " + squatsTitle + " ", Word: new(7)}}},
			edit: roughcut.Edit{
				Mode: roughcut.WorkoutMode, Titles: []roughcut.Title{{Text: squatsTitle, Start: 6.15}},
			},
			duration: testSourceDuration,
			titles:   []string{"0:06 — «" + squatsTitle + "»"},
		},
		{
			name:     "the mode of the plan is kept; the trim is by time",
			base:     workoutEdit(),
			input:    Input{TrimStart: 2},
			edit:     roughcut.Edit{Mode: roughcut.WorkoutMode, CutTime: []roughcut.Interval{{Start: 0, End: 2}}},
			duration: testSourceDuration - 2,
			changes:  []string{"Обрезал начало: убрал первые 2 с."},
		},
		{
			name:     "a rest cut by seconds, a title by seconds until the end",
			base:     workoutEdit(),
			input:    Input{CutSeconds: []SecondsRange{{From: 3.8, To: 5.6}}, AddTitles: []TitleInput{{Text: bridgeTitle, Start: new(6.0)}}},
			duration: testSourceDuration - 1.8,
			edit: roughcut.Edit{
				Mode: roughcut.WorkoutMode, CutTime: []roughcut.Interval{{Start: 3.8, End: 5.6}},
				Titles: []roughcut.Title{{Text: bridgeTitle, Start: 6}},
			},
			changes: []string{"Вырезал отрезок 0:04–0:06."},
			titles:  []string{"0:04 — «Мост»"},
		},
		{
			name:     "the sound removed, an exercise without a time",
			base:     workoutEdit(),
			input:    Input{Mute: new(true), UntimedTitles: []string{" Планка 30 с "}},
			edit:     roughcut.Edit{Mode: roughcut.WorkoutMode, Mute: true},
			duration: testSourceDuration,
			changes:  []string{muteLine},
			untimed:  []string{"Планка 30 с"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			in := tt.input
			in.Decision, in.Reason = EditDecision, "тренировка"

			outcome, err := Apply(testRequest(1, tt.base), &in)
			require.NoError(t, err)
			require.Equal(t, EditOutcome, outcome.Kind)

			edit := outcome.Edit
			edit.Notes = nil

			assert.Equal(t, tt.edit, edit)
			assert.Equal(t, roughcut.WorkoutMode, outcome.Mode)
			assert.InDelta(t, tt.duration, outcome.Duration, 0.01)
			assert.Equal(t, orNoLines(tt.changes), outcome.Changes)
			assert.Equal(t, orNoLines(tt.titles), outcome.Titles)
			assert.Equal(t, orNoLines(tt.untimed), outcome.Untimed)
		})
	}
}

func orNoLines(lines []string) []string {
	if lines == nil {
		return []string{}
	}

	return lines
}

// TestApplyWorkoutKeepsTalkAsBefore: a talking-head decision without workout fields keeps an edit without them.
func TestApplyWorkoutKeepsTalkAsBefore(t *testing.T) {
	t.Parallel()

	outcome, err := Apply(testRequest(1, nil), &Input{Decision: EditDecision, Reason: "описание"})
	require.NoError(t, err)

	assert.Equal(t, roughcut.TalkMode, outcome.Mode)
	assert.Empty(t, outcome.Edit.Mode)
	assert.Nil(t, outcome.Edit.CutTime)
	assert.Nil(t, outcome.Edit.Titles)
	assert.False(t, outcome.Edit.Mute)
	assert.Empty(t, outcome.Titles)
}

// TestApplyRejectsInvalidWorkoutEdit: titles without a time, with markup or too long, two at once, a title whose
// time is cut, an unknown mode, a reversed cut, unknown numbers go back to the model.
func TestApplyRejectsInvalidWorkoutEdit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    Input
		expected string
	}{
		{name: "an unknown mode", input: Input{Mode: "dance"}, expected: "mode должен быть talk или workout"},
		{name: "a reversed cut", input: Input{CutSeconds: []SecondsRange{{From: 3, To: 2}}}, expected: "вне видео"},
		{name: "a cut past the video", input: Input{CutSeconds: []SecondsRange{{From: 9, To: 10}}}, expected: "вне видео"},
		{name: "an unknown cut to restore", input: Input{RestoreCuts: []int{0}}, expected: "restore_cuts"},
		{name: "an unknown title to remove", input: Input{RemoveTitles: []int{0}}, expected: "remove_titles"},
		{name: "a title without a time", input: Input{AddTitles: []TitleInput{{Text: bridgeTitle}}}, expected: "untimed_titles"},
		{
			name:     "a title with a word and seconds",
			input:    Input{AddTitles: []TitleInput{{Text: bridgeTitle, Word: new(1), Start: new(1.0)}}},
			expected: "или словом",
		},
		{
			name:     "a title with markup",
			input:    Input{AddTitles: []TitleInput{{Text: `Мост {\b1}`, Word: new(1)}}},
			expected: noLineBreaks,
		},
		{
			name:     "a title on two lines",
			input:    Input{AddTitles: []TitleInput{{Text: "Мост\nи планка", Word: new(1)}}},
			expected: noLineBreaks,
		},
		{
			name:     "a title too long",
			input:    Input{AddTitles: []TitleInput{{Text: strings.Repeat("я", maxTitleRunes+1), Word: new(1)}}},
			expected: titleLength,
		},
		{
			name:     "an untimed title with an invisible character",
			input:    Input{UntimedTitles: []string{"Мост\u200b"}},
			expected: noLineBreaks,
		},
		{
			name:     "two titles at once",
			input:    Input{AddTitles: []TitleInput{{Text: bridgeTitle, Word: new(7)}, {Text: "Планка", Start: new(6.3)}}},
			expected: "почти одновременно",
		},
		{
			name:     "a title of a word past the transcript",
			input:    Input{AddTitles: []TitleInput{{Text: bridgeTitle, Word: new(11)}}},
			expected: "индексом 11",
		},
		{
			name: "a title whose time is cut",
			input: Input{
				CutSeconds: []SecondsRange{{From: 5, To: 8.95}}, AddTitles: []TitleInput{{Text: bridgeTitle, Start: new(6.0)}},
			},
			expected: "не видно в ролике",
		},
		{
			name:     "a title that ends before it starts",
			input:    Input{AddTitles: []TitleInput{{Text: bridgeTitle, Start: new(6.0), End: new(5.0)}}},
			expected: "конец раньше начала",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			in := tt.input
			in.Decision, in.Reason = EditDecision, testReason

			_, err := Apply(testRequest(1, workoutEdit()), &in)

			var check *CheckError
			require.ErrorAs(t, err, &check)
			assert.Contains(t, check.Message, tt.expected)
		})
	}
}

// TestApplyWorkoutRevision: a revision counts seconds on the reel the trainer saw (the start of the source is cut),
// keeps the mode, the cuts and the titles of the previous versions and changes them.
func TestApplyWorkoutRevision(t *testing.T) {
	t.Parallel()

	previous := func() *roughcut.Edit {
		return &roughcut.Edit{
			Mode: roughcut.WorkoutMode, CutTime: []roughcut.Interval{{Start: 0, End: 2}},
			Titles: []roughcut.Title{{Text: "Спина", Start: 2.4}, {Text: squatsName, Start: 6.15}},
		}
	}

	tests := []struct {
		name    string
		input   Input
		check   func(t *testing.T, edit *roughcut.Edit)
		changes []string
	}{
		{
			name:  "a title renamed",
			input: Input{ChangeTitles: []TitleChange{{Index: 1, Text: squatsTitle}}},
			check: func(t *testing.T, edit *roughcut.Edit) {
				assert.Equal(t, []roughcut.Title{{Text: "Спина", Start: 2.4}, {Text: squatsTitle, Start: 6.15}}, edit.Titles)
			},
			changes: []string{"Переименовал подпись «Приседания» → «" + squatsTitle + "»."},
		},
		{
			name:  "a title removed and one added at a second of the reel",
			input: Input{RemoveTitles: []int{0}, AddTitles: []TitleInput{{Text: bridgeTitle, Start: new(1.0), End: new(3.0)}}},
			check: func(t *testing.T, edit *roughcut.Edit) {
				assert.Equal(t, []roughcut.Title{{Text: bridgeTitle, Start: 3, End: 5}, {Text: squatsName, Start: 6.15}}, edit.Titles,
					"the reel starts at 2 s of the source")
			},
			changes: []string{"Убрал подпись «Спина».", "Добавил подпись «Мост» с 0:01."},
		},
		{
			name:  "a title moved",
			input: Input{ChangeTitles: []TitleChange{{Index: 1, Start: new(3.5)}}},
			check: func(t *testing.T, edit *roughcut.Edit) {
				assert.InDelta(t, 5.5, edit.Titles[1].Start, 1e-9)
			},
			changes: []string{"Сдвинул подпись «Приседания»: теперь с 0:04."},
		},
		{
			name:  "the cut start brought back",
			input: Input{RestoreCuts: []int{0}},
			check: func(t *testing.T, edit *roughcut.Edit) {
				assert.Nil(t, edit.CutTime)
			},
			changes: []string{"Вернул вырезанный ранее отрезок (2 с)."},
		},
		{
			name:  "a cut of the reel joins the cut start",
			input: Input{CutSeconds: []SecondsRange{{From: 0, To: 1}}},
			check: func(t *testing.T, edit *roughcut.Edit) {
				assert.Equal(t, []roughcut.Interval{{Start: 0, End: 3}}, edit.CutTime)
			},
			changes: []string{"Вырезал отрезок 0:00–0:01."},
		},
		{
			name:  "back to a talking head: the pauses are cut, the rest is kept",
			input: Input{Mode: roughcut.TalkMode},
			check: func(t *testing.T, edit *roughcut.Edit) {
				assert.Empty(t, edit.Mode)
				assert.Equal(t, []roughcut.Interval{{Start: 0, End: 2}}, edit.CutTime)
				assert.Len(t, edit.Titles, 2)
			},
			changes: []string{talkModeLine},
		},
		{
			name:    "the sound removed",
			input:   Input{Mute: new(true)},
			check:   func(t *testing.T, edit *roughcut.Edit) { assert.True(t, edit.Mute) },
			changes: []string{muteLine},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			in := tt.input
			in.Decision, in.Reason = EditDecision, testReason

			outcome, err := Apply(testRequest(2, previous()), &in)
			require.NoError(t, err)
			require.Equal(t, EditOutcome, outcome.Kind)

			tt.check(t, &outcome.Edit)
			assert.Equal(t, tt.changes, outcome.Changes)
		})
	}
}

// TestApplyWorkoutRevisionWithoutChange: keeping the sound as it is changes nothing in the reel.
func TestApplyWorkoutRevisionWithoutChange(t *testing.T) {
	t.Parallel()

	_, err := Apply(testRequest(2, workoutEdit()), &Input{Decision: EditDecision, Mute: new(false)})

	var check *CheckError
	require.ErrorAs(t, err, &check)
	assert.Contains(t, check.Message, "ничего не меняет")
}

// TestApplyNoSpeech: a video without words is decided by seconds only.
func TestApplyNoSpeech(t *testing.T) {
	t.Parallel()

	req := testRequest(1, workoutEdit())
	req.Transcript.Words, req.Transcript.Segments = nil, nil

	outcome, err := Apply(req, &Input{
		Decision: EditDecision, AddTitles: []TitleInput{{Text: "Выпады", Start: new(0.0)}},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"0:00 — «Выпады»"}, outcome.Titles)
	assert.InDelta(t, testSourceDuration, outcome.Duration, 0.01)

	assert.Contains(t, Prompt(req), "Речи в видео нет")
}

// TestApplyRefusesTooManyFragments: an edit the worker would refuse (more fragments than one render takes) goes back
// to the model instead of failing in the worker as invalid_edit.
func TestApplyRefusesTooManyFragments(t *testing.T) {
	t.Parallel()

	const (
		cutLength = 0.005
		cutStep   = 0.017
	)

	previous := workoutEdit()
	for i := range roughcut.MaxSegments + 1 {
		start := 0.1 + cutStep*float64(i)
		previous.CutTime = append(previous.CutTime, roughcut.Interval{Start: start, End: start + cutLength})
	}

	_, err := Apply(testRequest(2, previous), &Input{Decision: EditDecision, Reason: testReason, Mute: new(true)})

	var check *CheckError
	require.ErrorAs(t, err, &check)
	assert.Contains(t, check.Message, "не больше 500")
}
