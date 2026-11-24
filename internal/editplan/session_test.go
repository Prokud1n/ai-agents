package editplan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/agents"
)

const (
	validInput   = `{"decision": "edit", "reason": "убрать начало", "trim_start_seconds": 1}`
	invalidInput = `{"decision": "edit", "reason": "x", "remove_words": [{"from": 40, "to": 41}]}`
)

// TestSubmitTool: an invalid edit goes back to the model with the reason once, the second ends the decision; the
// first valid one is the decision.
func TestSubmitTool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		inputs       []string
		expectedKind OutcomeKind
		toolErrors   int
	}{
		{name: "valid at once", inputs: []string{validInput}, expectedKind: EditOutcome},
		{name: "valid after one failure", inputs: []string{invalidInput, validInput}, expectedKind: EditOutcome, toolErrors: 1},
		{name: "invalid twice", inputs: []string{invalidInput, invalidInput}, expectedKind: InvalidOutcome, toolErrors: 2},
		{
			name: "unknown fields", inputs: []string{`{"decision": "edit", "reason": "x", "music": "on"}`, `[]`},
			expectedKind: InvalidOutcome, toolErrors: 2,
		},
		{name: "refusal", inputs: []string{`{"decision": "cannot", "reason": "музыка"}`}, expectedKind: CannotOutcome},
		{name: "later calls do not change the decision", inputs: []string{validInput, invalidInput}, expectedKind: EditOutcome},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := NewSession(testRequest(2, nil))
			ctx := WithSession(t.Context(), session)
			tool := NewSubmitTool()
			toolErrors := 0

			for _, input := range tt.inputs {
				_, err := tool.Execute(ctx, json.RawMessage(input))
				if err != nil {
					var toolErr *agents.ToolError
					require.ErrorAs(t, err, &toolErr)

					toolErrors++
				}
			}

			outcome, ok := session.Outcome()
			require.True(t, ok)
			assert.Equal(t, tt.expectedKind, outcome.Kind)
			assert.Equal(t, tt.toolErrors, toolErrors)
		})
	}
}

func TestSubmitToolMessages(t *testing.T) {
	t.Parallel()

	session := NewSession(testRequest(2, nil))
	ctx := WithSession(t.Context(), session)
	tool := NewSubmitTool()

	_, err := tool.Execute(ctx, json.RawMessage(invalidInput))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "вне расшифровки")
	assert.Contains(t, err.Error(), "последняя попытка")

	_, err = tool.Execute(ctx, json.RawMessage(invalidInput))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Попытки исчерпаны")

	answer, err := tool.Execute(ctx, json.RawMessage(validInput))
	require.NoError(t, err)
	assert.Equal(t, decidedAnswer, answer)

	_, err = tool.Execute(t.Context(), json.RawMessage(validInput))
	require.EqualError(t, err, noSessionText, "outside an editing task (cmd/agent-run)")

	assert.True(t, json.Valid(tool.InputSchema()))
	assert.Equal(t, SubmitToolName, tool.Name())
}

// fakeRunner plays the model: it calls submit_edit with its inputs (through the run's context), then answers.
type fakeRunner struct {
	inputs []string
	err    error
	text   string
	got    *agents.RunAgentData
}

func (r *fakeRunner) Run(ctx context.Context, data *agents.RunAgentData) (*agents.Result, error) {
	r.got = data

	for _, input := range r.inputs {
		_, _ = NewSubmitTool().Execute(ctx, json.RawMessage(input))
	}

	if r.err != nil {
		return nil, r.err
	}

	return &agents.Result{Text: r.text}, nil
}

func TestDecider(t *testing.T) {
	t.Parallel()

	errOverloaded := errors.New("overloaded")

	tests := []struct {
		name         string
		runner       *fakeRunner
		expectedKind OutcomeKind
		expectedErr  error
	}{
		{name: "decided", runner: &fakeRunner{inputs: []string{validInput}, text: "готово"}, expectedKind: EditOutcome},
		{
			name:         "decided, then the run failed: the decision counts",
			runner:       &fakeRunner{inputs: []string{validInput}, err: fmt.Errorf("run: %w", agents.ErrMaxTokens)},
			expectedKind: EditOutcome,
		},
		{name: "answered with text only", runner: &fakeRunner{text: "Обрежу начало."}, expectedKind: NoAnswerOutcome},
		{name: "the API failed", runner: &fakeRunner{err: errOverloaded}, expectedErr: errOverloaded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := testRequest(2, nil)
			req.Revision = "убери первую секунду"

			outcome, err := NewDecider(tt.runner, slog.New(slog.DiscardHandler)).Decide(t.Context(), req)

			assert.Equal(t, AgentName, tt.runner.got.Agent)
			assert.Contains(t, tt.runner.got.Input, "убери первую секунду")

			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expectedKind, outcome.Kind)
		})
	}
}

func TestDeciderRejectsEmptyRequest(t *testing.T) {
	t.Parallel()

	runner := &fakeRunner{}

	_, err := NewDecider(runner, slog.New(slog.DiscardHandler)).Decide(t.Context(), &Request{TaskID: 1, Version: 2})
	require.ErrorIs(t, err, ErrInvalidRequest)
	assert.Nil(t, runner.got, "no model call")
}
