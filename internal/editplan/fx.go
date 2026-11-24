package editplan

import (
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/agents"
)

// Module adds submit_edit to the agent tools (agents.ToolsGroup) and provides the Decider; it needs agents.Module.
// It goes into every build with the agents (the app and cmd/agent-run): agents/editor lists the tool, and an unknown
// tool fails the start.
var Module = fx.Module("editplan",
	fx.Provide(asAgentRunner, fx.Private),
	fx.Provide(
		fx.Annotate(NewSubmitTool, fx.As(new(agents.Tool)), fx.ResultTags(agents.ToolsGroup)),
		NewDecider,
	),
)

func asAgentRunner(r *agents.Runtime) agentRunner {
	return r
}
