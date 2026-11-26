// Package editlocal is the editor (Монтажёр) without Telegram, for the owner's checks of the whole edit on a local
// video (cmd/edit-local, make edit): the same app side (editor.Executor with the editor's model, its decisions and
// revisions) and the same media worker, but no gateway, no task queue and no draft sent to the group.
//
// A local edit is a synthetic task: its ID is above TaskIDBase (a Redis counter), so its jobs, result files and done
// messages never meet those of a real task. The source goes to S3 like a video of the team chat (chat LocalChatID,
// the task ID as the message, a random file name, never over an object that is there), the executor is run in a loop
// as the task workers run it — deferred runs wait, temporary failures are retried with the backoff of the tasks, an
// interrupted run takes its job off the board — and the files of the version are downloaded into <out>/v<N>/. The
// state of the edit (state.json) lets the next run make a revision of the last version or finish a download; a lock
// keeps two runs out of one output directory.
package editlocal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
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
	// DefaultPollInterval is the longest wait between two runs of the executor (EDITOR_POLL_INTERVAL is meant for the
	// task queue and is minutes long; the done channel usually wakes the loop earlier).
	DefaultPollInterval = 5 * time.Second
	// DefaultRunTimeout of one run of the executor when Settings has none (TASKS_TIMEOUT).
	DefaultRunTimeout = 15 * time.Minute
	dirMode           = 0o750
	fileMode          = 0o600
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
	// ErrSourceMissing: the source of the edit is gone from S3 (the storage was cleaned): only a new edit helps.
	ErrSourceMissing = errors.New("the source of the edit is not in S3")
	// ErrPendingDownload: the last version is made but not downloaded; it is downloaded first.
	ErrPendingDownload = errors.New("the last version is not downloaded")
	// ErrNothingPending: every version of the edit is downloaded.
	ErrNothingPending = errors.New("nothing to download")
)

type executor interface {
	Execute(ctx context.Context, data *tasks.ExecuteData) (tasks.Output, error)
}

type board interface {
	Job(ctx context.Context, id string) (editjob.Job, bool, error)
	WorkerAlive(ctx context.Context) (bool, error)
	ListenDone(ctx context.Context, handle func(ctx context.Context, taskID int64)) error
	Forget(ctx context.Context, id string) error
}

type store interface {
	Key(f *media.File) (string, error)
	Save(ctx context.Context, f *media.File, r io.ReadSeeker) (string, error)
	ResultKey(sourceKey, name, ext string) (string, error)
	Download(ctx context.Context, key, dir string, maxSize int64) (*media.LocalFile, error)
	Exists(ctx context.Context, key string) (bool, error)
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
	// RunTimeout bounds one run of the executor (TASKS_TIMEOUT); 0 is DefaultRunTimeout.
	RunTimeout time.Duration
	// PollInterval is the longest wait between two runs; 0 is DefaultPollInterval.
	PollInterval time.Duration
	// LockStaleAfter: a lock of the output directory older than this is reported as stale (EDITOR_JOB_TIMEOUT: the
	// longest edit); 0 is never by age.
	LockStaleAfter time.Duration
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

	if r.settings.RunTimeout <= 0 {
		r.settings.RunTimeout = DefaultRunTimeout
	}

	if r.settings.LockStaleAfter <= 0 {
		r.settings.LockStaleAfter = time.Duration(math.MaxInt64)
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

// Report of a run. Done: the version is made and downloaded, its files are in Dir (relative to the output
// directory). A run that failed after the upload still has its task, timings and usage.
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

	release, err := acquireLock(data.OutDir, r.settings.LockStaleAfter, started)
	if err != nil {
		return nil, err
	}
	defer release()

	existing, err := LoadState(data.OutDir)

	switch {
	case err == nil && existing.Pending > 0:
		return nil, fmt.Errorf("%s: %w, version %d: %w", data.OutDir, ErrOutInUse, existing.Pending, ErrPendingDownload)
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

	name := filepath.Base(data.Path)

	file, err := r.newSource(ctx, name)
	if err != nil {
		return nil, err
	}

	report := &Report{TaskID: file.MessageID, Version: 1}
	r.progressf("задача %d: загружаю %s в S3", file.MessageID, name)

	uploadStart := r.now()

	key, err := r.store.Save(ctx, file, source)
	if err != nil {
		return report, fmt.Errorf("failed to upload the source: %w", err)
	}

	report.Timings.Upload = r.now().Sub(uploadStart)

	state := &State{
		Format: StateFormat, TaskID: file.MessageID, SourceKey: key, SourceFile: name, Caption: data.Caption, Version: 1,
	}
	run := &tasks.ExecuteData{
		TaskID: file.MessageID, Agent: editor.AgentName, Kind: teamchat.ReelsEditTaskKind, Input: data.Caption,
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

	release, err := acquireLock(data.OutDir, r.settings.LockStaleAfter, started)
	if err != nil {
		return nil, err
	}
	defer release()

	current, err := LoadState(data.OutDir)
	if err != nil {
		return nil, err
	}

	if current.Pending > 0 {
		return nil, fmt.Errorf("version %d: %w", current.Pending, ErrPendingDownload)
	}

	if err := r.checkWorker(ctx); err != nil {
		return nil, err
	}

	if err := r.checkSource(ctx, current.SourceKey); err != nil {
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

// Resume downloads the version a run made but did not download (interrupted, or the download failed).
func (r *Runner) Resume(ctx context.Context, outDir string) (*Report, error) {
	started := r.now()

	release, err := acquireLock(outDir, r.settings.LockStaleAfter, started)
	if err != nil {
		return nil, err
	}
	defer release()

	state, err := LoadState(outDir)
	if err != nil {
		return nil, err
	}

	if state.Pending == 0 {
		return nil, fmt.Errorf("version %d: %w", state.Version, ErrNothingPending)
	}

	report := &Report{TaskID: state.TaskID, Version: state.Pending}
	r.progressf("задача %d: докачиваю версию %d", state.TaskID, state.Pending)

	if err := r.finishDownload(ctx, state, outDir, report); err != nil {
		return report, err
	}

	report.Timings.Total = r.now().Sub(started)

	return report, nil
}

// complete runs the version, saves it as pending (its files are in S3), downloads its files and saves the state.
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
	report.Timings.Total = r.now().Sub(started)

	if err != nil {
		return err
	}

	report.Output = output
	path := filepath.Join(outDir, VersionDir(run.Version))

	if err := r.writeVersionTexts(path, output.Text, &report.Timings); err != nil {
		return err
	}

	next.Pending = run.Version
	next.UpdatedAt = r.now()

	if err := SaveState(outDir, next); err != nil {
		return err
	}

	if err := r.finishDownload(ctx, next, outDir, report); err != nil {
		return err
	}

	report.Timings.Total = r.now().Sub(started)

	// timings.txt again, now with the download.
	return writeFile(path, TimingsFile, report.Timings.Format())
}

// finishDownload downloads the pending version of the state and clears it.
func (r *Runner) finishDownload(ctx context.Context, state *State, outDir string, report *Report) error {
	version := state.Pending
	dir := VersionDir(version)

	r.progressf("задача %d: скачиваю версию %d", state.TaskID, version)

	downloadStart := r.now()
	if err := r.download(ctx, state.SourceKey, version, filepath.Join(outDir, dir)); err != nil {
		return err
	}

	report.Timings.Download = r.now().Sub(downloadStart)

	done := *state
	done.Pending = 0
	done.UpdatedAt = r.now()

	if err := SaveState(outDir, &done); err != nil {
		return err
	}

	report.Done, report.Dir = true, dir

	return nil
}

// writeVersionTexts writes the description and the timings so far into the directory of the version.
func (*Runner) writeVersionTexts(path, description string, timings *Timings) error {
	if err := mkdir(path); err != nil {
		return err
	}

	if err := writeFile(path, DescriptionFile, description+"\n"); err != nil {
		return err
	}

	return writeFile(path, TimingsFile, timings.Format())
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

func (r *Runner) progressf(format string, args ...any) {
	fmt.Fprintf(r.progress, "edit: "+format+"\n", args...)
}
