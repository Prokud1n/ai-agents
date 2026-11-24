package agents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/llmusage"
	"github.com/Prokud1n/ai-agents/internal/providers/anthropic"
)

const (
	// maxToolIterations bounds model turns in one run, so a model looping on tools cannot burn the budget.
	maxToolIterations = 20
	// recordUsageTimeout bounds the usage insert, which runs even if the run's context is cancelled.
	recordUsageTimeout = 5 * time.Second
)

type messageCreator interface {
	CreateMessage(ctx context.Context, req *anthropic.MessageRequest) (*anthropic.MessageResponse, error)
}

type usageRecorder interface {
	Record(ctx context.Context, data *llmusage.RecordUsageData) error
}

// Runtime runs agents: the tool use loop over the Claude API with usage accounting.
type Runtime struct {
	registry *Registry
	client   messageCreator
	usage    usageRecorder
	cfg      *config.LLM
	log      *slog.Logger
	now      func() time.Time
}

func NewRuntime(
	registry *Registry,
	client messageCreator,
	usage usageRecorder,
	cfg *config.LLM,
	log *slog.Logger,
) *Runtime {
	return &Runtime{registry: registry, client: client, usage: usage, cfg: cfg, log: log, now: time.Now}
}

// Run sends the conversation to the agent and executes its tool calls until the model gives a final answer.
// A failing tool does not stop the run: a short error goes back to the model as a tool_result with is_error.
//
// Stop reasons: end_turn and stop_sequence finish the run; tool_use runs the tools and continues;
// pause_turn sends the paused turn back as is and continues. max_tokens (and a full context window)
// returns ErrMaxTokens, refusal returns ErrRefusal, an unknown one ErrInvalidResponse: in these cases
// the partial Result comes with the error, and its Messages are safe to pass to the next run
// (tool_use blocks without a tool_result are removed). ErrIterationLimit comes with a partial Result too:
// the history up to the last tool results, the tokens and the text of the last turn.
//
// A successful call of a final tool (see finalTool) ends the run with its tool result as the last message and stop
// reason tool_use: the decision is in the tool, another model turn would only cost tokens.
//
// The end of the first user message is a cache breakpoint besides the system prompt, so every turn of a run with
// tools reads the task from the cache.
func (r *Runtime) Run(ctx context.Context, data *RunAgentData) (*Result, error) {
	agent, err := r.registry.Get(data.Agent)
	if err != nil {
		return nil, err
	}

	messages, err := initialMessages(data)
	if err != nil {
		return nil, err
	}

	tools := r.registry.toolsFor(agent)
	req := r.newRequest(agent, data, tools)
	result := &Result{Model: req.Model}

	for range maxToolIterations {
		req.Messages = withCacheBreakpoint(messages)

		resp, err := r.call(ctx, agent, &req)
		if err != nil {
			return nil, err
		}

		result.Calls++
		result.Tokens = result.Tokens.Add(tokensOf(resp.Usage))
		result.Text = resp.Text()
		result.StopReason = resp.StopReason
		assistant := anthropic.Message{Role: anthropic.AssistantRole, Content: resp.Content}

		switch resp.StopReason {
		case anthropic.ToolUseStopReason:
			toolResults, final, err := r.executeTools(ctx, agent, tools, resp.Content)
			if err != nil {
				return nil, err
			}

			messages = slices.Concat(messages, []anthropic.Message{
				assistant,
				{Role: anthropic.UserRole, Content: toolResults},
			})

			if final {
				result.Messages = messages

				return result, nil
			}
		case anthropic.PauseTurnStopReason:
			messages = slices.Concat(messages, []anthropic.Message{assistant})
		default:
			return finish(agent, result, messages, resp)
		}
	}

	result.Messages = messages

	return result, fmt.Errorf("failed to run agent %q: no final answer after %d model turns: %w",
		agent.Name, maxToolIterations, ErrIterationLimit)
}

// finish fills the result of the last turn. Only end_turn and stop_sequence are a success.
func finish(
	agent *Agent,
	result *Result,
	messages []anthropic.Message,
	resp *anthropic.MessageResponse,
) (*Result, error) {
	var stopErr error

	switch resp.StopReason {
	case anthropic.EndTurnStopReason, anthropic.StopSequenceStopReason:
		result.Messages = slices.Concat(messages,
			[]anthropic.Message{{Role: anthropic.AssistantRole, Content: resp.Content}})

		return result, nil
	case anthropic.MaxTokensStopReason, anthropic.ContextWindowStopReason:
		stopErr = ErrMaxTokens
	case anthropic.RefusalStopReason:
		stopErr = ErrRefusal
	default:
		stopErr = ErrInvalidResponse
	}

	result.Messages = withoutPendingToolUse(messages, resp.Content)

	return result, fmt.Errorf("failed to run agent %q: stop reason %q: %w", agent.Name, resp.StopReason, stopErr)
}

// withoutPendingToolUse appends the last assistant turn without its tool_use blocks: they have no
// tool_result, and the API rejects such a history. A turn left empty is dropped.
func withoutPendingToolUse(messages []anthropic.Message, content []anthropic.ContentBlock) []anthropic.Message {
	kept := make([]anthropic.ContentBlock, 0, len(content))

	for i := range content {
		if content[i].Type != anthropic.ToolUseContentType {
			kept = append(kept, content[i])
		}
	}

	if len(kept) == 0 {
		return slices.Clone(messages)
	}

	return slices.Concat(messages, []anthropic.Message{{Role: anthropic.AssistantRole, Content: kept}})
}

func (r *Runtime) newRequest(agent *Agent, data *RunAgentData, tools []Tool) anthropic.MessageRequest {
	req := anthropic.MessageRequest{
		Model:     firstNonEmpty(data.Model, agent.Model, r.cfg.DefaultModel),
		MaxTokens: r.cfg.DefaultMaxTokens,
		System: []anthropic.SystemBlock{{
			Type:         anthropic.TextContentType,
			Text:         BuildSystemPrompt(agent),
			CacheControl: anthropic.EphemeralCache(),
		}},
		Tools: make([]anthropic.ToolDefinition, 0, len(tools)),
	}

	if data.MaxTokens > 0 {
		req.MaxTokens = data.MaxTokens
	}

	for _, t := range tools {
		req.Tools = append(req.Tools, toolDefinition(t))
	}

	return req
}

// call makes one API call and records its usage; a failed recording is logged and does not fail the run.
// The usage is recorded even when ctx is cancelled right after the call: the call is already billed.
func (r *Runtime) call(
	ctx context.Context,
	agent *Agent,
	req *anthropic.MessageRequest,
) (*anthropic.MessageResponse, error) {
	started := r.now()

	resp, err := r.client.CreateMessage(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to run agent %q: %w", agent.Name, err)
	}

	usage := &llmusage.RecordUsageData{
		Agent:         agent.Name,
		Model:         req.Model,
		ResponseModel: resp.Model,
		RequestID:     resp.RequestID,
		Tokens:        tokensOf(resp.Usage),
		Duration:      r.now().Sub(started),
		CreatedAt:     started,
	}

	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordUsageTimeout)
	defer cancel()

	if err := r.usage.Record(recordCtx, usage); err != nil {
		r.log.ErrorContext(ctx, "agents: failed to record llm usage",
			"error", err, logKeyAgent, agent.Name, "request_id", resp.RequestID)
	}

	return resp, nil
}

// executeTools runs every tool_use block of the response and returns tool_result blocks in the same order, and
// whether a final tool succeeded. A tool error goes to the model as a short message (see ToolError), its details go
// to the log.
func (r *Runtime) executeTools(
	ctx context.Context,
	agent *Agent,
	tools []Tool,
	content []anthropic.ContentBlock,
) ([]anthropic.ContentBlock, bool, error) {
	results := make([]anthropic.ContentBlock, 0, len(content))
	final := false

	for i := range content {
		block := &content[i]
		if block.Type != anthropic.ToolUseContentType {
			continue
		}

		output, ends, err := runTool(ctx, tools, block)
		if err != nil {
			r.log.WarnContext(ctx, "agents: tool failed",
				"error", err, logKeyAgent, agent.Name, "tool", block.Name, "tool_use_id", block.ID)
			results = append(results, anthropic.ToolResultBlock(block.ID, toolErrorMessage(block.Name, err), true))

			continue
		}

		results = append(results, anthropic.ToolResultBlock(block.ID, output, false))
		final = final || ends
	}

	if len(results) == 0 {
		return nil, false, fmt.Errorf("failed to run agent %q: stop reason tool_use without tool_use blocks: %w",
			agent.Name, ErrInvalidResponse)
	}

	return results, final, nil
}

// runTool runs the tool of the block; true when it is a final tool that succeeded.
func runTool(ctx context.Context, tools []Tool, block *anthropic.ContentBlock) (string, bool, error) {
	for _, t := range tools {
		if t.Name() != block.Name {
			continue
		}

		output, err := t.Execute(ctx, block.Input)
		if err != nil {
			return "", false, fmt.Errorf("failed to execute tool %q: %w", block.Name, err)
		}

		f, ok := t.(finalTool)

		return output, ok && f.EndsRun(), nil
	}

	return "", false, NewToolError(fmt.Sprintf("tool %q is not available to this agent", block.Name))
}

// withCacheBreakpoint returns the messages with the end of the first user message (the task) marked as a cacheable
// prefix; with the system prompt that is two of the four breakpoints the API allows. The caller's messages are not
// changed, so the history of the result has no marks.
func withCacheBreakpoint(messages []anthropic.Message) []anthropic.Message {
	for i := range messages {
		if messages[i].Role != anthropic.UserRole {
			continue
		}

		content := messages[i].Content
		if len(content) == 0 || !content[len(content)-1].Cacheable() {
			return messages
		}

		last := content[len(content)-1]
		last.CacheControl = anthropic.EphemeralCache()

		out := slices.Clone(messages)
		out[i].Content = append(slices.Clone(content[:len(content)-1]), last)

		return out
	}

	return messages
}

// toolErrorMessage is what the model sees: the ToolError message, or a generic one for other errors.
func toolErrorMessage(tool string, err error) string {
	var toolErr *ToolError
	if errors.As(err, &toolErr) {
		return toolErr.Message
	}

	return fmt.Sprintf("tool %q failed with an internal error", tool)
}

// initialMessages copies the history and appends the new input, so the caller's slice is never modified.
// A history ending with an assistant message needs a new Input: otherwise the model would continue that
// message (a prefill) instead of answering.
func initialMessages(data *RunAgentData) ([]anthropic.Message, error) {
	if len(data.Messages) == 0 && data.Input == "" {
		return nil, fmt.Errorf("failed to run agent %q: input is empty: %w", data.Agent, ErrInvalidRunData)
	}

	if data.Input == "" && data.Messages[len(data.Messages)-1].Role == anthropic.AssistantRole {
		return nil, fmt.Errorf("failed to run agent %q: the history ends with an assistant message and input is empty: %w",
			data.Agent, ErrInvalidRunData)
	}

	messages := make([]anthropic.Message, 0, len(data.Messages)+1)
	messages = append(messages, data.Messages...)

	if data.Input != "" {
		messages = append(messages, anthropic.Message{
			Role:    anthropic.UserRole,
			Content: []anthropic.ContentBlock{anthropic.TextBlock(data.Input)},
		})
	}

	return messages, nil
}

func tokensOf(u anthropic.Usage) llmusage.Tokens {
	return llmusage.Tokens{
		Input:      u.InputTokens,
		Output:     u.OutputTokens,
		CacheRead:  u.CacheReadInputTokens,
		CacheWrite: u.CacheCreationInputTokens,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}
