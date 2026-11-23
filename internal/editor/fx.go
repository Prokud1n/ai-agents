package editor

import (
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

// Module runs the editor's tasks through the media worker; it needs editjob.Module, media.Module and tasks.Module.
// tasks.Executor is decorated: the editor's tasks go to Executor, the rest to the agents as before.
//
// The decoration is outside fx.Module on purpose (as in teamchat): a decorator applies to its own scope and the scopes
// below it, so only at the level of the application options does it reach tasks.Module.
var Module = fx.Options(
	fx.Module("editor",
		// Executor is visible to the application: the decorator of tasks.Executor below takes it.
		fx.Provide(NewExecutor),
		fx.Provide(NewWaker, asBoard, asDoneListener, asResultKeys, asTaskWaker, fx.Private),
		fx.Invoke(registerWaker),
	),
	fx.Decorate(routeExecutor),
)

// routeExecutor sends the editor's tasks to the media worker.
func routeExecutor(base tasks.Executor, editor *Executor) tasks.Executor {
	return tasks.NewRoutingExecutor(base, map[string]tasks.Executor{AgentName: editor})
}

// registerWaker: the waker is constructed after the task service and stops before it.
func registerWaker(lc fx.Lifecycle, w *Waker) {
	lc.Append(fx.Hook{OnStart: w.Start, OnStop: w.Stop})
}

func asBoard(b *editjob.Board) board {
	return b
}

func asDoneListener(b *editjob.Board) doneListener {
	return b
}

func asResultKeys(s *media.Store) resultKeys {
	return s
}

func asTaskWaker(s *tasks.Service) taskWaker {
	return s
}
