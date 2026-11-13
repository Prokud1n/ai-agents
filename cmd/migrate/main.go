// Command migrate runs database migrations and exits: "migrate up" applies all pending ones,
// "migrate down" rolls back one.
//
// It deliberately runs outside the fx lifecycle: fx kills the process with os.Exit when its start
// timeout expires, which would interrupt a long migration and leave the database "dirty".
// Here migrations have no timeout; SIGINT/SIGTERM stop them gracefully after the current migration.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/logger"
	"github.com/Prokud1n/ai-agents/internal/migrator"
)

const failureExitCode = 1

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	// After the first signal restore the default handling: a second Ctrl+C kills the process
	// at once, for the case when a migration hangs and waiting is worse than a dirty database.
	context.AfterFunc(ctx, stop)

	err := run(ctx, os.Args[1:])

	stop()

	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(failureExitCode)
	}
}

func run(ctx context.Context, args []string) error {
	cmd, err := migrator.ParseCommand(args)
	if err != nil {
		return err
	}

	cfg, err := config.ParseMigrate()
	if err != nil {
		return err
	}

	log, err := logger.New(cfg.Logging)
	if err != nil {
		return err
	}

	if err := migrator.NewMigrator(&cfg.Postgres, log).Run(ctx, cmd); err != nil {
		return fmt.Errorf("failed to run migrate %s: %w", cmd, err)
	}

	return nil
}
