package editor

import (
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/tasks"
	"github.com/Prokud1n/ai-agents/internal/teamchat"
)

// Module runs the editor's tasks through the media worker; it needs editjob.Module, editplan.Module, media.Module and
// tasks.Module.
// The editor's tasks go to Executor through a route of tasks.Executor (tasks.RoutesGroup), the rest to the agents as
// before.
var Module = fx.Module("editor",
	fx.Provide(NewExecutor),
	Route,
	fx.Provide(NewWaker, asBoard, asDoneListener, asStore, asDecider, asTaskWaker, asTechReporter, fx.Private),
	fx.Invoke(registerWaker, CheckBranding),
)

// Route sends the editor's tasks to Executor (a part of Module; internal.TestTaskRoutes checks it is in the group).
var Route = fx.Provide(fx.Annotate(newRoute, fx.ResultTags(tasks.RoutesGroup)))

// newRoute sends the editor's tasks to the media worker.
func newRoute(editor *Executor) tasks.Route {
	return tasks.Route{Agent: AgentName, Executor: editor}
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

func asStore(s *media.Store) store {
	return s
}

func asDecider(d *editplan.Decider) decider {
	return d
}

func asTechReporter(r *teamchat.TechReporter) techReporter {
	return r
}

func asTaskWaker(s *tasks.Service) taskWaker {
	return s
}
