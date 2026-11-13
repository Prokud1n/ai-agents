package llmusage

import "go.uber.org/fx"

// Module accounts Claude API usage: tokens and cost of every call, summaries by agent and model.
var Module = fx.Module("llmusage",
	fx.Provide(newRepo, fx.Private),
	fx.Provide(NewService),
)
