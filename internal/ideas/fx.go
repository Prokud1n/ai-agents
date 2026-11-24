package ideas

import (
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

// ToolModule adds submit_idea to the agent tools (agents.ToolsGroup). It goes into every build with the agents (the
// app and cmd/agent-run): agents/marketer lists the tool, and an unknown tool fails the start.
var ToolModule = fx.Module("ideas-tool",
	fx.Provide(fx.Annotate(NewSubmitTool, fx.As(new(agents.Tool)), fx.ResultTags(agents.ToolsGroup))),
)

// Module provides the backlog (Service) and routes the marketer's idea tasks to Executor (tasks.RoutesGroup); it
// needs postgres, agents.Module and ToolModule.
var Module = fx.Module("ideas",
	fx.Provide(NewService),
	Route,
	fx.Provide(newRepo, NewExecutor, asAgentRunner, asCardStore, fx.Private),
)

// Route sends the marketer's idea tasks to Executor (a part of Module; internal.TestTaskRoutes builds Module and
// checks the group).
var Route = fx.Provide(fx.Annotate(newRoute, fx.ResultTags(tasks.RoutesGroup)))

// newRoute sends the marketer's tasks of TaskKind to Executor.
func newRoute(e *Executor) tasks.Route {
	return tasks.Route{Agent: AgentName, Kind: TaskKind, Executor: e}
}

func asAgentRunner(r *agents.Runtime) agentRunner {
	return r
}

func asCardStore(s *Service) cardStore {
	return s
}
