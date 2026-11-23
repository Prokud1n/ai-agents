package mediaworker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
)

const (
	errorPause = 2 * time.Second // pause after a board error, so a Redis outage is not a busy loop
	// finalTimeout bounds the writes after a job (the result, the ack, the unlock), made even on stop.
	finalTimeout = 10 * time.Second
	// heartbeatInterval: how often the worker marks itself present and touches the heartbeat file.
	heartbeatInterval = 10 * time.Second
	// HeartbeatMaxAge: an older heartbeat file means the worker is unhealthy (it lost Redis or hangs).
	HeartbeatMaxAge = 4 * heartbeatInterval
	heartbeatName   = ".heartbeat"
	heartbeatMode   = 0o600
	// lockRefreshes per lock lifetime: a refresh is missed only after several failures in a row.
	lockRefreshes = 3
	// consumerSuffix: random characters that make the consumer name unique per process.
	consumerSuffix = 8
)

// leftoverPrefixes are the temporary directories of a job (the job's own and those of ffmpeg, the subtitles and
// the transcription, which go to TMPDIR): a worker killed mid-job leaves them, the next start removes them.
var leftoverPrefixes = []string{"job-", "ffmpeg-filter-", "subtitles-", "transcribe-", ".roughcut-", "media-"}

type workerBoard interface {
	Init(ctx context.Context) error
	Fetch(ctx context.Context, consumer string) (*editjob.Delivery, error)
	Ack(ctx context.Context, messageID string) error
	HasResult(ctx context.Context, jobID string) (bool, error)
	Lock(ctx context.Context, jobID, owner string) (bool, error)
	Refresh(ctx context.Context, jobID, owner string) (bool, error)
	Unlock(ctx context.Context, jobID, owner string) error
	Finish(ctx context.Context, job *editjob.Job, result *editjob.Result) error
	NotifyDone(ctx context.Context, job *editjob.Job)
	Heartbeat(ctx context.Context, owner string) error
	LockTTL() time.Duration
}

type jobRunner interface {
	Run(ctx context.Context, job *editjob.Job) (*editjob.Result, error)
}

// Worker takes jobs from the board one at a time and runs them with the pipeline.
//
// A job is run under a lock refreshed while it runs, and acked only after its result is stored: a worker that dies
// mid-job leaves the entry pending, its lock expires, and the entry is claimed again (by this worker after a restart
// or by another one). An entry whose job has a result is acked and skipped; one whose job is locked is left pending
// for a later claim. So a job is never edited twice at the same time, a finished job is not edited again, and a job of
// a dead worker is not lost.
//
// Stop cancels the running job at once (ffmpeg is killed with its context): the job is not acked and is taken again
// after the restart.
type Worker struct {
	board         workerBoard
	pipeline      jobRunner
	consumer      string
	workDir       string
	heartbeatFile string
	log           *slog.Logger
	now           func() time.Time

	cancel context.CancelFunc
	done   sync.WaitGroup
}

func NewWorker(board workerBoard, pipeline jobRunner, cfg *config.MediaWorker, log *slog.Logger) *Worker {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "media-worker"
	}

	return &Worker{
		board:    board,
		pipeline: pipeline,
		// Unique per process: a restarted container has the same host name and PID 1, and must not pass for its
		// previous run (its lock and its pending entries).
		consumer:      host + "-" + strconv.Itoa(os.Getpid()) + "-" + rand.Text()[:consumerSuffix],
		workDir:       cfg.WorkDir,
		heartbeatFile: HeartbeatFile(cfg),
		log:           log,
		now:           time.Now,
	}
}

// HeartbeatFile is the heartbeat file of the config: MEDIA_WORKER_HEARTBEAT_FILE, or .heartbeat in the work
// directory.
func HeartbeatFile(cfg *config.MediaWorker) string {
	if cfg.HeartbeatFile != "" {
		return cfg.HeartbeatFile
	}

	dir := cfg.WorkDir
	if dir == "" {
		dir = os.TempDir()
	}

	return filepath.Join(dir, heartbeatName)
}

// Start creates the consumer group, removes what a killed job left and starts taking jobs; the goroutines outlive
// the start context.
func (w *Worker) Start(ctx context.Context) error {
	if err := w.board.Init(ctx); err != nil {
		return fmt.Errorf("failed to init the job board: %w", err)
	}

	w.removeLeftovers(ctx)

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	w.cancel = cancel

	w.done.Go(func() { w.loop(runCtx) })
	w.done.Go(func() { w.heartbeat(runCtx) })

	w.log.InfoContext(ctx, "mediaworker: started", "consumer", w.consumer, "work_dir", w.workDir)

	return nil
}

// Stop interrupts the running job and waits for the goroutines, at most until ctx is done.
func (w *Worker) Stop(ctx context.Context) error {
	if w.cancel == nil {
		return nil
	}

	w.cancel()

	done := make(chan struct{})

	go func() {
		w.done.Wait()
		close(done)
	}()

	select {
	case <-done:
		w.log.InfoContext(ctx, "mediaworker: stopped")

		return nil
	case <-ctx.Done():
		return fmt.Errorf("failed to stop media worker: %w", ctx.Err())
	}
}

func (w *Worker) loop(ctx context.Context) {
	for ctx.Err() == nil {
		d, err := w.board.Fetch(ctx, w.consumer)
		if err != nil {
			if ctx.Err() == nil {
				w.log.ErrorContext(ctx, "mediaworker: failed to fetch a job", "error", err)
				sleep(ctx, errorPause)
			}

			continue
		}

		if d != nil {
			w.handle(ctx, d)
		}
	}
}

// handle runs the delivered job unless it is malformed, finished, expired or taken by another worker; see Worker.
func (w *Worker) handle(ctx context.Context, d *editjob.Delivery) {
	if d.Job == nil {
		w.log.ErrorContext(ctx, "mediaworker: skipping malformed job entry", "message_id", d.MessageID)
		w.ack(ctx, d)

		return
	}

	job := d.Job

	skip, err := w.skip(ctx, job)
	if err != nil {
		w.log.ErrorContext(ctx, "mediaworker: failed to check job, it will be taken again", "error", err, "job_id", job.ID)

		return
	}

	if skip {
		w.ack(ctx, d)

		return
	}

	// A held lock is another live worker, or one that died a moment ago (its lock has not expired yet): the entry
	// stays pending and is claimed again after the claim time; by then the job has a result or a free lock.
	locked, err := w.board.Lock(ctx, job.ID, w.consumer)
	if err != nil || !locked {
		w.log.InfoContext(ctx, "mediaworker: job is locked, it will be taken again later", "job_id", job.ID, "error", err)

		return
	}

	defer w.unlock(ctx, job)

	result := w.run(ctx, job)
	if result == nil { // stopped or lost: the entry stays pending and is taken again (after the restart)
		w.log.InfoContext(ctx, "mediaworker: job interrupted", "job_id", job.ID)

		return
	}

	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalTimeout)
	defer cancel()

	if err := w.board.Finish(finalCtx, job, result); err != nil {
		w.log.ErrorContext(ctx, "mediaworker: failed to store the result, the job will be run again", "error", err,
			"job_id", job.ID)

		return
	}

	w.ack(finalCtx, d)
}

// skip: a job with a result is announced again (the app may have missed it) and skipped; an expired job gets a
// timeout result without being run.
func (w *Worker) skip(ctx context.Context, job *editjob.Job) (bool, error) {
	finished, err := w.board.HasResult(ctx, job.ID)
	if err != nil {
		return false, err
	}

	if finished {
		w.log.InfoContext(ctx, "mediaworker: job already has a result", "job_id", job.ID)
		w.board.NotifyDone(ctx, job)

		return true, nil
	}

	if !w.now().Before(job.Deadline) {
		w.log.WarnContext(ctx, "mediaworker: job expired before it was taken", "job_id", job.ID, "deadline", job.Deadline)

		if err := w.board.Finish(ctx, job, editjob.Failed(job.ID, editjob.TimeoutCode, w.now())); err != nil {
			return false, err
		}

		return true, nil
	}

	return false, nil
}

// run runs the pipeline until the job's deadline with the lock refreshed; nil when the worker is stopping or lost
// the lock (another worker runs the job now, its result counts).
func (w *Worker) run(ctx context.Context, job *editjob.Job) *editjob.Result {
	jobCtx, cancel := context.WithDeadline(ctx, job.Deadline)
	defer cancel()

	var lost atomic.Bool

	refreshDone := make(chan struct{})

	go func() {
		defer close(refreshDone)

		if !w.keepLock(jobCtx, job) {
			lost.Store(true)
			cancel()
		}
	}()

	w.log.InfoContext(ctx, "mediaworker: job started", "job_id", job.ID, "task_id", job.TaskID, "source", job.SourceKey)

	started := w.now()
	result, err := w.runPipeline(jobCtx, job)

	cancel()
	<-refreshDone

	if ctx.Err() != nil || lost.Load() {
		return nil
	}

	if err != nil {
		code := CodeOf(err)
		if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
			code = editjob.TimeoutCode
		}

		w.log.ErrorContext(ctx, "mediaworker: job failed", "job_id", job.ID, "code", code, "error", err,
			"seconds", roundTime(w.now().Sub(started).Seconds()))

		result.Status, result.Code, result.FinishedAt = editjob.FailedStatus, code, w.now()

		return result
	}

	w.log.InfoContext(ctx, "mediaworker: job done", "job_id", job.ID, "seconds", roundTime(w.now().Sub(started).Seconds()),
		"source_duration", result.SourceDuration, "duration", result.Duration, "audio_seconds", result.AudioSeconds)

	return result
}

// runPipeline turns a panic of the pipeline into a temporary failure with its stack in the log.
func (w *Worker) runPipeline(ctx context.Context, job *editjob.Job) (result *editjob.Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			w.log.ErrorContext(ctx, "mediaworker: pipeline panicked", "job_id", job.ID, "panic", fmt.Sprint(r),
				"stack", string(debug.Stack()))

			result, err = &editjob.Result{JobID: job.ID}, fmt.Errorf("pipeline panicked: %v", r)
		}
	}()

	result, err = w.pipeline.Run(ctx, job)
	if result == nil {
		result = &editjob.Result{JobID: job.ID}
	}

	return result, err
}

// keepLock refreshes the job's lock until ctx is done and reports false when the lock is lost (it expired and
// another worker took the job): this run then stops, so the job is not edited twice at once.
func (w *Worker) keepLock(ctx context.Context, job *editjob.Job) bool {
	ticker := time.NewTicker(w.board.LockTTL() / lockRefreshes)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return true
		case <-ticker.C:
			held, err := w.board.Refresh(ctx, job.ID, w.consumer)

			switch {
			case err != nil && ctx.Err() == nil:
				w.log.WarnContext(ctx, "mediaworker: failed to refresh job lock", "error", err, "job_id", job.ID)
			case err == nil && !held:
				w.log.ErrorContext(ctx, "mediaworker: job lock lost, stopping the job", "job_id", job.ID)

				return false
			}
		}
	}
}

func (w *Worker) unlock(ctx context.Context, job *editjob.Job) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalTimeout)
	defer cancel()

	if err := w.board.Unlock(ctx, job.ID, w.consumer); err != nil {
		w.log.WarnContext(ctx, "mediaworker: failed to unlock job, the lock expires by itself", "error", err, "job_id", job.ID)
	}
}

func (w *Worker) ack(ctx context.Context, d *editjob.Delivery) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finalTimeout)
	defer cancel()

	if err := w.board.Ack(ctx, d.MessageID); err != nil {
		w.log.ErrorContext(ctx, "mediaworker: failed to ack job entry", "error", err, "message_id", d.MessageID)
	}
}

// heartbeat marks the worker present on the board and touches the heartbeat file while Redis answers.
func (w *Worker) heartbeat(ctx context.Context) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		if err := w.board.Heartbeat(ctx, w.consumer); err != nil {
			if ctx.Err() == nil {
				w.log.WarnContext(ctx, "mediaworker: heartbeat failed", "error", err)
			}
		} else if err := touch(w.heartbeatFile, w.now()); err != nil {
			w.log.WarnContext(ctx, "mediaworker: failed to touch the heartbeat file", "error", err, "file", w.heartbeatFile)
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// touch sets the modification time of the heartbeat file, creating it first if needed.
func touch(path string, now time.Time) error {
	if err := os.Chtimes(path, now, now); err == nil {
		return nil
	}

	if err := os.WriteFile(path, nil, heartbeatMode); err != nil {
		return fmt.Errorf("failed to write heartbeat file: %w", err)
	}

	if err := os.Chtimes(path, now, now); err != nil {
		return fmt.Errorf("failed to touch heartbeat file: %w", err)
	}

	return nil
}

// CheckHeartbeat reports an error if the heartbeat file is missing or older than maxAge (the container
// healthcheck).
func CheckHeartbeat(path string, maxAge time.Duration, now time.Time) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to read heartbeat: %w", err)
	}

	if age := now.Sub(info.ModTime()); age > maxAge {
		return fmt.Errorf("heartbeat is %s old, at most %s: %w", age.Round(time.Second), maxAge, ErrUnhealthy)
	}

	return nil
}

// ErrUnhealthy: the worker has not sent a heartbeat for too long.
var ErrUnhealthy = errors.New("media worker is unhealthy")

// removeLeftovers removes the temporary directories a killed job left in MEDIA_WORKER_DIR. Only the configured
// directory is cleaned, and only entries with the prefixes of the job's own directories.
func (w *Worker) removeLeftovers(ctx context.Context) {
	if w.workDir == "" {
		return
	}

	entries, err := os.ReadDir(w.workDir)
	if err != nil {
		w.log.WarnContext(ctx, "mediaworker: failed to read the work dir", "error", err, "dir", w.workDir)

		return
	}

	for _, e := range entries {
		if !hasLeftoverPrefix(e.Name()) {
			continue
		}

		if err := os.RemoveAll(filepath.Join(w.workDir, e.Name())); err != nil {
			w.log.WarnContext(ctx, "mediaworker: failed to remove a leftover", "error", err, "name", e.Name())

			continue
		}

		w.log.InfoContext(ctx, "mediaworker: removed a leftover of a killed job", "name", e.Name())
	}
}

func hasLeftoverPrefix(name string) bool {
	for _, prefix := range leftoverPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	return false
}

func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
