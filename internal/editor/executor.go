// Package editor is the editor (Монтажёр) on the app's side: its tasks are not run by a model but by the media
// worker (cmd/media-worker), which edits the source video in a container of its own. Executor gives the job to the
// worker through the board (internal/editjob) and turns its result into a video draft; Waker runs the task as soon as
// the worker announces the result.
//
// Executor never waits for the worker: a run that finds no result yet is deferred (tasks.DeferredError), so the task
// goes back to the queue, no task worker is held for the minutes of the edit, the app stops at once, and the task is
// picked up again after a restart. The job is keyed by the task and the draft version, so every run finds the same
// job: a retried or restarted run never submits a second edit.
package editor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

// AgentName is the editor's agent: the tasks of the editor's bot (gateway.EditorRole).
const AgentName = "editor"

var errNoSource = errors.New("the task has no source video")

type board interface {
	Submit(ctx context.Context, job *editjob.Job) (bool, error)
	Job(ctx context.Context, id string) (editjob.Job, bool, error)
	Result(ctx context.Context, id string) (editjob.Result, bool, error)
	Forget(ctx context.Context, id string) error
}

type resultKeys interface {
	ResultKey(sourceKey, name, ext string) (string, error)
}

// Executor is the tasks.Executor of the editor's tasks; see the package description.
type Executor struct {
	board        board
	keys         resultKeys
	jobTimeout   time.Duration
	pollInterval time.Duration
	log          *slog.Logger
	now          func() time.Time
}

func NewExecutor(b board, keys resultKeys, cfg *config.Editor, log *slog.Logger) *Executor {
	return &Executor{
		board: b, keys: keys, jobTimeout: cfg.JobTimeout, pollInterval: cfg.PollInterval, log: log, now: time.Now,
	}
}

// Execute returns the video draft when the worker has finished, a permanent or temporary error when it failed, and
// a tasks.DeferredError while the job is waiting or running. The job is submitted by the first run.
func (e *Executor) Execute(ctx context.Context, data *tasks.ExecuteData) (tasks.Output, error) {
	if len(data.Media) == 0 {
		return tasks.Output{}, &tasks.PermanentError{Message: noSourceText, Err: errNoSource}
	}

	if data.Version > 1 { // a revision by text is refused before it is queued; this is a safety net
		return tasks.Output{}, &tasks.PermanentError{Message: tasks.VideoRevisionText, Err: tasks.ErrVideoRevision}
	}

	id := editjob.JobID(data.TaskID, data.Version)

	result, found, err := e.board.Result(ctx, id)
	if err != nil {
		return tasks.Output{}, err
	}

	if found {
		return e.finish(ctx, data, &result)
	}

	job, found, err := e.board.Job(ctx, id)
	if err != nil {
		return tasks.Output{}, err
	}

	if !found {
		return tasks.Output{}, e.submit(ctx, data, id)
	}

	now := e.now()
	if !now.Before(job.Deadline) {
		e.forget(ctx, id)

		return tasks.Output{}, &tasks.PermanentError{
			Message: timeoutText(e.jobTimeout),
			Err:     fmt.Errorf("job %s has no result by its deadline %s: %w", id, job.Deadline.Format(time.RFC3339), context.DeadlineExceeded),
		}
	}

	return tasks.Output{}, &tasks.DeferredError{After: min(e.pollInterval, job.Deadline.Sub(now))}
}

// submit gives the job to the worker and defers the run.
func (e *Executor) submit(ctx context.Context, data *tasks.ExecuteData, id string) error {
	now := e.now()
	job := &editjob.Job{
		ID: id, TaskID: data.TaskID, Version: data.Version, SourceKey: data.Media[0],
		CreatedAt: now, Deadline: now.Add(e.jobTimeout),
	}

	submitted, err := e.board.Submit(ctx, job)
	if err != nil {
		return fmt.Errorf("failed to submit edit job: %w", err)
	}

	if submitted {
		e.log.InfoContext(ctx, "editor: job submitted", "job_id", id, "task_id", data.TaskID, "deadline", job.Deadline)
	}

	return &tasks.DeferredError{After: e.pollInterval}
}

// finish turns the worker's result into the draft or the failure. A failed job is forgotten, so a retry of the task
// submits it again; a finished one is kept (a repeated run of the task finds the same result).
func (e *Executor) finish(ctx context.Context, data *tasks.ExecuteData, result *editjob.Result) (tasks.Output, error) {
	if result.Status == editjob.DoneStatus {
		files, err := editjob.Files(e.keys, data.Media[0], data.Version)
		if err != nil {
			return tasks.Output{}, &tasks.PermanentError{Message: editFailedText, Err: err}
		}

		e.log.InfoContext(ctx, "editor: reel ready", "task_id", data.TaskID, "job_id", result.JobID, "key", files.Reel,
			"source_duration", result.SourceDuration, "duration", result.Duration, "audio_seconds", result.AudioSeconds)

		return tasks.Output{Text: Describe(result, data.Input), MediaKey: files.Reel}, nil
	}

	e.forget(ctx, result.JobID)

	err := fmt.Errorf("edit job %s failed: %s", result.JobID, result.Code)
	if result.Code == editjob.TemporaryCode {
		return tasks.Output{}, &tasks.TemporaryError{Message: editFailedText, Err: err}
	}

	return tasks.Output{}, &tasks.PermanentError{Message: failureText(result), Err: err}
}

func (e *Executor) forget(ctx context.Context, id string) {
	if err := e.board.Forget(ctx, id); err != nil {
		e.log.ErrorContext(ctx, "editor: failed to forget the job, a retry finds its old result", "error", err, "job_id", id)
	}
}
