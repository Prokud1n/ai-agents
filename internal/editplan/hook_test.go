package editplan

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

const (
	testHook     = "3 ошибки в планке"
	oldHook      = "Спина ровно всегда"
	hookNoChange = "ничего не меняет"
)

func TestCheckHook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *string
		expected string
	}{
		{name: "not given", input: nil},
		{name: "removed", input: new("  ")},
		{name: "a hook", input: new(testHook)},
		{name: "a number sign and degrees are text", input: new("Растяжка № 1 на 90°")},
		{name: "40 characters", input: new("Почему болит поясница после приседаний?!")},
		{name: "longer than 40", input: new("Пять ошибок в планке, которые делают почти все"), expected: "длиннее 40 символов"},
		{name: "one word", input: new("Планка"), expected: "от 2 до 6 слов"},
		{name: "seven words", input: new("раз два три четыре пять шесть семь"), expected: "от 2 до 6 слов"},
		{name: "emoji", input: new("Пресс за 10 минут 🔥"), expected: "без эмодзи и хештегов"},
		{name: "emoji by the selector", input: new("Пресс за 10 минут ❤️"), expected: "без эмодзи и хештегов"},
		{name: "hashtag", input: new("Пресс за 10 #минут"), expected: "без эмодзи и хештегов"},
		{name: "line break", input: new("Пресс за\n10 минут"), expected: noLineBreaks},
		{name: "markup", input: new(`Пресс {\b1} за 10`), expected: noLineBreaks},
		{name: "invisible", input: new("Пресс за\u200b10 минут"), expected: noLineBreaks},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkHook(tt.input)
			if tt.expected == "" {
				assert.NoError(t, err)

				return
			}

			var check *CheckError
			require.ErrorAs(t, err, &check)
			assert.Contains(t, check.Message, tt.expected)
		})
	}
}

// TestApplyHook: the hook title is set, changed, removed and kept by the versions, with a line in the description;
// a revision that only repeats the hook changes nothing.
func TestApplyHook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		version  int
		previous *roughcut.Edit
		input    Input
		hook     string
		changes  []string
		err      string
	}{
		{
			name: "the first version gets the model's hook", version: 1,
			input: Input{HookTitle: new("  3 ошибки   в планке ")}, hook: testHook,
			changes: []string{"Заголовок: «" + testHook + "»."},
		},
		{name: "the first version without a hook", version: 1, input: Input{}, hook: ""},
		{
			name: "a revision changes the hook", version: 2, previous: &roughcut.Edit{Hook: oldHook},
			input: Input{HookTitle: new(testHook)}, hook: testHook, changes: []string{"Заголовок: «" + testHook + "»."},
		},
		{
			name: "a revision removes the hook", version: 2, previous: &roughcut.Edit{Hook: oldHook},
			input: Input{HookTitle: new("")}, hook: "", changes: []string{hookRemovedLine},
		},
		{
			name: "a revision adds a hook", version: 2, previous: &roughcut.Edit{},
			input: Input{HookTitle: new(testHook)}, hook: testHook, changes: []string{"Заголовок: «" + testHook + "»."},
		},
		{
			name: "another change keeps the hook", version: 2, previous: &roughcut.Edit{Hook: oldHook},
			input: Input{TrimEnd: 1}, hook: oldHook,
		},
		{
			name: "the same hook again changes nothing", version: 2, previous: &roughcut.Edit{Hook: oldHook},
			input: Input{HookTitle: new(" " + oldHook)}, err: hookNoChange,
		},
		{
			name: "removing no hook changes nothing", version: 2, previous: &roughcut.Edit{},
			input: Input{HookTitle: new("")}, err: hookNoChange,
		},
		{
			name: "an invalid hook", version: 1, input: Input{HookTitle: new("Планка")}, err: "от 2 до 6 слов",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			in := tt.input
			in.Decision, in.Reason = EditDecision, testReason

			outcome, err := Apply(testRequest(tt.version, tt.previous), &in)
			if tt.err != "" {
				var check *CheckError
				require.ErrorAs(t, err, &check, "a check failure for the model")
				assert.Contains(t, check.Message, tt.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, EditOutcome, outcome.Kind)
			assert.Equal(t, tt.hook, outcome.Edit.Hook)

			if len(tt.changes) > 0 {
				assert.Equal(t, tt.changes[0], outcome.Changes[0], "the hook line goes first")
			} else {
				assert.NotContains(t, strings.Join(outcome.Changes, "\n"), "аголов")
			}
		})
	}
}

func TestPromptHook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		req      func() *Request
		expected []string
	}{
		{
			name: "the first version proposes a hook",
			req: func() *Request {
				req := testRequest(1, nil)
				req.Hook = true

				return req
			},
			expected: []string{"Заголовок-хук: предложи его в hook_title"},
		},
		{
			name: "a first version without the hook by default",
			req:  func() *Request { return testRequest(1, nil) },
			expected: []string{
				"Подписи нет — реши только спорные повторы.",
				"Заголовок-хук ставь через hook_title, только если тренер просит в подписи.",
			},
		},
		{
			name: "a short reel planned for the hook only",
			req: func() *Request {
				req := testRequest(1, nil)
				req.Hook = true
				req.Plan.Candidates = nil

				return req
			},
			expected: []string{"Подписи нет — предложи заголовок-хук и реши спорные повторы."},
		},
		{
			name:     "a revision with a hook",
			req:      func() *Request { return testRequest(2, &roughcut.Edit{Hook: oldHook}) },
			expected: []string{"Заголовок-хук сейчас: «" + oldHook + "». Поменять — hook_title"},
		},
		{
			name:     "a revision without one",
			req:      func() *Request { return testRequest(2, &roughcut.Edit{}) },
			expected: []string{"Заголовка-хука нет. Добавь его через hook_title, только если тренер просит."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			prompt := Prompt(tt.req())
			for _, part := range tt.expected {
				assert.Contains(t, prompt, part)
			}
		})
	}
}
