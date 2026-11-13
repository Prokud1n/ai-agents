package migrator

import "go.uber.org/fx"

// Module applies migrations on application start (if enabled in config).
var Module = fx.Module("migrator",
	fx.Provide(NewMigrator, fx.Private),
	fx.Invoke(RunOnStart),
)

// CommandModule runs the migrate command given via fx.Supply(Command) and shuts the application down:
// used by cmd/migrate.
var CommandModule = fx.Module("migrator",
	fx.Provide(NewMigrator, fx.Private),
	fx.Invoke(RunAndShutdown),
)
