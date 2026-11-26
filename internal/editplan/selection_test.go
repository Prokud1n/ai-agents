package editplan

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// selectionSpeech is a talking head of six phrases, 12.15 s after the cleaning (every phrase is its words plus the
// margins: 2.4 + 2.4 + 1.95 + 1.5 + 1.95 + 1.95 s):
//
//	0–4 привет сегодня расскажу про планку | 5–9 планка укрепляет спину и пресс | 10–13 держите локти под плечами |
//	14–16 не прогибайте поясницу | 17–20 начните с тридцати секунд | 21–24 сохраняйте и пробуйте завтра
const selectionSpeech = "привет сегодня расскажу про планку. | планка укрепляет спину и пресс. | " +
	"держите локти под плечами. | не прогибайте поясницу. | начните с тридцати секунд. | " +
	"сохраняйте и пробуйте завтра."

// selectionTarget: the recommended reel is 4–8 s, so the 12 s of selectionSpeech are shortened.
const selectionTarget = 8

// selectionRequest is selectionSpeech with its automatic plan and the target; version 2 revises it.
func selectionRequest(t *testing.T, version int) *Request {
	t.Helper()

	src := speech(selectionSpeech)

	plan, err := roughcut.BuildPlanEdited(src, src.Duration, testSettings(), &roughcut.Edit{})
	require.NoError(t, err)
	require.InDelta(t, 12.15, plan.Duration, 0.01, "the phrases with their margins")

	return &Request{TaskID: 7, Version: version, Transcript: src, Plan: &plan, TargetSeconds: selectionTarget}
}

var (
	introWords  = roughcut.WordRange{From: 0, To: 4}
	plankWords  = roughcut.WordRange{From: 5, To: 9}
	lowerWords  = roughcut.WordRange{From: 14, To: 16}
	keptEndLine = "Оставил главное: начало «планка укрепляет спину и пресс держите…», финал «…тридцати секунд " +
		"сохраняйте и пробуйте завтра»."
)

func TestRequestSelecting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		change   func(req *Request)
		expected bool
	}{
		{name: "longer than the target", change: func(*Request) {}, expected: true},
		{name: "within the target", change: func(req *Request) { req.TargetSeconds = 20 }},
		{name: "no target", change: func(req *Request) { req.TargetSeconds = 0 }},
		{name: "a revision", change: func(req *Request) { req.Version = 2 }},
		{
			name:   "a workout",
			change: func(req *Request) { req.Plan.Edit = &roughcut.Edit{Mode: roughcut.WorkoutMode} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := selectionRequest(t, 1)
			tt.change(req)

			assert.Equal(t, tt.expected, req.Selecting())
		})
	}
}

// TestSelectionAndCleaning: with the cleanup on (EDITOR_CLEANUP) a first version longer than the target is selected
// (the selection drops the failed takes itself, the task says so), one within it is cleaned; never both.
func TestSelectionAndCleaning(t *testing.T) {
	t.Parallel()

	long := selectionRequest(t, 1)
	long.Cleanup = true
	assert.True(t, long.Selecting())
	assert.False(t, long.Cleaning())
	assert.False(t, long.CleanupOnly())
	assert.Contains(t, Prompt(long), selectionCleaning)
	assert.Contains(t, Prompt(long), "подготовку к съёмке через cleanup_words",
		"the selection drops the failed takes through cleanup_words")
	assert.True(t, long.CleansTakes())

	short := selectionRequest(t, 1)
	short.Cleanup, short.TargetSeconds = true, 20
	assert.False(t, short.Selecting())
	assert.True(t, short.Cleaning())
	assert.True(t, short.CleanupOnly())
}

// TestApplySelection: a first version longer than the target must come out within the recommended range (±15 %); the
// trainer is told how the reel starts and ends and what did not make it, the decisions log has the length and every
// phrase left out.
func TestApplySelection(t *testing.T) {
	t.Parallel()

	req := selectionRequest(t, 1)

	_, err := Apply(req, &Input{Decision: EditDecision, Reason: "всё важно"})

	var check *CheckError
	require.ErrorAs(t, err, &check, "12 s is longer than 4–8 s")
	assert.Contains(t, check.Message, "рилс должен быть 4–8 с (допустимо 3.4–9.2 с)")
	assert.Contains(t, check.Message, "вырежи ещё от", "the seconds to cut")
	assert.Contains(t, check.Message, "ближе всего по длине: слова 0–4 (2,2 с) «привет сегодня расскажу про планку»",
		"no phrase is that long: the closest are named")

	outcome, err := Apply(req, &Input{
		Decision: EditDecision, Reason: "ролик про планку: хук — польза, финал — призыв",
		Remove: []roughcut.WordRange{introWords, lowerWords},
	})
	require.NoError(t, err)

	assert.True(t, outcome.Selected)
	assert.Zero(t, outcome.Target)
	assert.InDelta(t, 8.25, outcome.Duration, 0.01)
	assert.Equal(t, []string{
		keptEndLine, "Не вошло: «привет сегодня расскажу про планку», «не прогибайте поясницу».",
	}, outcome.Changes, "no line for every fragment cut")

	assert.Equal(t, []string{"Отбор под рилс: ролик про планку: хук — польза, финал — призыв"}, outcome.Edit.Notes,
		"the decisions only")
	require.Len(t, outcome.Edit.Details, 3)
	assert.Regexp(t, `^Длина рилс: 0:08\.\d \(рекомендуется 4–8 с\)$`, outcome.Edit.Details[0])
	assert.Equal(t, []string{
		"Не вошло в рилс: слова 0–4 «привет сегодня расскажу про планку»",
		"Не вошло в рилс: слова 14–16 «не прогибайте поясницу»",
	}, outcome.Edit.Details[1:])

	workout, err := Apply(req, &Input{Decision: EditDecision, Reason: "тренировка", Mode: roughcut.WorkoutMode})
	require.NoError(t, err, "a workout is not shortened")
	assert.False(t, workout.Selected)
}

// TestApplySelectionWithCleanupWords: the selection of a first version cuts the failed takes through cleanup_words
// (with the cleanup on or off): they count in the length of the reel and get the line about the cleaning, without the
// floor of a cleaning; a revision still refuses them.
func TestApplySelectionWithCleanupWords(t *testing.T) {
	t.Parallel()

	for _, cleanup := range []bool{true, false} {
		req := selectionRequest(t, 1)
		req.Cleanup = cleanup

		_, err := Apply(req, &Input{
			Decision: EditDecision, Reason: "дубль вступления", CleanupWords: []roughcut.WordRange{introWords},
		})

		var check *CheckError
		require.ErrorAs(t, err, &check, "9.75 s with the take cut is still longer than 9.2 s")
		assert.Contains(t, check.Message, "рилс должен быть 4–8 с", "the length counts the take")

		outcome, err := Apply(req, &Input{
			Decision: EditDecision, Reason: "дубль вступления и лишняя фраза",
			Remove: []roughcut.WordRange{lowerWords}, CleanupWords: []roughcut.WordRange{introWords},
		})
		require.NoError(t, err)

		assert.True(t, outcome.Selected)
		assert.InDelta(t, 8.25, outcome.Duration, 0.01, "the take counts in the length")
		assert.Equal(t, 1, outcome.Cleaned)
		require.Len(t, outcome.Changes, 3)
		assert.Equal(t, keptEndLine, outcome.Changes[0])
		assert.Equal(t, "Не вошло: «не прогибайте поясницу».", outcome.Changes[1], "the take is not «не вошло»")
		assert.Equal(t, CleanupLinePrefix+": 1 фрагмент (2,4 с).", outcome.Changes[2])
		assert.Equal(t, []string{"Отбор под рилс: дубль вступления и лишняя фраза"}, outcome.Edit.Notes)
		assert.Contains(t, outcome.Edit.Details, "Чистка: вырезаны слова 0–4 «привет сегодня расскажу про планку»")
	}

	revision := selectionRequest(t, 2)
	revision.Cleanup = true

	_, err := Apply(revision, &Input{
		Decision: EditDecision, Reason: "дубль", CleanupWords: []roughcut.WordRange{introWords},
	})

	var check *CheckError
	require.ErrorAs(t, err, &check)
	assert.Contains(t, check.Message, "cleanup_words — только")
}

// TestApplyTargetLength: a length the trainer asked for («сделай 6 секунд») is checked within ±15 % in a revision too.
func TestApplyTargetLength(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    Input
		expected string
	}{
		{
			name: "too long for the length asked",
			input: Input{
				Decision: EditDecision, TargetSeconds: 6, Remove: []roughcut.WordRange{introWords, plankWords},
			},
			expected: "длина по просьбе тренера — 6 с (допустимо 5.1–6.9 с)",
		},
		{
			name: "too short for the length asked",
			input: Input{
				Decision: EditDecision, TargetSeconds: 6,
				Remove: []roughcut.WordRange{introWords, plankWords, lowerWords, {From: 10, To: 13}},
			},
			expected: "ролик короче нужного: вырежи на ",
		},
		{
			name: "too short: the ranges cut of that length",
			input: Input{
				Decision: EditDecision, TargetSeconds: 6,
				Remove: []roughcut.WordRange{introWords, plankWords, lowerWords, {From: 10, To: 13}},
			},
			expected: "подходят по длине вырезанные тобой отрезки: слова 0–4 (2,2 с)",
		},
		{
			name:     "shorter than a reel",
			input:    Input{Decision: EditDecision, TargetSeconds: 3, Remove: []roughcut.WordRange{introWords}},
			expected: "target_seconds — длина рилс в секундах от 5",
		},
		{
			name:     "longer than the source",
			input:    Input{Decision: EditDecision, TargetSeconds: 600, Remove: []roughcut.WordRange{introWords}},
			expected: "target_seconds — длина рилс",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Apply(selectionRequest(t, 2), &tt.input)

			var check *CheckError
			require.ErrorAs(t, err, &check)
			assert.Contains(t, check.Message, tt.expected)
		})
	}

	outcome, err := Apply(selectionRequest(t, 2), &Input{
		Decision: EditDecision, Reason: "сделай 6 секунд", TargetSeconds: 6,
		Remove: []roughcut.WordRange{introWords, plankWords, lowerWords},
	})
	require.NoError(t, err)
	assert.True(t, outcome.Selected)
	assert.InDelta(t, 6, outcome.Target, 1e-9)
	assert.InDelta(t, 5.85, outcome.Duration, 0.01)
	assert.Contains(t, outcome.Changes, "Не вошло: «привет сегодня расскажу про планку», «планка укрепляет спину и "+
		"пресс», «не прогибайте поясницу».")
	assert.Contains(t, outcome.Edit.Notes, "Правка тренера к версии 1: сделай 6 секунд")
	assert.Contains(t, outcome.Changes, "Оставил главное: «держите локти под плечами начните с тридцати секунд "+
		"сохраняйте и пробуйте завтра».", "12 words: one quote, not the start and the end")
}

// TestApplyRevisionWithoutLength: a revision that names no length is not checked against the recommended range even
// with a target, and it is not a selection.
func TestApplyRevisionWithoutLength(t *testing.T) {
	t.Parallel()

	outcome, err := Apply(selectionRequest(t, 2), &Input{
		Decision: EditDecision, Reason: "убери вступление", Remove: []roughcut.WordRange{introWords},
	})
	require.NoError(t, err, "9.75 s is above 4–8 s, but nobody asked for a length")

	assert.False(t, outcome.Selected)
	assert.InDelta(t, 9.75, outcome.Duration, 0.01)
	assert.Equal(t, []string{"Вырезал фрагмент «привет сегодня расскажу про планку»."}, outcome.Changes)
	assert.Empty(t, outcome.Edit.Details)
}

// TestApplyKeepWhole: «не сокращай» on a long talking head is target_seconds of the current length: it passes the
// check, and a reel that did not get shorter is not «сократил под рилс».
func TestApplyKeepWhole(t *testing.T) {
	t.Parallel()

	req := selectionRequest(t, 1)
	req.Caption = "не сокращай"

	outcome, err := Apply(req, &Input{Decision: EditDecision, Reason: "тренер просит целиком", TargetSeconds: 12.1})
	require.NoError(t, err)

	assert.False(t, outcome.Selected)
	assert.InDelta(t, req.Plan.Duration, outcome.Duration, 1e-9)
	assert.Empty(t, outcome.Changes)
	assert.Empty(t, outcome.Edit.Details)
	assert.Equal(t, []string{"Подпись тренера к исходнику: тренер просит целиком"}, outcome.Edit.Notes)
}

// TestApplyFixWordChecksIndex: the word the model read at the index must be there (any case, punctuation around it),
// so a word counted wrong inside a phrase goes back to the model.
func TestApplyFixWordChecksIndex(t *testing.T) {
	t.Parallel()

	_, err := Apply(testRequest(2, nil), &Input{
		Decision: EditDecision, FixWords: []FixInput{{Index: 8, Word: "десять", Text: "10"}},
	})

	var check *CheckError
	require.ErrorAs(t, err, &check)
	assert.Contains(t, check.Message, "слово 8 в расшифровке — «по», а не «десять»")

	outcome, err := Apply(testRequest(2, nil), &Input{
		Decision: EditDecision, FixWords: []FixInput{{Index: 9, Word: "Десять,", Text: "10"}},
	})
	require.NoError(t, err)
	assert.Equal(t, []roughcut.WordFix{{Index: 9, Text: "10"}}, outcome.Edit.Words)
}

// TestApplyResolvedRepeatNotLogged: a disputed repeat whose phrase the decision cut is not «решения нет».
func TestApplyResolvedRepeatNotLogged(t *testing.T) {
	t.Parallel()

	outcome, err := Apply(testRequest(1, nil), &Input{
		Decision: EditDecision, Reason: "без первой фразы", Remove: []roughcut.WordRange{{From: 0, To: 2}},
	})
	require.NoError(t, err)
	assert.Empty(t, outcome.Edit.Details)
}

// TestApplyNotesWithoutCaption: a first version without a caption logs the model's reason and the repeats it left
// undecided, so the decisions log does not say «Решения монтажёра: 0».
func TestApplyNotesWithoutCaption(t *testing.T) {
	t.Parallel()

	outcome, err := Apply(testRequest(1, nil), &Input{Decision: EditDecision, Reason: "повтор — счёт"})
	require.NoError(t, err)

	assert.False(t, outcome.Selected, "no target")
	assert.Equal(t, []string{"Решение по черновой склейке: повтор — счёт"}, outcome.Edit.Notes)
	assert.Equal(t, []string{"Повтор 0: решения нет — оставлены обе фразы"}, outcome.Edit.Details,
		"both phrases of the repeat stay in the reel")
}
