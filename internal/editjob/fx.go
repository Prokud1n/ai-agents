package editjob

import "go.uber.org/fx"

// Module provides the Board; it needs redis.Module and *config.Editor.
var Module = fx.Module("editjob",
	fx.Provide(NewBoard),
)
