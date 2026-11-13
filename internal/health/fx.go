package health

import "go.uber.org/fx"

var Module = fx.Module("health",
	fx.Provide(NewHandler, fx.Private),
	fx.Invoke(RegisterRoutes),
)
