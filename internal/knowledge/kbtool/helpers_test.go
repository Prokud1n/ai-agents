package kbtool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/llmusage"
	"github.com/Prokud1n/ai-agents/internal/providers/anthropic"
)

const (
	testAgentsDir = "testdata/agents"
	// testSkillsDir does not exist: no shared skills.
	testSkillsDir = "testdata/skills"
	writerAgent   = "writer"
	plainAgent    = "plain"
	testLLMModel  = "claude-test"
	finalAnswer   = "Готово."
	maxTestTokens = 1024
)

// fakeLLM is a scripted model: every turn but the last calls the tools given in toolCalls (one turn per
// input), the last answers with finalAnswer. It keeps the requests it got.
type fakeLLM struct {
	mu        sync.Mutex
	toolCalls []string
	requests  []anthropic.MessageRequest
}

func (f *fakeLLM) CreateMessage(_ context.Context, req *anthropic.MessageRequest) (*anthropic.MessageResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.requests = append(f.requests, *req)
	turn := len(f.requests) - 1

	if turn < len(f.toolCalls) {
		return &anthropic.MessageResponse{
			Model:      testLLMModel,
			StopReason: anthropic.ToolUseStopReason,
			Content: []anthropic.ContentBlock{{
				Type:  anthropic.ToolUseContentType,
				ID:    fmt.Sprintf("toolu_%d", turn),
				Name:  Name,
				Input: json.RawMessage(f.toolCalls[turn]),
			}},
		}, nil
	}

	return &anthropic.MessageResponse{
		Model:      testLLMModel,
		StopReason: anthropic.EndTurnStopReason,
		Content:    []anthropic.ContentBlock{anthropic.TextBlock(finalAnswer)},
	}, nil
}

func (f *fakeLLM) sentRequests() []anthropic.MessageRequest {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]anthropic.MessageRequest(nil), f.requests...)
}

type nopRecorder struct{}

func (nopRecorder) Record(context.Context, *llmusage.RecordUsageData) error {
	return nil
}

func newTestRuntime(t *testing.T, tools []agents.Tool, llm *fakeLLM) *agents.Runtime {
	t.Helper()

	registry, err := agents.LoadRegistry(testAgentsDir, testSkillsDir, tools)
	require.NoError(t, err)

	return agents.NewRuntime(registry, llm, nopRecorder{},
		&config.LLM{DefaultModel: testLLMModel, DefaultMaxTokens: maxTestTokens}, slog.New(slog.DiscardHandler))
}

// toolResults returns the tool_result blocks the runtime sent back to the model, in order.
func toolResults(requests []anthropic.MessageRequest) []anthropic.ContentBlock {
	if len(requests) == 0 {
		return nil
	}

	var results []anthropic.ContentBlock

	last := requests[len(requests)-1]
	for _, m := range last.Messages {
		for i := range m.Content {
			if m.Content[i].Type == anthropic.ToolResultContentType {
				results = append(results, m.Content[i])
			}
		}
	}

	return results
}

func toolNames(req *anthropic.MessageRequest) []string {
	names := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		names = append(names, tool.Name)
	}

	return names
}
