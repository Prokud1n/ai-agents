package editlocal

import (
	"context"
	"log/slog"
	"os"

	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editor"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/llmusage"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
)

// Module provides the Runner of local edits: the editor's executor of the app with the editor's model (its usage
// counted by UsageMeter) and a «Техничка» that only logs. It needs redis, media, editjob, agents and llmusage, and
// none of the gateway, the task queue or teamchat: nothing is sent to the group.
var Module = fx.Module("editlocal",
	fx.Provide(newUsageMeter, newSequence, newExecutor, fx.Private),
	fx.Provide(newRunner),
)

func newUsageMeter(runtime *agents.Runtime, usage *llmusage.Service) *UsageMeter {
	return NewUsageMeter(runtime, usage)
}

func newSequence(client *redis.Client) *RedisSequence {
	return NewRedisSequence(client, SequenceKey)
}

func newExecutor(
	board *editjob.Board,
	store *media.Store,
	meter *UsageMeter,
	cfg *config.Editor,
	log *slog.Logger,
) *editor.Executor {
	return editor.NewExecutor(board, store, editplan.NewDecider(meter, log), techLog{log: log}, cfg, log)
}

type runnerParams struct {
	fx.In

	Executor *editor.Executor
	Board    *editjob.Board
	Store    *media.Store
	Sequence *RedisSequence
	Meter    *UsageMeter
	Telegram *config.Telegram
	Tasks    *config.Tasks
	Editor   *config.Editor
	Log      *slog.Logger
}

func newRunner(p runnerParams) *Runner {
	return NewRunner(&Params{
		Executor: p.Executor, Board: p.Board, Store: p.Store, Sequence: p.Sequence, Meter: p.Meter,
		Settings: Settings{
			MaxFileSize: p.Telegram.MaxFileSize, MaxAttempts: p.Tasks.MaxAttempts,
			RetryBaseDelay: p.Tasks.RetryBaseDelay, RetryMaxDelay: p.Tasks.RetryMaxDelay, RunTimeout: p.Tasks.Timeout,
			LockStaleAfter: p.Editor.JobTimeout,
		},
		Log: p.Log, Progress: os.Stderr,
	})
}

// techLog is «Техничка» of a local edit: the team's report goes to the log, nothing to the group.
type techLog struct {
	log *slog.Logger
}

func (t techLog) Report(ctx context.Context, agent, text string) error {
	t.log.WarnContext(ctx, "editlocal: report to «Техничка» (not sent)", "agent", agent, "text", text)

	return nil
}
