package editor

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

func TestDescribe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		result   editjob.Result
		version  int
		caption  string
		summary  *editjob.Summary
		expected string
	}{
		{
			name:    "cut with the trainer's caption",
			result:  editjob.Result{SourceDuration: 182.4, Duration: 141.2},
			version: 1,
			caption: " про планку ",
			expected: "Черновик рилс готов: было 3:02, стало 2:21 — вырезал 0:41 (паузы, оговорки и неудачные дубли).\n" +
				"Ваша подпись к исходнику: «про планку»",
		},
		{
			name:    "repeats left in without a decision",
			result:  editjob.Result{SourceDuration: 60, Duration: 45, Candidates: 2},
			version: 1,
			expected: "Черновик рилс готов: было 1:00, стало 0:45 — вырезал 0:15 (паузы, оговорки и неудачные дубли).\n" +
				"Спорные повторы оставил в ролике: 2.",
		},
		{
			name:    "the caption applied and the repeats decided",
			result:  editjob.Result{SourceDuration: 60, Duration: 40, Candidates: 1},
			version: 1,
			caption: "без первой фразы",
			summary: &editjob.Summary{Changes: []string{"Вырезал фрагмент «всем привет».", "Спорные повторы: вырезал 1, оставил 1."}},
			expected: "Черновик рилс готов: было 1:00, стало 0:40 — вырезал 0:20 (паузы, оговорки и неудачные дубли).\n" +
				"Вырезал фрагмент «всем привет».\nСпорные повторы: вырезал 1, оставил 1.\n" +
				"Ваша подпись к исходнику: «без первой фразы»",
		},
		{
			name:    "the caption is not applied",
			result:  editjob.Result{SourceDuration: 60, Duration: 45},
			version: 1,
			caption: "добавь музыку",
			summary: &editjob.Summary{Note: captionCannotNote},
			expected: "Черновик рилс готов: было 1:00, стало 0:45 — вырезал 0:15 (паузы, оговорки и неудачные дубли).\n" +
				"Ваша подпись к исходнику: «добавь музыку»\n" + captionCannotNote,
		},
		{
			name:     "nothing to cut",
			result:   editjob.Result{SourceDuration: 30.2, Duration: 30},
			version:  1,
			expected: "Черновик рилс готов: 0:30, вырезать было почти нечего.",
		},
		{
			name:     "a revision says what changed",
			result:   editjob.Result{SourceDuration: 60, Duration: 42},
			version:  2,
			caption:  "про планку",
			summary:  &editjob.Summary{Changes: []string{testChange}, PreviousDuration: 45},
			expected: "Новая версия рилс: было 0:45, стало 0:42.\n" + testChange,
		},
		{
			name:     "a revision without its summary",
			result:   editjob.Result{SourceDuration: 60, Duration: 42},
			version:  3,
			expected: "Новая версия рилс: 0:42.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, Describe(&tt.result, tt.version, tt.caption, tt.summary))
		})
	}
}

func TestDescribeTruncatesLongCaption(t *testing.T) {
	t.Parallel()

	got := Describe(&editjob.Result{SourceDuration: 60, Duration: 50}, 1, strings.Repeat("а", maxCaptionRunes*2), nil)

	assert.Less(t, len([]rune(got)), 1024, "fits a Telegram video caption")
	assert.True(t, strings.HasSuffix(got, "…»"))
}

func TestFirstSummary(t *testing.T) {
	t.Parallel()

	twoCandidates := &roughcut.Plan{Candidates: []roughcut.Candidate{{ID: 0}, {ID: 1}}}

	tests := []struct {
		name     string
		caption  string
		plan     *roughcut.Plan
		outcome  editplan.Outcome
		expected editjob.Summary
	}{
		{
			name: "repeats decided", plan: twoCandidates,
			outcome:  editplan.Outcome{Kind: editplan.EditOutcome, Cut: 1, Kept: 1},
			expected: editjob.Summary{Changes: []string{"Спорные повторы: вырезал 1, оставил 1."}},
		},
		{
			name: "the caption applied, no repeats", caption: "без начала", plan: &roughcut.Plan{},
			outcome:  editplan.Outcome{Kind: editplan.EditOutcome, Changes: []string{"Обрезал начало: убрал первые 2 с."}},
			expected: editjob.Summary{Changes: []string{"Обрезал начало: убрал первые 2 с."}},
		},
		{
			name: "the caption cannot be applied", caption: "добавь музыку", plan: twoCandidates,
			outcome: editplan.Outcome{Kind: editplan.CannotOutcome},
			expected: editjob.Summary{
				Changes: []string{"Спорные повторы оставил в ролике: 2."}, Note: captionCannotNote,
			},
		},
		{
			name: "the caption not understood", caption: "сделай красиво", plan: &roughcut.Plan{},
			outcome:  editplan.Outcome{Kind: editplan.UnclearOutcome},
			expected: editjob.Summary{Note: captionUnclearNote},
		},
		{
			name: "the model did not decide the repeats", plan: twoCandidates,
			outcome:  editplan.Outcome{Kind: editplan.NoAnswerOutcome},
			expected: editjob.Summary{Changes: []string{"Спорные повторы оставил в ролике: 2."}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := &editplan.Request{Version: 1, Caption: tt.caption, Plan: tt.plan}

			assert.Equal(t, &tt.expected, firstSummary(req, &tt.outcome))
		})
	}
}

func TestRevisionTexts(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		cannotRevisionText, unclearRevisionText, modelFailedText, artifactsMissingText, captionCannotNote,
		captionUnclearNote,
	} {
		assert.NotContains(t, text, "S3")
		assert.NotContains(t, text, "Claude")
		assert.NotContains(t, text, "модел")
		assert.NotContains(t, text, "Whisper")
	}

	assert.Equal(t, "Такое я пока не умею: могу обрезать, убрать или вернуть фрагмент, выбрать дубль и поправить "+
		"слово в субтитрах. Напишите правку по-другому — сделаю новую версию.", cannotRevisionText)
	assert.Equal(t, "Не понял, что именно поменять. Напишите, например: „убери первые 3 секунды“ или "+
		"„оставь второй дубль“.", unclearRevisionText)
}

func TestFailureText(t *testing.T) {
	t.Parallel()

	e := newTestExecutor(&fakeBoard{})

	for _, code := range []editjob.FailureCode{
		editjob.SourceMissingCode, editjob.SourceTooLargeCode, editjob.SourceTooLongCode, editjob.BadSourceCode,
		editjob.NoAudioCode, editjob.NoSpeechCode, editjob.TimeoutCode, editjob.QueueTimeoutCode,
	} {
		text := e.failureText(&editjob.Result{Code: code, SourceDuration: 700, MaxDuration: 600})

		assert.NotEmpty(t, text, code)
		assert.NotContains(t, text, string(code), "no codes for the trainer")
		assert.NotContains(t, text, "OPENAI", "no setup details for the trainer")
	}

	assert.Equal(t, "Исходник длится 11:40, а монтирую я видео до 10 мин. Разрежьте его на части и пришлите по одной.",
		e.failureText(&editjob.Result{Code: editjob.SourceTooLongCode, SourceDuration: 700, MaxDuration: 600}))
	assert.Equal(t, "Монтаж этого видео занял больше 45 мин и был остановлен — скорее всего, оно слишком длинное. "+
		"Разрежьте его на части и пришлите по одной.", runningTimeoutText(testTimeout))
	assert.Equal(t, "Очередь на монтаж была слишком длинной: за 3 ч до этого видео дело не дошло. "+
		"Пришлите исходник ещё раз чуть позже.", queueTimeoutText(testQueue))
	assert.NotContains(t, technicalText, "OPENAI")
}
