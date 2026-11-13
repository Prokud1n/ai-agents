package health

import "go.uber.org/fx"

// Readiness checks are collected from the fx value group "readiness". Dependencies are registered
// there in one place, internal/readiness, so infrastructure packages do not import this one.
var Module = fx.Module("health",
	fx.Provide(NewHandler, fx.Private),
	fx.Invoke(RegisterRoutes),
)
