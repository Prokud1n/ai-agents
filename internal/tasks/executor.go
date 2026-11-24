package tasks

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/providers/anthropic"
)

// Executor runs a task and returns the draft. A *PermanentError gives the run up at once; a *DeferredError puts
// the task back to the queue without counting the attempt (the work goes on elsewhere); any other error is retried.
// An empty draft text is an error (checked by the service).
type Executor interface {
	Execute(ctx context.Context, data *ExecuteData) (Output, error)
}

type ExecuteData struct {
	TaskID int64
	Agent  string
	Kind   string
	Input  string
	Media  []string
	Source Source
	// Version is the draft version this run makes: the task's latest version + 1.
	Version int
	// Prompt is the agent's input: the task, and for a revision the previous version and the trainer's text.
	Prompt string
	// Revision is the trainer's text of the revision this run makes; empty for the first version. An executor that
	// does not use Prompt (the editor) takes it from here.
	Revision string
	// LastAttempt: a failure of this run gives the task up (the attempts are exhausted), so the executor must not
	// leave work behind for a retry.
	LastAttempt bool
}

// Output is the draft a run made: its text and, for a video draft, the S3 key of the video (the text is then
// the video's description).
type Output struct {
	Text     string
	MediaKey string
}

// RoutingExecutor runs the tasks of the agents in its routes with their own executors (the editor's videos go to
// the media worker), the rest with the fallback (the agents' runtime).
type RoutingExecutor struct {
	fallback Executor
	routes   map[string]Executor
}

func NewRoutingExecutor(fallback Executor, routes map[string]Executor) *RoutingExecutor {
	return &RoutingExecutor{fallback: fallback, routes: maps.Clone(routes)}
}

func (e *RoutingExecutor) Execute(ctx context.Context, data *ExecuteData) (Output, error) {
	if executor, ok := e.routes[data.Agent]; ok {
		return executor.Execute(ctx, data)
	}

	return e.fallback.Execute(ctx, data)
}

type agentRunner interface {
	Run(ctx context.Context, data *agents.RunAgentData) (*agents.Result, error)
}

// AgentExecutor runs tasks with agents.Runtime.
type AgentExecutor struct {
	runner agentRunner
}

func NewAgentExecutor(runner agentRunner) *AgentExecutor {
	return &AgentExecutor{runner: runner}
}

func (e *AgentExecutor) Execute(ctx context.Context, data *ExecuteData) (Output, error) {
	result, err := e.runner.Run(ctx, &agents.RunAgentData{Agent: data.Agent, Input: data.Prompt})
	if err != nil {
		return Output{}, classifyAgentError(err)
	}

	return Output{Text: result.Text}, nil
}

// classifyAgentError marks errors that a retry cannot fix as permanent, with a message for the trainer.
func classifyAgentError(err error) error {
	var apiErr *anthropic.APIError

	switch {
	case errors.Is(err, agents.ErrRefusal):
		return &PermanentError{Message: "Агент отказался выполнять задачу. Переформулируйте запрос.", Err: err}
	case errors.Is(err, agents.ErrMaxTokens):
		return &PermanentError{Message: "Ответ агента не поместился в лимит длины. Сократите задачу или разбейте её на части.", Err: err}
	case errors.Is(err, agents.ErrIterationLimit):
		return &PermanentError{Message: "Агент не смог закончить работу: слишком много шагов.", Err: err}
	case errors.Is(err, agents.ErrAgentNotFound):
		return &PermanentError{Message: "Такого агента нет.", Err: err}
	case errors.Is(err, agents.ErrInvalidRunData):
		return &PermanentError{Message: "Пустая задача: напишите, что нужно сделать.", Err: err}
	case errors.Is(err, anthropic.ErrAPIKeyMissing):
		return &PermanentError{Message: "Не настроен ключ Claude API (ANTHROPIC_API_KEY).", Err: err}
	case errors.As(err, &apiErr) && !apiErr.Retryable():
		return &PermanentError{Message: claudeRejectedMessage(apiErr.StatusCode), Err: err}
	default:
		return fmt.Errorf("failed to run agent: %w", err)
	}
}

func claudeRejectedMessage(status int) string {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "Claude API не принял ключ: проверьте ANTHROPIC_API_KEY."
	}

	return fmt.Sprintf("Claude API отклонил запрос (HTTP %d).", status)
}
