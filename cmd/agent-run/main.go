// Command agent-run runs one agent with a prompt and prints the answer and its usage:
//
//	agent-run -agent _example -prompt "Привет!"
//
// It is for manual checks with a real ANTHROPIC_API_KEY; usage is recorded to PostgreSQL like in the app.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal"
	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/llmusage"
)

const failureExitCode = 1

var errUsage = errors.New("usage: agent-run -agent <name> -prompt <text> [-model <model>]")

type options struct {
	agent  string
	prompt string
	model  string
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(failureExitCode)
	}
}

func run(args []string, out io.Writer) error {
	opts, err := parseFlags(args)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var (
		runtime *agents.Runtime
		usage   *llmusage.Service
	)

	app := fx.New(append(internal.DeclareAgentRunOptions(), fx.Populate(&runtime, &usage))...)
	if err := app.Start(ctx); err != nil {
		return fmt.Errorf("failed to start: %w", err)
	}

	defer func() {
		if err := app.Stop(context.WithoutCancel(ctx)); err != nil {
			fmt.Fprintln(os.Stderr, "failed to stop:", err)
		}
	}()

	result, err := runtime.Run(ctx, &agents.RunAgentData{Agent: opts.agent, Input: opts.prompt, Model: opts.model})
	if err != nil {
		return fmt.Errorf("failed to run agent: %w", err)
	}

	printResult(out, result, usage)

	return nil
}

func parseFlags(args []string) (options, error) {
	var opts options

	flags := flag.NewFlagSet("agent-run", flag.ContinueOnError)
	flags.StringVar(&opts.agent, "agent", "", "agent name (directory in AGENTS_DIR)")
	flags.StringVar(&opts.prompt, "prompt", "", "user message")
	flags.StringVar(&opts.model, "model", "", "model override (default: agent model or LLM_DEFAULT_MODEL)")

	if err := flags.Parse(args); err != nil {
		return options{}, fmt.Errorf("failed to parse flags: %w", err)
	}

	if opts.agent == "" || opts.prompt == "" {
		return options{}, errUsage
	}

	return opts, nil
}

func printResult(out io.Writer, result *agents.Result, usage *llmusage.Service) {
	cost := "unknown (no price for the model in LLM_PRICES)"
	if nano, ok := usage.Cost(result.Model, result.Tokens); ok {
		cost = "$" + llmusage.FormatUSD(nano)
	}

	fmt.Fprintln(out, result.Text)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "--- usage ---")
	fmt.Fprintf(out, "model: %s, stop reason: %s, api calls: %d\n", result.Model, result.StopReason, result.Calls)
	fmt.Fprintf(out, "tokens: input %d, output %d, cache read %d, cache write %d\n",
		result.Tokens.Input, result.Tokens.Output, result.Tokens.CacheRead, result.Tokens.CacheWrite)
	fmt.Fprintf(out, "cost: %s\n", cost)
}
