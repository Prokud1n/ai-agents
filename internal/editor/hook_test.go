package editor

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/providers/anthropic"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	// overloadedRetried names the cases of an overloaded API retried by the task.
	overloadedRetried = "overloaded: retried by the task"
	testHook          = "3 ошибки в планке"
	testHookLine      = "Заголовок: «3 ошибки в планке»."
	testFixLine       = "Исправил в субтитрах: «держим» → «держите»."
)

// TestExecutorHookInAutoJob: with EDITOR_HOOK_TITLE the auto job stops after the plan for the hook title.
func TestExecutorHookInAutoJob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		hook     bool
		expected bool
	}{
		{name: "on", hook: true, expected: true},
		{name: "off", hook: false, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := &fakeBoard{}
			e := newTestExecutor(b)
			e.hookTitle = tt.hook

			data := editData()
			data.Input = ""

			_, err := e.Execute(t.Context(), data)

			var deferred *tasks.DeferredError
			require.ErrorAs(t, err, &deferred)
			require.Len(t, b.submitted, 1)
			assert.Equal(t, tt.expected, b.submitted[0].Hook)
			assert.False(t, b.submitted[0].Decide)
		})
	}
}

// storedShortFiles: a short talking head without disputed repeats — the worker stopped it only for the hook title.
func storedShortFiles(t *testing.T) map[string][]byte {
	t.Helper()

	files := storedFiles(t)
	keys, err := editjob.Files(testStore{}, testSourceKey, 1)
	require.NoError(t, err)

	plan := testPlan()
	plan.Candidates = nil

	raw, err := json.Marshal(plan)
	require.NoError(t, err)

	files[keys.Plan] = raw

	return files
}

// TestExecutorDecidesHook: the model's hook title goes to the render with its line in the description; when the model
// cannot decide, the automatic cut is rendered without a hook (no note: nothing else was asked), and a temporary
// failure is retried by the task first.
func TestExecutorDecidesHook(t *testing.T) {
	t.Parallel()

	hooked := &editplan.Outcome{
		Kind: editplan.EditOutcome, Changes: []string{testHookLine},
		Edit: roughcut.Edit{Hook: testHook, Notes: []string{"Решение по черновой склейке: хук"}},
	}
	unhooked := &editplan.Outcome{
		Kind: editplan.EditOutcome, Changes: []string{testFixLine},
		Edit: roughcut.Edit{Words: []roughcut.WordFix{{Index: 1, Text: "держите"}}},
	}

	tests := []struct {
		name     string
		decider  *fakeDecider
		last     bool
		retried  bool
		edit     *roughcut.Edit
		expected *editjob.Summary
	}{
		{
			name: "the model's hook", decider: &fakeDecider{outcome: hooked}, edit: &hooked.Edit,
			expected: &editjob.Summary{Changes: []string{testHookLine}},
		},
		{
			name: "the model found no hook: a hint last", decider: &fakeDecider{outcome: unhooked}, edit: &unhooked.Edit,
			expected: &editjob.Summary{Changes: []string{testFixLine, noHookHint}},
		},
		{
			name:    "no API key: no hook",
			decider: &fakeDecider{err: fmt.Errorf("run: %w", anthropic.ErrAPIKeyMissing)}, expected: &editjob.Summary{},
		},
		{
			name:    overloadedRetried,
			decider: &fakeDecider{err: &anthropic.APIError{StatusCode: http.StatusServiceUnavailable}}, retried: true,
		},
		{
			name:    "overloaded on the last attempt: no hook",
			decider: &fakeDecider{err: &anthropic.APIError{StatusCode: http.StatusServiceUnavailable}}, last: true,
			expected: &editjob.Summary{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			planned := pendingJob(time.Hour)
			planned.Stage, planned.Hook = editjob.AutoStage, true
			b := &fakeBoard{job: planned, result: &editjob.Result{JobID: testJobID, Status: editjob.PlannedStatus}}
			e := newTestExecutorWith(&testDeps{board: b, store: testStore{files: storedShortFiles(t)},
				decider: tt.decider, tech: &fakeTech{}})
			e.hookTitle = true

			data := editData()
			data.Input, data.LastAttempt = "", tt.last

			_, err := e.Execute(t.Context(), data)

			require.Len(t, tt.decider.requests, 1)
			assert.True(t, tt.decider.requests[0].Hook, "the model is asked for the hook")

			if tt.retried {
				var temporary *tasks.TemporaryError
				require.ErrorAs(t, err, &temporary)
				assert.Empty(t, b.replaced)

				return
			}

			var deferred *tasks.DeferredError
			require.ErrorAs(t, err, &deferred)
			require.Len(t, b.replaced, 1)
			assert.Equal(t, tt.edit, b.replaced[0].Edit)
			assert.Equal(t, tt.expected, b.replaced[0].Summary)
		})
	}
}

// TestDescribeHook: the hook line of the first version and of a revision is in the description of the draft.
func TestDescribeHook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		version  int
		summary  *editjob.Summary
		expected string
	}{
		{
			name: "first version", version: 1, summary: &editjob.Summary{Changes: []string{testHookLine}},
			expected: "Черновик рилс готов: 0:45, вырезать было почти нечего.\n" + testHookLine,
		},
		{
			name: "removed by a revision", version: 2,
			summary:  &editjob.Summary{Changes: []string{"Убрал заголовок."}, PreviousDuration: 45},
			expected: "Новая версия рилс: было 0:45, стало 0:45.\nУбрал заголовок.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := &editjob.Result{JobID: testJobID, Status: editjob.DoneStatus, SourceDuration: 45, Duration: 45}
			assert.Equal(t, tt.expected, Describe(result, tt.version, "", tt.summary))
		})
	}
}
