// Command transcribe extracts speech from a video and prints its transcript with word timestamps as JSON:
//
//	transcribe -in video.mp4 [-lang ru] [-out result.json]
//
// It needs ffmpeg and ffprobe in PATH and OPENAI_API_KEY in the environment.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const outputFileMode = 0o600

var errNoInput = errors.New("-in is required")

type options struct {
	in   string
	lang string
	out  string
}

func main() {
	err := run(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "transcribe:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	opts, err := parseFlags(args, os.Stderr)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var svc *transcription.Service

	// Only the dependency graph is built; app.Start is not called (see DeclareTranscribeOptions).
	app := fx.New(append(internal.DeclareTranscribeOptions(), fx.Populate(&svc))...)
	if err := app.Err(); err != nil {
		return fmt.Errorf("failed to build application: %w", err)
	}

	result, err := svc.Transcribe(ctx, transcription.TranscribeData{Path: opts.in, Language: opts.lang})
	if err != nil {
		return err
	}

	return writeResult(result, opts.out, os.Stdout)
}

func parseFlags(args []string, output io.Writer) (options, error) {
	var opts options

	fs := flag.NewFlagSet("transcribe", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&opts.in, "in", "", "input video or audio file (required)")
	fs.StringVar(&opts.lang, "lang", "", "speech language, ISO 639-1 (default WHISPER_LANGUAGE, ru)")
	fs.StringVar(&opts.out, "out", "", "output JSON file (default stdout)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: transcribe -in video.mp4 [-lang ru] [-out result.json]")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return options{}, fmt.Errorf("failed to parse flags: %w", err)
	}

	if opts.in == "" {
		fs.Usage()

		return options{}, fmt.Errorf("failed to parse flags: %w", errNoInput)
	}

	return opts, nil
}

func writeResult(result *transcription.Result, path string, stdout io.Writer) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode result: %w", err)
	}

	data = append(data, '\n')

	if path == "" {
		if _, err := stdout.Write(data); err != nil {
			return fmt.Errorf("failed to write result: %w", err)
		}

		return nil
	}

	if err := os.WriteFile(path, data, outputFileMode); err != nil {
		return fmt.Errorf("failed to write result: %w", err)
	}

	return nil
}
