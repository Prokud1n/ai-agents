package agents

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/llmusage"
	"github.com/Prokud1n/ai-agents/internal/providers/anthropic"
)

// maxToolIterations bounds model turns in one run, so a model looping on tools cannot burn the budget.
const maxToolIterations = 20

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
// A failing tool does not stop the run: the error goes back to the model as a tool_result with is_error.
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
		req.Messages = messages

		resp, err := r.call(ctx, agent, &req)
		if err != nil {
			return nil, err
		}

		result.Calls++
		result.Tokens = result.Tokens.Add(tokensOf(resp.Usage))
		messages = append(messages, anthropic.Message{Role: anthropic.AssistantRole, Content: resp.Content})

		if resp.StopReason != anthropic.ToolUseStopReason {
			result.Text = resp.Text()
			result.Messages = messages
			result.StopReason = resp.StopReason

			return result, nil
		}

		messages = append(messages, anthropic.Message{
			Role:    anthropic.UserRole,
			Content: r.executeTools(ctx, agent, tools, resp.Content),
		})
	}

	return nil, fmt.Errorf("failed to run agent %q: no final answer after %d model turns: %w",
		agent.Name, maxToolIterations, ErrIterationLimit)
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
		Agent:     agent.Name,
		Model:     firstNonEmpty(resp.Model, req.Model),
		RequestID: resp.RequestID,
		Tokens:    tokensOf(resp.Usage),
		Duration:  r.now().Sub(started),
		CreatedAt: started,
	}

	if err := r.usage.Record(ctx, usage); err != nil {
		r.log.ErrorContext(ctx, "agents: failed to record llm usage",
			"error", err, "agent", agent.Name, "request_id", resp.RequestID)
	}

	return resp, nil
}

// executeTools runs every tool_use block of the response and returns tool_result blocks in the same order.
func (r *Runtime) executeTools(
	ctx context.Context,
	agent *Agent,
	tools []Tool,
	content []anthropic.ContentBlock,
) []anthropic.ContentBlock {
	results := make([]anthropic.ContentBlock, 0, len(content))

	for i := range content {
		block := &content[i]
		if block.Type != anthropic.ToolUseContentType {
			continue
		}

		output, err := runTool(ctx, tools, block)
		if err != nil {
			r.log.WarnContext(ctx, "agents: tool failed", "error", err, "agent", agent.Name, "tool", block.Name)
			results = append(results, anthropic.ToolResultBlock(block.ID, err.Error(), true))

			continue
		}

		results = append(results, anthropic.ToolResultBlock(block.ID, output, false))
	}

	return results
}

func runTool(ctx context.Context, tools []Tool, block *anthropic.ContentBlock) (string, error) {
	for _, t := range tools {
		if t.Name() != block.Name {
			continue
		}

		output, err := t.Execute(ctx, block.Input)
		if err != nil {
			return "", fmt.Errorf("failed to execute tool %q: %w", block.Name, err)
		}

		return output, nil
	}

	return "", fmt.Errorf("failed to execute tool %q: the tool is not available to this agent: %w",
		block.Name, ErrInvalidRunData)
}

// initialMessages copies the history and appends the new input, so the caller's slice is never modified.
func initialMessages(data *RunAgentData) ([]anthropic.Message, error) {
	if len(data.Messages) == 0 && data.Input == "" {
		return nil, fmt.Errorf("failed to run agent %q: input is empty: %w", data.Agent, ErrInvalidRunData)
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
