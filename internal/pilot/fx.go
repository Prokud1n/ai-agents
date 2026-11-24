package pilot

import "go.uber.org/fx"

// Module provides the pilot Service; it needs postgres.
var Module = fx.Module("pilot",
	fx.Provide(NewService),
	fx.Provide(newRepo, fx.Private),
)
