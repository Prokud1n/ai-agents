package editor

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

// declinedTexts are the prepared texts of all seven kinds the editor declines.
var declinedTexts = []string{
	"Музыку добавьте в Instagram при публикации — я её не накладываю.",
	"Цвет и яркость я не меняю — их можно поправить фильтром в Instagram при публикации.",
	"Эффекты и переходы я не добавляю — склейки остаются простыми.",
	"Вставки из других видео и фото я не добавляю — монтирую только из присланного исходника.",
	"Ускорение и замедление я не делаю — видео идёт с обычной скоростью.",
	"Фильтры красоты и ретушь я не накладываю — их можно добавить в Instagram при публикации.",
	"Переставлять фрагменты местами я пока не умею — порядок остаётся как в исходнике.",
}

// TestExecutorCleanupInAutoJob: with EDITOR_CLEANUP the auto job of a first version without a caption stops after the
// plan for the model.
func TestExecutorCleanupInAutoJob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cleanup  bool
		expected bool
	}{
		{name: "on", cleanup: true, expected: true},
		{name: "off", cleanup: false, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := &fakeBoard{}
			e := newTestExecutor(b)
			e.cleanup = tt.cleanup

			data := editData()
			data.Input = ""

			_, err := e.Execute(t.Context(), data)

			var deferred *tasks.DeferredError
			require.ErrorAs(t, err, &deferred)
			require.Len(t, b.submitted, 1)
			assert.Equal(t, tt.expected, b.submitted[0].Decide)
		})
	}
}

// TestExecutorCleanupInvalid: a cleaning that failed the checks twice renders the automatic cut with a note for the
// trainer and the hook title that passed its checks.
func TestExecutorCleanupInvalid(t *testing.T) {
	t.Parallel()

	const hook = "мой путь в спорт"

	decider := &fakeDecider{outcome: &editplan.Outcome{
		Kind: editplan.InvalidOutcome, Reason: "мало осталось", Edit: roughcut.Edit{Hook: hook},
	}}
	planned := pendingJob(time.Hour)
	planned.Stage, planned.Decide = editjob.AutoStage, true
	b := &fakeBoard{job: planned, result: &editjob.Result{JobID: testJobID, Status: editjob.PlannedStatus}}
	e := newTestExecutorWith(&testDeps{board: b, store: testStore{files: storedShortFiles(t)}, decider: decider,
		tech: &fakeTech{}})
	e.cleanup, e.hookTitle = true, true

	data := editData()
	data.Input = ""

	_, err := e.Execute(t.Context(), data)

	var deferred *tasks.DeferredError
	require.ErrorAs(t, err, &deferred)
	require.Len(t, decider.requests, 1)
	assert.True(t, decider.requests[0].Cleanup)
	assert.True(t, decider.requests[0].CleanupOnly())

	require.Len(t, b.replaced, 1)
	assert.Equal(t, &roughcut.Edit{Hook: hook}, b.replaced[0].Edit, "the automatic cut with the checked hook")
	assert.Equal(t, &editjob.Summary{
		Changes: []string{"Заголовок: «" + hook + "»."},
		Note:    fmt.Sprintf(cleanupInvalidNote, cutWithoutTakes),
	}, b.replaced[0].Summary)
}

func TestCleanupInvalidNote(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "Неудачные дубли и подготовку к съёмке не убрал: не смог надёжно выбрать, что вырезать, поэтому "+
		"вырезал только паузы и оговорки. Если остались лишние дубли, ответьте на черновик, например: „убери первый "+
		"дубль вступления“.", fmt.Sprintf(cleanupInvalidNote, cutWithoutTakes))
}

// TestDescribeHonestTakes: «неудачные дубли» in the header only when some were cut — by the worker, or by the editor
// (the cleaning or a disputed repeat cut: Summary.Cleaned).
func TestDescribeHonestTakes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		retakes  int
		cleaned  bool
		expected string
	}{
		{name: "nothing of the takes", expected: cutWithoutTakes},
		{name: "the worker's retakes", retakes: 1, expected: cutWithTakes},
		{name: "the editor's cleaning", cleaned: true, expected: cutWithTakes},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := &editjob.Result{SourceDuration: 60, Duration: 45, Retakes: tt.retakes}
			text := Describe(result, 1, "", &editjob.Summary{Cleaned: tt.cleaned})
			assert.True(t, strings.HasPrefix(text, "Черновик рилс готов: было 1:00, стало 0:45 — вырезал 0:15 ("+
				tt.expected+")."), text)
		})
	}
}

// TestFirstSummaryCleaned: a failed take cleaned or a disputed repeat cut marks the summary, so the header names the
// takes; a cleaning of the preparation and the links alone does not (T3.22).
func TestFirstSummaryCleaned(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		outcome  editplan.Outcome
		expected bool
	}{
		{name: "a failed take cleaned", outcome: editplan.Outcome{Kind: editplan.EditOutcome, Cleaned: 2, CleanedTakes: true}, expected: true},
		{name: "only the preparation and links cleaned", outcome: editplan.Outcome{Kind: editplan.EditOutcome, Cleaned: 2}},
		{name: "a repeat cut", outcome: editplan.Outcome{Kind: editplan.EditOutcome, Cut: 1}, expected: true},
		{name: "nothing", outcome: editplan.Outcome{Kind: editplan.EditOutcome}},
		{name: "no decision", outcome: editplan.Outcome{Kind: editplan.InvalidOutcome}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := &editplan.Request{Version: 1, Plan: &roughcut.Plan{Candidates: []roughcut.Candidate{{ID: 0}}}}
			assert.Equal(t, tt.expected, firstSummary(req, &tt.outcome).Cleaned)
		})
	}
}

// TestDescribeAllDeclined: seven declined requests with the notes and the caption still fit the caption of a video.
func TestDescribeAllDeclined(t *testing.T) {
	t.Parallel()

	note := captionDeclinedNote + " " + editplan.DeclinedText(declinedTexts)
	text := Describe(&editjob.Result{SourceDuration: 250, Duration: 200}, 1, strings.Repeat("сделай всё красиво ", 20),
		&editjob.Summary{Note: note, Changes: []string{"Спорные повторы оставил в ролике: 2."}}, longReelNote(180))

	assert.LessOrEqual(t, utf16Len(text), maxDescriptionUnits)
	assert.True(t, strings.HasPrefix(text, "Черновик рилс готов: было 4:10, стало 3:20"))
	assert.Contains(t, text, longReelNote(180))
	assert.True(t, strings.HasSuffix(text, shortenedNote))
}

// TestRevisionUnclearDeclined: a revision not understood that also asks for music gets both texts.
func TestRevisionUnclearDeclined(t *testing.T) {
	t.Parallel()

	b := &fakeBoard{}
	decider := &fakeDecider{outcome: &editplan.Outcome{Kind: editplan.UnclearOutcome, Declined: declinedTexts[:1]}}

	_, err := newTestExecutorWith(&testDeps{board: b, store: testStore{files: storedFiles(t)}, decider: decider,
		tech: &fakeTech{}}).Execute(t.Context(), revisionData())

	var permanent *tasks.PermanentError
	require.ErrorAs(t, err, &permanent)
	assert.Equal(t, unclearRevisionText+" "+declinedTexts[0], permanent.Message)
}
