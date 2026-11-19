package agents

import (
	"github.com/Prokud1n/ai-agents/internal/llmusage"
	"github.com/Prokud1n/ai-agents/internal/providers/anthropic"
)

type RunAgentData struct {
	Agent string
	// Messages is the conversation so far (optional); it is not modified.
	Messages []anthropic.Message
	// Input is a new user message appended after Messages; required when Messages is empty.
	Input string
	// Model overrides the agent and default model (optional).
	Model string
	// MaxTokens overrides LLM_DEFAULT_MAX_TOKENS (optional).
	MaxTokens int
}

type Result struct {
	// Text is the final answer: text blocks of the last assistant message.
	Text string
	// Messages is the whole conversation including the new turns; pass it to the next run to continue.
	// With ErrMaxTokens or ErrRefusal it holds the partial turn without unanswered tool_use blocks.
	Messages []anthropic.Message
	// Model is the requested model: usage costs are calculated by it.
	Model      string
	StopReason anthropic.StopReason
	// Calls is the number of API calls (model turns) made.
	Calls int
	// Tokens is the usage summed over all calls.
	Tokens llmusage.Tokens
}
