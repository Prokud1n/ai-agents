package ideas

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

// noCardText is shown to the trainer when the marketer could not make a valid card in all the attempts.
const noCardText = "Маркетолог не смог оформить карточку идеи. Отправьте идею ещё раз командой /idea."

type agentRunner interface {
	Run(ctx context.Context, data *agents.RunAgentData) (*agents.Result, error)
}

type cardStore interface {
	SaveCard(ctx context.Context, taskID int64, version int, card *Card) error
	GetCard(ctx context.Context, taskID int64, version int) (Card, error)
}

// Executor makes the card of an idea task (tasks.Executor for the marketer's tasks of TaskKind): the agent answers
// with submit_idea, the card is stored for the version, and the draft is the card's text.
type Executor struct {
	runner agentRunner
	cards  cardStore
	log    *slog.Logger
}

func NewExecutor(runner agentRunner, cards cardStore, log *slog.Logger) *Executor {
	return &Executor{runner: runner, cards: cards, log: log}
}

func (e *Executor) Execute(ctx context.Context, data *tasks.ExecuteData) (tasks.Output, error) {
	req := &Request{Idea: data.Input, Revision: data.Revision}

	if data.Version > 1 {
		previous, err := e.cards.GetCard(ctx, data.TaskID, data.Version-1)
		if err != nil {
			return tasks.Output{}, fmt.Errorf("failed to get the previous idea card: %w", err)
		}

		req.Previous = &previous
	}

	session := NewSession()
	result, runErr := e.runner.Run(WithSession(ctx, session), &agents.RunAgentData{Agent: data.Agent, Input: Prompt(req)})

	card, ok := session.Card()
	if !ok {
		return tasks.Output{}, e.noCard(ctx, data, result, runErr)
	}

	if err := e.cards.SaveCard(ctx, data.TaskID, data.Version, &card); err != nil {
		return tasks.Output{}, fmt.Errorf("failed to save the idea card: %w", err)
	}

	e.log.InfoContext(ctx, "ideas: card made", "task_id", data.TaskID, "version", data.Version,
		"format", card.Format, "run_error", runErr)

	return tasks.Output{Text: FormatCard(&card)}, nil
}

// noCard: a failed run is classified as any agent run; a run that ended without a valid card is retried, and its
// last failure tells the trainer to send the idea again.
func (e *Executor) noCard(ctx context.Context, data *tasks.ExecuteData, result *agents.Result, runErr error) error {
	if runErr != nil {
		return tasks.ClassifyAgentError(runErr)
	}

	answer := ""
	if result != nil {
		answer = result.Text
	}

	e.log.WarnContext(ctx, "ideas: the model answered without a card", "task_id", data.TaskID,
		"version", data.Version, "answer_chars", len([]rune(answer)))

	return &tasks.TemporaryError{Message: noCardText, Err: ErrNoCard}
}
