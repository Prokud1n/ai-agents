package health

import "go.uber.org/fx"

// Readiness checks are collected from the fx value group "readiness": a module registers its
// dependency with fx.Provide(fx.Annotate(asChecker, fx.ResultTags(`group:"readiness"`))),
// where asChecker returns health.Checker.
var Module = fx.Module("health",
	fx.Provide(NewHandler, fx.Private),
	fx.Invoke(RegisterRoutes),
)
