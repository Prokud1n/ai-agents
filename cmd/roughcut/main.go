// Command roughcut makes a rough cut of a talking-head video: it removes pauses, filler sounds and failed
// takes by the transcript and renders the rest:
//
//	roughcut -in video.mp4 [-transcript transcript.json] [-out roughcut] [-plan-only] [-plan plan.json] [-accept 0,2]
//
// The output directory gets plan.json (edit decision list), decisions.log (why takes were removed),
// transcript.json (the transcript on the new timeline) and roughcut.mp4. Without -transcript the video is
// transcribed first (needs OPENAI_API_KEY); the source transcript is saved as source-transcript.json.
// It needs ffmpeg and ffprobe in PATH.
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
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	defaultOutDir        = "roughcut"
	sourceTranscriptFile = "source-transcript.json"
	outputDirMode        = 0o750
)

var (
	errNoInput           = errors.New("-in is required")
	errInvalidSimilarity = errors.New("-similarity must be within 0..1")
	errNegativePause     = errors.New("-max-pause must not be negative")
	errPlanNoTranscript  = errors.New("-plan needs -transcript: the transcript of the source is remapped by the plan")
	errAcceptWithPlan    = errors.New("-accept rebuilds the plan, it cannot be used with -plan")
	errBadAccept         = errors.New("-accept takes candidate IDs separated by commas: 0,2")
)

type options struct {
	in         string
	transcript string
	plan       string
	out        string
	lang       string
	planOnly   bool
	// Overrides of the config; zero values (-1 for similarity) keep it.
	maxPause   time.Duration
	similarity float64
	fillers    string
	accept     []int
}

func main() {
	err := run(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "roughcut:", err)
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

	var (
		cutter      *roughcut.Service
		transcriber *transcription.Service
	)

	// Only the dependency graph is built; app.Start is not called (see DeclareRoughCutOptions).
	app := fx.New(append(internal.DeclareRoughCutOptions(), fx.Populate(&cutter, &transcriber))...)
	if err := app.Err(); err != nil {
		return fmt.Errorf("failed to build application: %w", err)
	}

	data, err := prepare(ctx, &opts, cutter, transcriber)
	if err != nil {
		return err
	}

	result, err := cutter.Cut(ctx, &data)
	if err != nil {
		return err
	}

	return printSummary(os.Stdout, result)
}

// prepare reads or makes the transcript, reads an edited plan and applies the threshold flags.
func prepare(
	ctx context.Context,
	opts *options,
	cutter *roughcut.Service,
	transcriber *transcription.Service,
) (roughcut.CutData, error) {
	data := roughcut.CutData{Source: opts.in, OutDir: opts.out, PlanOnly: opts.planOnly}

	if opts.transcript != "" {
		data.Transcript = new(transcription.Result)
		if err := readJSON(opts.transcript, data.Transcript); err != nil {
			return roughcut.CutData{}, err
		}
	} else {
		result, err := transcribe(ctx, transcriber, opts)
		if err != nil {
			return roughcut.CutData{}, err
		}

		data.Transcript = result
	}

	if opts.plan != "" {
		data.Plan = new(roughcut.Plan)
		if err := readJSON(opts.plan, data.Plan); err != nil {
			return roughcut.CutData{}, err
		}
	}

	settings := applyOverrides(cutter.DefaultSettings(), opts)
	data.Settings = &settings
	data.AcceptCandidates = opts.accept

	return data, nil
}

// transcribe runs T3.1 on the video and saves the result next to the cut, so a re-run with other
// thresholds can take it with -transcript instead of paying for Whisper again.
func transcribe(ctx context.Context, svc *transcription.Service, opts *options) (*transcription.Result, error) {
	result, err := svc.Transcribe(ctx, transcription.TranscribeData{Path: opts.in, Language: opts.lang})
	if err != nil {
		return nil, fmt.Errorf("failed to transcribe: %w", err)
	}

	if err := os.MkdirAll(opts.out, outputDirMode); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	if err := roughcut.WriteJSON(filepath.Join(opts.out, sourceTranscriptFile), result); err != nil {
		return nil, err
	}

	return result, nil
}

func applyOverrides(s roughcut.Settings, opts *options) roughcut.Settings {
	out := s

	if opts.maxPause > 0 {
		out.MaxPause = opts.maxPause.Seconds()
	}

	if opts.similarity >= 0 {
		out.RetakeSimilarity = opts.similarity
	}

	if opts.fillers != "" {
		out.Fillers = splitList(opts.fillers)
	}

	return out
}

func splitList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))

	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}

	return out
}

func parseIDs(s string) ([]int, error) {
	parts := splitList(s)
	if len(parts) == 0 {
		return nil, nil
	}

	ids := make([]int, 0, len(parts))

	for _, p := range parts {
		id, err := strconv.Atoi(p)
		if err != nil || id < 0 {
			return nil, errBadAccept
		}

		ids = append(ids, id)
	}

	return ids, nil
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", path, err)
	}

	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("failed to parse %s: %w", path, err)
	}

	return nil
}

func parseFlags(args []string, output io.Writer) (options, error) {
	opts := options{similarity: -1}
	acceptIDs := ""

	fs := flag.NewFlagSet("roughcut", flag.ContinueOnError)
	fs.SetOutput(output)
	fs.StringVar(&opts.in, "in", "", "source video (required)")
	fs.StringVar(&opts.transcript, "transcript", "", "transcript JSON from transcribe (default: transcribe the video)")
	fs.StringVar(&opts.plan, "plan", "", "render this (edited) plan.json instead of building a new plan")
	fs.StringVar(&opts.out, "out", defaultOutDir, "output directory")
	fs.StringVar(&opts.lang, "lang", "", "speech language for transcription, ISO 639-1 (default WHISPER_LANGUAGE)")
	fs.BoolVar(&opts.planOnly, "plan-only", false, "write the plan, transcript and log without rendering the video")
	fs.DurationVar(&opts.maxPause, "max-pause", 0, "longest pause kept between words (default ROUGHCUT_MAX_PAUSE, 300ms)")
	fs.Float64Var(&opts.similarity, "similarity", -1,
		"share of words (0..1) repeated for a phrase to be a failed take (default ROUGHCUT_RETAKE_SIMILARITY, 0.7)")
	fs.StringVar(&opts.fillers, "fillers", "", "comma-separated fillers to cut (default ROUGHCUT_FILLERS)")
	fs.StringVar(&acceptIDs, "accept", "",
		"comma-separated IDs of plan.json candidates to cut as well (same transcript and thresholds)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(),
			"usage: roughcut -in video.mp4 [-transcript transcript.json] [-out dir] [-plan-only] [-plan plan.json]")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return options{}, fmt.Errorf("failed to parse flags: %w", err)
	}

	ids, err := parseIDs(acceptIDs)
	if err == nil {
		opts.accept = ids
		err = validateOptions(&opts)
	}

	if err != nil {
		fs.Usage()

		return options{}, fmt.Errorf("failed to parse flags: %w", err)
	}

	return opts, nil
}

func validateOptions(opts *options) error {
	switch {
	case opts.in == "":
		return errNoInput
	case opts.maxPause < 0:
		return errNegativePause
	case opts.plan != "" && opts.transcript == "":
		return errPlanNoTranscript
	case opts.plan != "" && len(opts.accept) > 0:
		return errAcceptWithPlan
	case opts.similarity > 1 || (opts.similarity < 0 && opts.similarity != -1):
		return errInvalidSimilarity
	}

	return nil
}

func printSummary(w io.Writer, r *roughcut.Result) error {
	var b strings.Builder

	b.WriteString(r.Log)
	b.WriteString("\nФайлы:\n")

	for _, f := range []string{r.Files.Plan, r.Files.Log, r.Files.Transcript, r.Files.Video} {
		if f != "" {
			b.WriteString("- " + f + "\n")
		}
	}

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("failed to write summary: %w", err)
	}

	return nil
}
