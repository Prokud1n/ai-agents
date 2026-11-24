package editplan

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Prokud1n/ai-agents/internal/agents"
)

type agentRunner interface {
	Run(ctx context.Context, data *agents.RunAgentData) (*agents.Result, error)
}

// Decider asks the editor's agent (agents/editor, through agents.Runtime: the calls are counted in llm_usage) to
// decide on the request and returns its checked decision.
type Decider struct {
	runner agentRunner
	log    *slog.Logger
}

func NewDecider(runner agentRunner, log *slog.Logger) *Decider {
	return &Decider{runner: runner, log: log}
}

// Decide runs the agent once. A decision made before the run failed (the model went on after submit_edit) counts.
// A run that fails without a decision returns the agents' or the API's error; a run that ends without calling
// submit_edit is NoAnswerOutcome.
func (d *Decider) Decide(ctx context.Context, req *Request) (*Outcome, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}

	session := NewSession(req)

	result, err := d.runner.Run(WithSession(ctx, session), &agents.RunAgentData{Agent: AgentName, Input: Prompt(req)})

	if outcome, ok := session.Outcome(); ok {
		d.log.InfoContext(ctx, "editplan: decided", "task_id", req.TaskID, "version", req.Version,
			"kind", outcome.Kind, "reason", outcome.Reason, "changes", len(outcome.Changes), "cut", outcome.Cut,
			"kept", outcome.Kept, "duration", outcome.Duration, "run_error", err)

		return outcome, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to decide the edit: %w", err)
	}

	d.log.WarnContext(ctx, "editplan: the model answered without a decision", "task_id", req.TaskID,
		"version", req.Version, "answer", oneLine(result.Text, maxReasonRunes))

	return &Outcome{Kind: NoAnswerOutcome, Reason: oneLine(result.Text, maxReasonRunes)}, nil
}
