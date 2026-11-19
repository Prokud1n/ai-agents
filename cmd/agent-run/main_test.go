package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/llmusage"
)

const partialHeader = "--- partial answer:"

func TestPartialReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    error
		expected string
	}{
		{name: "complete", input: nil, expected: ""},
		{
			name:     "iteration limit",
			input:    fmt.Errorf("failed to run agent: %w", agents.ErrIterationLimit),
			expected: agents.ErrIterationLimit.Error(),
		},
		{name: "max tokens", input: agents.ErrMaxTokens, expected: agents.ErrMaxTokens.Error()},
		{name: "refusal", input: agents.ErrRefusal, expected: agents.ErrRefusal.Error()},
		{name: "other error", input: agents.ErrInvalidRunData, expected: "run failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, partialReason(tt.input))
		})
	}
}

func TestPrintResult(t *testing.T) {
	t.Parallel()

	usage, err := llmusage.NewService(&config.LLM{Prices: "claude-sonnet-5-5=2/10/2.5/0.1"}, nil,
		slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	result := &agents.Result{
		Text: "Let me check.", Model: "claude-sonnet-5-5", Calls: 20,
		Tokens: llmusage.Tokens{Input: 1_000_000},
	}

	tests := []struct {
		name          string
		partial       string
		expectPartial bool
	}{
		{name: "complete answer is not marked", partial: ""},
		{name: "iteration limit is marked", partial: agents.ErrIterationLimit.Error(), expectPartial: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			printResult(&out, result, usage, tt.partial)

			if tt.expectPartial {
				assert.Contains(t, out.String(), partialHeader+" "+tt.partial+" ---\nLet me check.")
			} else {
				assert.NotContains(t, out.String(), partialHeader)
			}

			assert.Contains(t, out.String(), "cost: $2.000000000")
		})
	}
}
