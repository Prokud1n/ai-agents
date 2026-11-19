package internal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/kbcli"
	"github.com/Prokud1n/ai-agents/internal/knowledge"
	"github.com/Prokud1n/ai-agents/internal/providers/embeddings"
	"github.com/Prokud1n/ai-agents/internal/providers/postgres"
)

// Exit codes of the kb command.
const (
	KBExitOK    = 0
	KBExitError = 1
	KBExitUsage = 2
)

// DeclareKBOptions builds the kb command: knowledge base service over PostgreSQL and embeddings.
func DeclareKBOptions() []fx.Option {
	return []fx.Option{
		kbcli.Module,
		fx.WithLogger(func(log *slog.Logger) fxevent.Logger {
			logger := &fxevent.SlogLogger{Logger: log}
			logger.UseLogLevel(slog.LevelDebug)

			return logger
		}),
		postgres.Module,
		embeddings.Module,
		knowledge.Module,
	}
}

// RunKB runs the kb command with arguments without the program name and returns the exit code.
// The command runs between fx start and stop, not in a start hook, so a long ingest is not limited
// by APP_START_TIMEOUT.
func RunKB(args []string) int {
	cmd, err := kbcli.ParseCommand(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kb: %v\n\n%s\n", err, kbcli.Usage)

		return KBExitUsage
	}

	lifecycle, err := config.ParseLifecycle()
	if err != nil {
		fmt.Fprintf(os.Stderr, "kb: %v\n", err)

		return KBExitError
	}

	var runner *kbcli.Runner

	app := fx.New(append(DeclareKBOptions(), fx.Populate(&runner))...)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startCtx, cancelStart := context.WithTimeout(ctx, lifecycle.StartTimeout)
	defer cancelStart()

	if err := app.Start(startCtx); err != nil {
		fmt.Fprintf(os.Stderr, "kb: failed to start: %v\n", err)

		return KBExitError
	}

	runErr := runner.Run(ctx, &cmd)

	stopCtx, cancelStop := context.WithTimeout(context.Background(), lifecycle.StopTimeout)
	defer cancelStop()

	if err := app.Stop(stopCtx); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("failed to stop: %w", err))
	}

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "kb: %v\n", runErr)

		return KBExitError
	}

	return KBExitOK
}
