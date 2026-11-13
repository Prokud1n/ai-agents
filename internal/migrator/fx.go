package migrator

import "go.uber.org/fx"

// Module applies migrations on application start (if enabled in config).
var Module = fx.Module("migrator",
	fx.Provide(NewMigrator),
	fx.Invoke(RunOnStart),
)

// CommandModule applies migrations and shuts the application down: used by cmd/migrate.
var CommandModule = fx.Module("migrator",
	fx.Provide(NewMigrator),
	fx.Invoke(RunAndShutdown),
)
