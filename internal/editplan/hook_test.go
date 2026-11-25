package editplan

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

// fifteenWords is one word over the limit of a hook.
const fifteenWords = "раз два три четыре пять шесть семь восемь девять десять раз два три четыре пять"

// hook90 is a hook of exactly 90 characters and 13 words.
const hook90 = "Видео для всех, кто боится начинать тренироваться дома и почему-то считает, что поздновато"

const (
	hookWords    = "от 1 до 14 слов"
	hookEmoji    = "без эмодзи и хештегов"
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
		{name: "90 characters", input: new(hook90)},
		{name: "longer than 90", input: new(hook90 + "!"), expected: "длиннее 90 символов"},
		{name: "an address to the audience", input: new("Видео для тех, кто боится начать тренироваться дома")},
		{name: "one word", input: new("Планка")},
		{name: "quotes and a period are not counted", input: new("«" + hook90 + "».")},
		{name: "fifteen words", input: new(fifteenWords), expected: hookWords},
		{name: "emoji", input: new("Пресс за 10 минут 🔥"), expected: hookEmoji},
		{name: "emoji by the selector", input: new("Пресс за 10 минут ❤️"), expected: hookEmoji},
		{name: "hashtag", input: new("Пресс за 10 #минут"), expected: hookEmoji},
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
			name: "an invalid hook", version: 1, input: Input{HookTitle: new(fifteenWords)},
			err: hookWords,
		},
		{
			name: "quotes and the period are dropped", version: 1, input: Input{HookTitle: new(" «3 ошибки в планке.» ")},
			hook: testHook, changes: []string{"Заголовок: «" + testHook + "»."},
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
			name:     "a short reel planned for the hook only",
			req:      hookOnlyRequest,
			expected: []string{"Подписи нет — предложи только заголовок-хук; вырезать ничего не нужно."},
		},
		{
			name: "a reel with disputed repeats and the hook",
			req: func() *Request {
				req := testRequest(1, nil)
				req.Hook = true

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

func TestNormalizeHook(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "spaces", input: "  3  ошибки\tв планке ", expected: testHook},
		{name: "guillemets and a period", input: "«3 ошибки в планке».", expected: testHook},
		{name: "straight quotes", input: `"3 ошибки в планке"`, expected: testHook},
		{name: "low quotes", input: "„3 ошибки в планке“", expected: testHook},
		{name: "a period inside quotes", input: "«3 ошибки в планке.»", expected: testHook},
		{name: "an ellipsis stays", input: "Планка, которая…", expected: "Планка, которая…"},
		{name: "three dots stay", input: "Планка, которая...", expected: "Планка, которая..."},
		{name: "quotes inside stay", input: "Упражнение «планка»", expected: "Упражнение «планка»"},
		{name: "a question mark stays", input: "Болит спина?", expected: "Болит спина?"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, normalizeHook(tt.input))
		})
	}
}

// hookOnlyRequest is a short talking head stopped only for the hook title: no caption, no disputed repeats.
func hookOnlyRequest() *Request {
	req := testRequest(1, nil)
	req.Hook = true
	req.Plan.Candidates = nil

	return req
}

// TestApplyHookOnly: a reel stopped only for the hook is not cut or trimmed by the model; the hook and the corrections
// of the subtitles pass. With a caption, disputed repeats or the hook off the reel can be cut as before.
func TestApplyHookOnly(t *testing.T) {
	t.Parallel()

	const refused = "ничего не вырезай и не обрезай"

	tests := []struct {
		name  string
		req   func() *Request
		input Input
		err   string
	}{
		{name: "the hook", req: hookOnlyRequest, input: Input{HookTitle: new(testHook)}},
		{name: "a correction", req: hookOnlyRequest, input: Input{FixWords: []FixInput{{Index: 7, Text: "делайте"}}}},
		{name: "words cut", req: hookOnlyRequest, input: Input{Remove: []roughcut.WordRange{{From: 7, To: 8}}}, err: refused},
		{name: "the start trimmed", req: hookOnlyRequest, input: Input{TrimStart: 1}, err: refused},
		{name: "the end trimmed", req: hookOnlyRequest, input: Input{TrimEnd: 1}, err: refused},
		{name: "seconds cut", req: hookOnlyRequest, input: Input{CutSeconds: []SecondsRange{{From: 1, To: 2}}}, err: refused},
		{name: "a length", req: hookOnlyRequest, input: Input{TargetSeconds: 3}, err: refused},
		{
			name: "with a caption the reel is cut", input: Input{TrimStart: 1},
			req: func() *Request {
				req := hookOnlyRequest()
				req.Caption = "убери начало"

				return req
			},
		},
		{
			name: "with the hook off the reel is cut", input: Input{TrimStart: 1},
			req: func() *Request {
				req := hookOnlyRequest()
				req.Hook = false

				return req
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			in := tt.input
			in.Decision, in.Reason = EditDecision, testReason

			outcome, err := Apply(tt.req(), &in)
			if tt.err != "" {
				var check *CheckError
				require.ErrorAs(t, err, &check)
				assert.Contains(t, check.Message, tt.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, EditOutcome, outcome.Kind)
		})
	}
}
