package tasks

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/providers/anthropic"
)

// Executor runs an agent on a prompt and returns its answer. A *PermanentError gives the run up at once;
// any other error is retried. An empty answer is an error (checked by the service).
type Executor interface {
	Execute(ctx context.Context, data *ExecuteData) (string, error)
}

type ExecuteData struct {
	TaskID int64
	Agent  string
	Prompt string
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

func (e *AgentExecutor) Execute(ctx context.Context, data *ExecuteData) (string, error) {
	result, err := e.runner.Run(ctx, &agents.RunAgentData{Agent: data.Agent, Input: data.Prompt})
	if err != nil {
		return "", classifyAgentError(err)
	}

	return result.Text, nil
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
