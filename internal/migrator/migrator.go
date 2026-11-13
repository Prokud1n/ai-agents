package migrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the "pgx5" database driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/migrations"
)

const (
	sourceName     = "iofs"
	databaseScheme = "pgx5"
	downOneStep    = -1
)

type Migrator struct {
	cfg *config.Postgres
	log *slog.Logger
}

func NewMigrator(cfg *config.Postgres, log *slog.Logger) *Migrator {
	return &Migrator{cfg: cfg, log: log}
}

// Up applies all pending migrations. golang-migrate takes a Postgres advisory lock,
// so several instances starting at once do not race.
func (m *Migrator) Up(ctx context.Context) error {
	return m.run(ctx, UpCommand, func(mg *migrate.Migrate) error {
		if err := mg.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			return fmt.Errorf("failed to apply migrations: %w", err)
		}

		return nil
	})
}

// Down rolls back exactly one migration.
func (m *Migrator) Down(ctx context.Context) error {
	return m.run(ctx, DownCommand, func(mg *migrate.Migrate) error {
		if err := mg.Steps(downOneStep); err != nil {
			return fmt.Errorf("failed to roll back migration: %w", err)
		}

		return nil
	})
}

// Run executes the given command.
func (m *Migrator) Run(ctx context.Context, cmd Command) error {
	if cmd == DownCommand {
		return m.Down(ctx)
	}

	return m.Up(ctx)
}

func (m *Migrator) run(ctx context.Context, cmd Command, action func(*migrate.Migrate) error) error {
	mg, err := m.newMigrate()
	if err != nil {
		return err
	}

	defer func() {
		sourceErr, databaseErr := mg.Close()
		if closeErr := errors.Join(sourceErr, databaseErr); closeErr != nil {
			m.log.ErrorContext(ctx, "migrator: failed to close", "error", closeErr)
		}
	}()

	// Stop between migrations when the start timeout expires or the app is interrupted;
	// the migration in progress is finished, so the schema is never left half-applied.
	done := make(chan struct{})
	defer close(done)

	go func() {
		select {
		case <-ctx.Done():
			mg.GracefulStop <- true
		case <-done:
		}
	}()

	if err := action(mg); err != nil {
		return err
	}

	version, dirty, err := mg.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return fmt.Errorf("failed to get migrations version: %w", err)
	}

	m.log.InfoContext(ctx, "migrator: done", "command", cmd, "version", version, "dirty", dirty)

	return nil
}

func (m *Migrator) newMigrate() (*migrate.Migrate, error) {
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to open migrations source: %w", err)
	}

	databaseURL, err := migrateURL(m.cfg.DSN())
	if err != nil {
		return nil, err
	}

	mg, err := migrate.NewWithSourceInstance(sourceName, source, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to init migrations: %w", err)
	}

	return mg, nil
}

// RunOnStart applies migrations on application start when enabled in config.
func RunOnStart(lc fx.Lifecycle, cfg *config.Migrations, m *Migrator) {
	if !cfg.RunOnStart {
		return
	}

	lc.Append(fx.StartHook(m.Up))
}

// RunAndShutdown runs the migrate command and stops the application: used by the standalone migrate binary.
func RunAndShutdown(lc fx.Lifecycle, cmd Command, m *Migrator, shutdowner fx.Shutdowner) {
	lc.Append(fx.StartHook(func(ctx context.Context) error {
		if err := m.Run(ctx, cmd); err != nil {
			return err
		}

		if err := shutdowner.Shutdown(); err != nil {
			return fmt.Errorf("failed to shut down after migrations: %w", err)
		}

		return nil
	}))
}

// migrateURL turns a postgres DSN into a URL for the golang-migrate pgx/v5 driver.
func migrateURL(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("failed to parse postgres dsn: %w", err)
	}

	u.Scheme = databaseScheme

	return u.String(), nil
}
