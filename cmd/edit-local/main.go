// Command edit-local runs the editor (Монтажёр) on a local video without Telegram (make edit, README «Проверка
// Монтажёра без Telegram»):
//
//	edit-local -in video.mov [-caption "подпись"] -out ./edits/name    # version 1
//	edit-local -out ./edits/name -revision "сделай 40 секунд"          # a revision of the last version
//	edit-local -out ./edits/name                                       # download a version left undownloaded
//
// It runs the app's editor (the model, its decisions and revisions) against the running media worker; nothing goes to
// the group and no task is queued. The files of the version go to <out>/v<N>/, the summary to stdout, logs to stderr.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal"
	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editlocal"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	failureExitCode = 1
	usageExitCode   = 2
)

const usageText = `usage:
  edit-local -in <video> [-caption <text>] -out <dir>    a new edit: version 1 into <dir>/v1
  edit-local -out <dir> -revision <text>                  a revision of the last version in <dir>
  edit-local -out <dir>                                   download the version an interrupted run left in S3`

var errUsage = errors.New(usageText)

type options struct {
	in       string
	caption  string
	out      string
	revision string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseFlags(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}

	if err != nil {
		fmt.Fprintln(stderr, err)

		return usageExitCode
	}

	lifecycle, err := config.ParseLifecycle()
	if err != nil {
		fmt.Fprintln(stderr, err)

		return failureExitCode
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var runner *editlocal.Runner

	app := fx.New(append(internal.DeclareEditLocalOptions(), fx.Populate(&runner))...)

	startCtx, cancelStart := context.WithTimeout(ctx, lifecycle.StartTimeout)
	defer cancelStart()

	if err := app.Start(startCtx); err != nil {
		fmt.Fprintln(stderr, "failed to start:", err)

		return failureExitCode
	}

	defer func() {
		stopCtx, cancelStop := context.WithTimeout(context.WithoutCancel(ctx), lifecycle.StopTimeout)
		defer cancelStop()

		if err := app.Stop(stopCtx); err != nil {
			fmt.Fprintln(stderr, "failed to stop:", err)
		}
	}()

	report, err := edit(ctx, runner, &opts)
	if report != nil {
		printReport(stdout, report)
	}

	if err != nil {
		printFailure(stderr, err)

		return failureExitCode
	}

	return 0
}

func parseFlags(args []string, output io.Writer) (options, error) {
	var opts options

	flags := flag.NewFlagSet("edit-local", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		fmt.Fprintln(output, usageText)
		flags.PrintDefaults()
	}
	flags.StringVar(&opts.in, "in", "", "source video: a new edit, version 1")
	flags.StringVar(&opts.caption, "caption", "", "the trainer's caption of the source (with -in)")
	flags.StringVar(&opts.out, "out", "", "output directory: state.json and v<N>/ with the files of each version")
	flags.StringVar(&opts.revision, "revision", "", "the trainer's revision of the last version in -out")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return options{}, err
		}

		return options{}, fmt.Errorf("failed to parse flags: %w", err)
	}

	if flags.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected arguments %q: %w", flags.Args(), errUsage)
	}

	if err := opts.validate(); err != nil {
		return options{}, err
	}

	return opts, nil
}

func (o *options) validate() error {
	switch {
	case strings.TrimSpace(o.out) == "":
		return fmt.Errorf("-out is required: %w", errUsage)
	case o.revision != "" && strings.TrimSpace(o.revision) == "":
		return fmt.Errorf("-revision is blank: %w", errUsage)
	case o.in != "" && o.revision != "":
		return fmt.Errorf("-in and -revision together: %w", errUsage)
	case o.in == "" && o.caption != "":
		return fmt.Errorf("-caption is the caption of a new source (-in): %w", errUsage)
	}

	return nil
}

func edit(ctx context.Context, runner *editlocal.Runner, opts *options) (*editlocal.Report, error) {
	if opts.in != "" {
		report, err := runner.First(ctx, &editlocal.FirstData{Path: opts.in, Caption: opts.caption, OutDir: opts.out})
		if err != nil {
			return report, fmt.Errorf("failed to make version 1: %w", err)
		}

		return report, nil
	}

	if opts.revision == "" {
		report, err := runner.Resume(ctx, opts.out)
		if err != nil {
			return report, fmt.Errorf("failed to download the version: %w", err)
		}

		return report, nil
	}

	report, err := runner.Revise(ctx, &editlocal.ReviseData{OutDir: opts.out, Revision: opts.revision})
	if err != nil {
		return report, fmt.Errorf("failed to make the revision: %w", err)
	}

	return report, nil
}

func printReport(out io.Writer, report *editlocal.Report) {
	fmt.Fprintln(out, "--- make edit ---")

	if report.Done {
		fmt.Fprintf(out, "Версия %d готова: OUT/%s/ (%s)\n", report.Version, report.Dir,
			strings.Join(editlocal.VersionFiles, ", "))
	} else {
		fmt.Fprintf(out, "Версия %d не сделана.\n", report.Version)
	}

	fmt.Fprintf(out, "Задача: %d (локальная: без Telegram и очереди задач)\n", report.TaskID)
	fmt.Fprintf(out, "Время: %s\n", report.Timings.Summary())
	fmt.Fprintf(out, "Модель: %s\n", report.Usage.Summary())

	if report.Done {
		fmt.Fprintln(out, "--- описание черновика ---")
		fmt.Fprintln(out, report.Output.Text)
	}
}

// hints explain the failures of the setup to the owner.
var hints = []struct {
	err  error
	hint string
}{
	{err: editlocal.ErrWorkerNotRunning, hint: "media-worker не запущен: make up"},
	{err: editlocal.ErrOutInUse, hint: "в OUT уже есть монтаж: для правки передайте REVISION, для нового видео — другой OUT"},
	{err: editlocal.ErrNoState, hint: "в OUT нет state.json: сначала make edit FILE=<видео> OUT=<папка>"},
	{err: editlocal.ErrInvalidState, hint: "state.json в OUT испорчен: начните новый монтаж в другой папке"},
	{err: editlocal.ErrInvalidSource, hint: "такой файл бот бы не принял: нужно видео (mp4, mov, webm, mkv, avi, 3gp, mpeg) " +
		"не больше TELEGRAM_MAX_FILE_SIZE"},
	{err: editlocal.ErrEmptyRevision, hint: "REVISION пустой: напишите правку, как тренер в реплае"},
	{err: editlocal.ErrOutLocked, hint: "в OUT уже работает другой запуск make edit: дождитесь его. Если его точно нет " +
		"(контейнер убит), удалите OUT/.lock"},
	{err: editlocal.ErrStaleLock, hint: "в OUT остался .lock прерванного запуска: убедитесь, что make edit с этим OUT " +
		"не идёт (docker ps), и удалите OUT/.lock"},
	{err: editlocal.ErrSourceMissing, hint: "исходник не найден в S3 (хранилище очищено?) — начните заново: " +
		"make edit FILE=<видео> с новым OUT"},
	{err: editlocal.ErrPendingDownload, hint: "последняя версия не скачана: сначала make edit OUT=<папка> без FILE и REVISION"},
	{err: editlocal.ErrNothingPending, hint: "всё уже скачано: для правки передайте REVISION"},
	{err: context.Canceled, hint: "монтаж остановлен (Ctrl+C), задание снято с очереди воркера"},
}

// printFailure prints the text the trainer would get (a failure of the edit), or a hint for the setup, and the cause.
// The trainer's text is printed once: the cause is what is under it.
func printFailure(out io.Writer, err error) {
	var (
		permanent *tasks.PermanentError
		temporary *tasks.TemporaryError
		cause     = err
	)

	switch {
	case errors.As(err, &permanent):
		fmt.Fprintf(out, "Монтаж не сделан. Тренер получил бы: %s\n", permanent.Message)
		cause = permanent.Err
	case errors.As(err, &temporary):
		fmt.Fprintf(out, "Монтаж не сделан, попытки кончились. Тренер получил бы: %s\n", temporary.Message)
		cause = temporary.Err
	}

	for _, h := range hints {
		if errors.Is(err, h.err) {
			fmt.Fprintln(out, h.hint)
		}
	}

	fmt.Fprintln(out, "причина:", cause)
}
