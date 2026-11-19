package agents

import (
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/llmusage"
	"github.com/Prokud1n/ai-agents/internal/providers/anthropic"
)

// Module loads agents from AGENTS_DIR on start and provides the Runtime.
// Tools come from the value group ToolsGroup.
var Module = fx.Module(moduleName,
	fx.Provide(asMessageCreator, asUsageRecorder, asPriceLookup, fx.Private),
	fx.Provide(
		fx.Annotate(NewRegistry, fx.ParamTags("", "", "", "", ToolsGroup)),
		NewRuntime,
	),
	// Load and validate agents on start even before anything uses them: a broken agents/ fails the start.
	fx.Invoke(func(*Registry) {}),
)

func asMessageCreator(c *anthropic.Client) messageCreator {
	return c
}

func asUsageRecorder(s *llmusage.Service) usageRecorder {
	return s
}

func asPriceLookup(s *llmusage.Service) priceLookup {
	return s
}
