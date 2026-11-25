// Command subtitles makes Reels subtitles in the brand style from a transcript (the JSON of cmd/transcribe):
//
//	subtitles -in video.mp4 -transcript result.json -out reel.mp4 [-style style.yaml] [-accents "присед,3 подхода"]
//	subtitles -transcript result.json -out subtitles.ass -ass-only
//
// Rendering needs ffmpeg with libass in PATH and the fonts in SUBTITLES_FONTS_DIR; -ass-only needs neither.
//
// Exit codes: 0 done, 1 error, 2 the video is written but libass replaced a brand font.
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
	"path/filepath"
	"strings"
	"syscall"

	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/subtitles"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	outputFileMode = 0o600
	// exitFontFallback: the video is written, but libass replaced a brand font. Other errors exit with 1.
	exitFontFallback = 2
)

var (
	errNoTranscript  = errors.New("-transcript is required")
	errNoOutput      = errors.New("-out is required")
	errNoInput       = errors.New("-in is required unless -ass-only")
	errFontFallbacks = errors.New("brand fonts were not found, the video uses other fonts")
)

type options struct {
	in         string
	transcript string
	out        string
	style      string
	accents    []string
	assOnly    bool
	karaoke    bool
	hook       string
	fit        ffmpeg.FitMode
}

func main() {
	err := run(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "subtitles:", err)

		if errors.Is(err, errFontFallbacks) {
			os.Exit(exitFontFallback)
		}

		os.Exit(1)
	}
}

func run(args []string) error {
	opts, err := parseFlags(args, os.Stderr)
	if err != nil {
		return err
	}

	data, err := loadInput(&opts)
	if err != nil {
		return err
	}

	if len(data.Transcript.Words) == 0 {
		fmt.Fprintln(os.Stderr, "subtitles: warning: the transcript has no words, the output has no subtitles")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var svc *subtitles.Service

	// Only the dependency graph is built; app.Start is not called (see DeclareSubtitlesOptions).
	app := fx.New(append(internal.DeclareSubtitlesOptions(), fx.Populate(&svc))...)
	if err := app.Err(); err != nil {
		return fmt.Errorf("failed to build application: %w", err)
	}

	if opts.assOnly {
		result, err := svc.CreateSubtitles(&data)
		if err != nil {
			return err
		}

		return writeFile(opts.out, result.ASS)
	}

	result, err := svc.BurnSubtitles(ctx, &subtitles.BurnSubtitlesData{
		CreateSubtitlesData: data, VideoPath: opts.in, OutputPath: opts.out, Fit: opts.fit,
	})
	if err != nil {
		return err
	}

	if len(result.FontFallbacks) > 0 {
		return fmt.Errorf("%s written, but %w: %s (check SUBTITLES_FONTS_DIR and the font names in the style)",
			opts.out, errFontFallbacks, strings.Join(result.FontFallbacks, "; "))
	}

	return nil
}

func parseFlags(args []string, output io.Writer) (options, error) {
	var (
		opts    options
		accents string
		fit     string
	)

	fs := flag.NewFlagSet("subtitles", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&opts.in, "in", "", "input video (required unless -ass-only)")
	fs.StringVar(&opts.transcript, "transcript", "", "transcript JSON from transcribe (required)")
	fs.StringVar(&opts.out, "out", "", "output: MP4 video, or the .ass file with -ass-only (required)")
	fs.StringVar(&opts.style, "style", "", "YAML style file (default SUBTITLES_STYLE_FILE or the built-in style)")
	fs.BoolVar(&opts.assOnly, "ass-only", false, "write only the ASS subtitles, do not render the video")
	fs.StringVar(&accents, "accents", "", "comma-separated words or phrases to accent instead of the automatic choice")
	fs.BoolVar(&opts.karaoke, "karaoke", false, "highlight the word being spoken")
	fs.StringVar(&opts.hook, "hook", "", "hook title over the first seconds (at most 40 characters, plain text)")
	fs.StringVar(&fit, "fit", string(ffmpeg.PadFitMode), "video of another size: pad (keep the whole frame) or crop (fill)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: subtitles -in video.mp4 -transcript result.json -out reel.mp4 [flags]")
		fmt.Fprintln(fs.Output(), "       subtitles -transcript result.json -out subtitles.ass -ass-only [flags]")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return options{}, fmt.Errorf("failed to parse flags: %w", err)
	}

	opts.accents = splitAccents(accents)
	opts.fit = ffmpeg.FitMode(fit)

	var missing error

	switch {
	case opts.transcript == "":
		missing = errNoTranscript
	case opts.out == "":
		missing = errNoOutput
	case opts.in == "" && !opts.assOnly:
		missing = errNoInput
	}

	if missing != nil {
		fs.Usage()

		return options{}, fmt.Errorf("failed to parse flags: %w", missing)
	}

	return opts, nil
}

func splitAccents(s string) []string {
	var accents []string

	for a := range strings.SplitSeq(s, ",") {
		if a = strings.TrimSpace(a); a != "" {
			accents = append(accents, a)
		}
	}

	return accents
}

func loadInput(opts *options) (subtitles.CreateSubtitlesData, error) {
	content, err := os.ReadFile(filepath.Clean(opts.transcript))
	if err != nil {
		return subtitles.CreateSubtitlesData{}, fmt.Errorf("failed to read transcript: %w", err)
	}

	var transcript transcription.Result
	if err := json.Unmarshal(content, &transcript); err != nil {
		return subtitles.CreateSubtitlesData{}, fmt.Errorf("failed to parse transcript %s: %w", opts.transcript, err)
	}

	data := subtitles.CreateSubtitlesData{Transcript: &transcript, Accents: opts.accents, Karaoke: opts.karaoke,
		Hook: opts.hook}

	if opts.style != "" {
		style, err := subtitles.LoadStyle(opts.style)
		if err != nil {
			return subtitles.CreateSubtitlesData{}, err
		}

		data.Style = &style
	}

	return data, nil
}

func writeFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, outputFileMode); err != nil {
		return fmt.Errorf("failed to write subtitles: %w", err)
	}

	return nil
}
