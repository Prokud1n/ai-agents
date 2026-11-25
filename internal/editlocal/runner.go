// Package editlocal is the editor (Монтажёр) without Telegram, for the owner's checks of the whole edit on a local
// video (cmd/edit-local, make edit): the same app side (editor.Executor with the editor's model, its decisions and
// revisions) and the same media worker, but no gateway, no task queue and no draft sent to the group.
//
// A local edit is a synthetic task: its ID is above TaskIDBase (a Redis counter), so its jobs, result files and done
// messages never meet those of a real task. The source goes to S3 like a video of the team chat (chat LocalChatID,
// the task ID as the message), the executor is run in a loop as the task workers run it — deferred runs wait, temporary
// failures are retried with the backoff of the tasks — and the files of the version are downloaded into
// <out>/v<N>/. The state of the edit (state.json) lets the next run make a revision of the last version.
package editlocal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editor"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/tasks"
	"github.com/Prokud1n/ai-agents/internal/teamchat"
)

// Files of a version in <out>/v<N>/.
const (
	ReelFile        = "reel.mp4"
	PlanFile        = "plan.json"
	DecisionsFile   = "decisions.log"
	DescriptionFile = "description.txt"
	TimingsFile     = "timings.txt"
)

const (
	// LocalChatID is the chat of local sources in their S3 keys: the team chat is a group, its ID is negative.
	LocalChatID int64 = 1
	// localFileID is the file unique id of local sources in their S3 keys (the task ID makes the key unique).
	localFileID = "local"
	// minWait between two runs of the executor: a deferred run without a delay does not spin.
	minWait = 100 * time.Millisecond
	// DefaultPollInterval is the longest wait between two runs of the executor (EDITOR_POLL_INTERVAL is meant for the
	// task queue and is minutes long; the done channel usually wakes the loop earlier).
	DefaultPollInterval = 5 * time.Second
	dirMode             = 0o750
	fileMode            = 0o600
)

// VersionFiles are the files of every finished version.
var VersionFiles = []string{ReelFile, PlanFile, DecisionsFile, DescriptionFile, TimingsFile}

var (
	// ErrWorkerNotRunning: no media worker has sent a heartbeat; nothing is uploaded.
	ErrWorkerNotRunning = errors.New("the media worker is not running")
	// ErrOutInUse: the output directory already holds an edit; a new source needs another one.
	ErrOutInUse = errors.New("the output directory already holds an edit")
	// ErrInvalidSource: the source is not a video file the bot would take (type, size, not a regular file).
	ErrInvalidSource = errors.New("invalid source video")
	// ErrEmptyRevision: a revision without text.
	ErrEmptyRevision = errors.New("the revision has no text")
)

type executor interface {
	Execute(ctx context.Context, data *tasks.ExecuteData) (tasks.Output, error)
}

type board interface {
	Job(ctx context.Context, id string) (editjob.Job, bool, error)
	WorkerAlive(ctx context.Context) (bool, error)
	ListenDone(ctx context.Context, handle func(ctx context.Context, taskID int64)) error
}

type store interface {
	Save(ctx context.Context, f *media.File, r io.ReadSeeker) (string, error)
	ResultKey(sourceKey, name, ext string) (string, error)
	Download(ctx context.Context, key, dir string, maxSize int64) (*media.LocalFile, error)
}

type sequence interface {
	NextTaskID(ctx context.Context) (int64, error)
}

type meter interface {
	Snapshot() Usage
}

// Settings of the loop and the limits.
type Settings struct {
	// MaxFileSize of the source and of each downloaded file: TELEGRAM_MAX_FILE_SIZE, as the bot takes.
	MaxFileSize int64
	// MaxAttempts, RetryBaseDelay and RetryMaxDelay are those of the tasks (TASKS_*): a failed run is retried with
	// the delay base * 2^(attempt-1), at most max; the last attempt is LastAttempt.
	MaxAttempts    int
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
	// PollInterval is the longest wait between two runs; 0 is DefaultPollInterval.
	PollInterval time.Duration
}

// Params of NewRunner.
type Params struct {
	Executor executor
	Board    board
	Store    store
	Sequence sequence
	// Meter gives the model's usage; nil counts nothing.
	Meter    meter
	Settings Settings
	Log      *slog.Logger
	// Progress gets the steps for the owner; nil discards them.
	Progress io.Writer
}

// Runner makes the versions of local edits.
type Runner struct {
	executor executor
	board    board
	store    store
	sequence sequence
	meter    meter
	settings Settings
	log      *slog.Logger
	progress io.Writer
	now      func() time.Time
}

func NewRunner(p *Params) *Runner {
	r := &Runner{
		executor: p.Executor, board: p.Board, store: p.Store, sequence: p.Sequence, meter: p.Meter, settings: p.Settings,
		log: p.Log, progress: p.Progress, now: time.Now,
	}

	if r.meter == nil {
		r.meter = noMeter{}
	}

	if r.progress == nil {
		r.progress = io.Discard
	}

	r.settings.MaxAttempts = max(r.settings.MaxAttempts, 1)

	if r.settings.PollInterval <= 0 {
		r.settings.PollInterval = DefaultPollInterval
	}

	return r
}

type noMeter struct{}

func (noMeter) Snapshot() Usage { return Usage{} }

// FirstData is a new edit: the local source, the caption of the trainer and the output directory.
type FirstData struct {
	Path    string
	Caption string
	OutDir  string
}

// ReviseData is a revision of the last version of the edit in the output directory.
type ReviseData struct {
	OutDir   string
	Revision string
}

// Report of a run. Done: the version is made, its files are in Dir (relative to the output directory). A run that
// failed after the upload still has its task, timings and usage.
type Report struct {
	TaskID  int64
	Version int
	Done    bool
	Dir     string
	Output  tasks.Output
	Timings Timings
	Usage   Usage
}

// First uploads the source and makes version 1 into <out>/v1/. The output directory must not hold an edit yet.
// A failure of the edit is the executor's error (a tasks.PermanentError carries the text the trainer would get).
func (r *Runner) First(ctx context.Context, data *FirstData) (*Report, error) {
	started := r.now()

	_, err := LoadState(data.OutDir)

	switch {
	case err == nil:
		return nil, fmt.Errorf("%s: %w", data.OutDir, ErrOutInUse)
	case !errors.Is(err, ErrNoState):
		return nil, err
	}

	if err := r.checkWorker(ctx); err != nil {
		return nil, err
	}

	source, err := openSource(data.Path, r.settings.MaxFileSize)
	if err != nil {
		return nil, err
	}
	defer source.Close()

	taskID, err := r.sequence.NextTaskID(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get a local task id: %w", err)
	}

	name := filepath.Base(data.Path)
	report := &Report{TaskID: taskID, Version: 1}
	r.progressf("задача %d: загружаю %s в S3", taskID, name)

	uploadStart := r.now()

	key, err := r.store.Save(ctx, &media.File{
		ChatID: LocalChatID, MessageID: taskID, FileUniqueID: localFileID, FileName: name,
	}, source)
	if err != nil {
		return report, fmt.Errorf("failed to upload the source: %w", err)
	}

	report.Timings.Upload = r.now().Sub(uploadStart)

	state := &State{TaskID: taskID, SourceKey: key, SourceFile: name, Caption: data.Caption, Version: 1}
	run := &tasks.ExecuteData{
		TaskID: taskID, Agent: editor.AgentName, Kind: teamchat.ReelsEditTaskKind, Input: data.Caption,
		Media: []string{key}, Version: 1,
	}

	return report, r.complete(ctx, run, state, data.OutDir, report, started)
}

// Revise makes the next version of the edit in the output directory with the revision, as a reply to the draft
// would. A revision the editor does not apply is a tasks.PermanentError; the state stays at the last version.
func (r *Runner) Revise(ctx context.Context, data *ReviseData) (*Report, error) {
	started := r.now()

	revision := strings.TrimSpace(data.Revision)
	if revision == "" {
		return nil, ErrEmptyRevision
	}

	current, err := LoadState(data.OutDir)
	if err != nil {
		return nil, err
	}

	if err := r.checkWorker(ctx); err != nil {
		return nil, err
	}

	next := current.Next(revision)
	report := &Report{TaskID: current.TaskID, Version: next.Version}
	r.progressf("задача %d: правка версии %d → версия %d", current.TaskID, current.Version, next.Version)

	run := &tasks.ExecuteData{
		TaskID: current.TaskID, Agent: editor.AgentName, Kind: teamchat.ReelsEditTaskKind, Input: current.Caption,
		Media: []string{current.SourceKey}, Version: next.Version, Revision: revision,
	}

	return report, r.complete(ctx, run, next, data.OutDir, report, started)
}

// complete runs the version, downloads its files and saves the state.
func (r *Runner) complete(
	ctx context.Context,
	run *tasks.ExecuteData,
	next *State,
	outDir string,
	report *Report,
	started time.Time,
) error {
	output, err := r.execute(ctx, run, &report.Timings)
	report.Usage = r.meter.Snapshot()

	if err != nil {
		report.Timings.Total = r.now().Sub(started)

		return err
	}

	report.Output = output
	dir := VersionDir(run.Version)
	path := filepath.Join(outDir, dir)

	r.progressf("задача %d: скачиваю версию %d", run.TaskID, run.Version)

	downloadStart := r.now()
	if err := r.download(ctx, next.SourceKey, run.Version, path); err != nil {
		return err
	}

	report.Timings.Download = r.now().Sub(downloadStart)
	report.Timings.Total = r.now().Sub(started)

	if err := writeFile(path, DescriptionFile, output.Text+"\n"); err != nil {
		return err
	}

	if err := writeFile(path, TimingsFile, report.Timings.Format()); err != nil {
		return err
	}

	next.UpdatedAt = r.now()
	if err := SaveState(outDir, next); err != nil {
		return err
	}

	report.Done, report.Dir = true, dir

	return nil
}

// execute runs the executor as the task workers do until the version is made or fails: a deferred run waits (the
// done channel wakes it early), any other failure but a permanent one is retried while attempts are left.
func (r *Runner) execute(ctx context.Context, data *tasks.ExecuteData, timings *Timings) (tasks.Output, error) {
	wake, stop := r.listen(ctx, data.TaskID)
	defer stop()

	run := *data
	attempt := 1

	var last stage

	for {
		current := r.stage(ctx, &run)
		if current != last {
			r.progressf("задача %d: %s", run.TaskID, stageText(current))
			last = current
		}

		start, model := r.now(), r.meter.Snapshot().ModelTime
		run.LastAttempt = attempt >= r.settings.MaxAttempts

		output, err := r.executor.Execute(ctx, &run)
		if err == nil {
			timings.add(current, r.now().Sub(start), r.meter.Snapshot().ModelTime-model)

			return output, nil
		}

		delay, err := r.retryDelay(err, &attempt)
		if err == nil {
			err = wait(ctx, delay, wake)
		}

		timings.add(current, r.now().Sub(start), r.meter.Snapshot().ModelTime-model)

		if err != nil {
			return tasks.Output{}, err
		}
	}
}

func stageText(s stage) string {
	if s == renderStage {
		return "воркер рендерит"
	}

	return "воркер: очередь, транскрипция и план"
}

// stage is the stage of the job of the version on the board; before it is submitted, the stage it will have.
func (r *Runner) stage(ctx context.Context, run *tasks.ExecuteData) stage {
	job, found, err := r.board.Job(ctx, editjob.JobID(run.TaskID, run.Version))
	if err != nil {
		r.log.DebugContext(ctx, "editlocal: failed to read the job", "error", err, "task_id", run.TaskID)
	}

	switch {
	case found && job.EffectiveStage() == editjob.RenderStage:
		return renderStage
	case found:
		return planStage
	case run.Version > 1:
		return renderStage
	default:
		return planStage
	}
}

// retryDelay is how long to wait before the next run, or the error that ends the edit. A deferred run is not an
// attempt; a permanent error and a failure of the last attempt end it.
func (r *Runner) retryDelay(err error, attempt *int) (time.Duration, error) {
	var deferred *tasks.DeferredError
	if errors.As(err, &deferred) {
		return min(max(deferred.After, minWait), r.settings.PollInterval), nil
	}

	var permanent *tasks.PermanentError
	if errors.As(err, &permanent) || *attempt >= r.settings.MaxAttempts {
		return 0, err
	}

	delay := backoff(r.settings.RetryBaseDelay, r.settings.RetryMaxDelay, *attempt)
	r.progressf("попытка %d из %d не удалась, повтор через %s: %v", *attempt, r.settings.MaxAttempts, delay, err)
	*attempt++

	return max(delay, minWait), nil
}

// backoff is base * 2^(attempt-1), at most limit (as the tasks retry).
func backoff(base, limit time.Duration, attempt int) time.Duration {
	delay := base

	for range attempt - 1 {
		delay *= 2
		if delay >= limit {
			return limit
		}
	}

	return min(delay, limit)
}

func wait(ctx context.Context, delay time.Duration, wake <-chan struct{}) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("failed to wait for the edit: %w", ctx.Err())
	case <-wake:
	case <-timer.C:
	}

	return nil
}

// listen signals wake when the worker announces a finished job of the task; stop ends the listener. A lost
// announcement only delays the loop until the poll.
func (r *Runner) listen(ctx context.Context, taskID int64) (wake <-chan struct{}, stop func()) {
	listenCtx, cancel := context.WithCancel(ctx)
	signal := make(chan struct{}, 1)
	done := make(chan struct{})

	go func() {
		defer close(done)

		err := r.board.ListenDone(listenCtx, func(_ context.Context, id int64) {
			if id != taskID {
				return
			}

			select {
			case signal <- struct{}{}:
			default:
			}
		})
		if err != nil && listenCtx.Err() == nil {
			r.log.WarnContext(listenCtx, "editlocal: failed to listen for finished jobs, polling", "error", err)
		}
	}()

	return signal, func() {
		cancel()
		<-done
	}
}

func (r *Runner) checkWorker(ctx context.Context) error {
	alive, err := r.board.WorkerAlive(ctx)
	if err != nil {
		return fmt.Errorf("failed to check the media worker: %w", err)
	}

	if !alive {
		return ErrWorkerNotRunning
	}

	return nil
}

// download copies the reel, the plan and the decisions of the version into dir.
func (r *Runner) download(ctx context.Context, sourceKey string, version int, dir string) error {
	files, err := editjob.Files(r.store, sourceKey, version)
	if err != nil {
		return fmt.Errorf("failed to make the keys of the result: %w", err)
	}

	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("failed to create %s: %w", dir, err)
	}

	for _, f := range []struct{ key, name string }{
		{key: files.Reel, name: ReelFile},
		{key: files.Plan, name: PlanFile},
		{key: files.Decisions, name: DecisionsFile},
	} {
		local, err := r.store.Download(ctx, f.key, dir, r.settings.MaxFileSize)
		if err != nil {
			return fmt.Errorf("failed to download %s: %w", f.name, err)
		}

		if err := os.Rename(local.Path, filepath.Join(dir, f.name)); err != nil {
			_ = local.Remove()

			return fmt.Errorf("failed to save %s: %w", f.name, err)
		}
	}

	return nil
}

// openSource opens the local source read-only after the checks the bot makes: a regular file of a video type, not
// empty and not above maxSize.
func openSource(path string, maxSize int64) (*os.File, error) {
	name := filepath.Base(path)
	if media.Extension("", name) == media.UnknownExtension {
		return nil, fmt.Errorf("%s: not a video file (mp4, mov, webm, mkv, avi, 3gp, mpeg): %w", name, ErrInvalidSource)
	}

	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("failed to open the source: %w", err)
	}

	info, err := file.Stat()

	switch {
	case err != nil:
		err = fmt.Errorf("failed to read the source: %w", err)
	case !info.Mode().IsRegular():
		err = fmt.Errorf("%s is not a regular file: %w", name, ErrInvalidSource)
	case info.Size() == 0:
		err = fmt.Errorf("%s is empty: %w", name, ErrInvalidSource)
	case info.Size() > maxSize:
		err = fmt.Errorf("%s is %d bytes, above the limit of %d (TELEGRAM_MAX_FILE_SIZE): %w", name, info.Size(), maxSize,
			ErrInvalidSource)
	}

	if err != nil {
		_ = file.Close()

		return nil, err
	}

	return file, nil
}

func writeFile(dir, name, content string) error {
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), fileMode); err != nil {
		return fmt.Errorf("failed to write %s: %w", name, err)
	}

	return nil
}

func (r *Runner) progressf(format string, args ...any) {
	fmt.Fprintf(r.progress, "edit: "+format+"\n", args...)
}
