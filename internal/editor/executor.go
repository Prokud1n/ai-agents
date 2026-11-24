// Package editor is the editor (Монтажёр) on the app's side. The video is edited by the media worker
// (cmd/media-worker) in a container of its own; what to cut is decided here, by the editor's agent (agents/editor,
// internal/editplan), from the transcript and the plan the worker stored in S3. Executor gives the jobs to the worker
// through the board (internal/editjob) and turns its result into a video draft; Waker runs the task as soon as the
// worker announces a result.
//
// The first version is one auto job: the worker transcribes and plans; with nothing to decide (no caption of the
// trainer, no disputed repeats) it renders at once, otherwise it stops with a planned result, the model decides here,
// and the job is replaced by its render stage (the transcript is not made again). A revision («Правки» or a reply to
// the video draft) is a new version: the model decides on the previous version's plan and the revision text, and the
// worker renders the new version from the stored transcript under the keys of that version, so the earlier drafts
// stay. The model's decision is checked (editplan.Apply) before any job carries it; the trainer's text never reaches
// the worker.
//
// Executor never waits for the worker: a run that finds no result yet is deferred (tasks.DeferredError), so the task
// goes back to the queue, no task worker is held for the minutes of the edit, the app stops at once, and the task is
// picked up again after a restart. The job is keyed by the task and the draft version, so every run finds the same
// job: a retried or restarted run never submits a second edit.
//
// Limits: a job waits for a worker up to EDITOR_QUEUE_TIMEOUT (sources are edited one by one), runs up to
// EDITOR_JOB_TIMEOUT from the moment a worker took it, and fails at once when no worker is running
// (no heartbeat EDITOR_WORKER_WAIT after the submit). Technical failures reach the trainer as one plain text; the code
// goes to the log and to «Техничка».
package editor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/providers/anthropic"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/tasks"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

// AgentName is the editor's agent: the tasks of the editor's bot (gateway.EditorRole).
const AgentName = editplan.AgentName

const (
	// startGrace: the worker reports a job that ran out of time itself; the app fails it only this much later.
	startGrace = time.Minute
	// maxArtifactSize of the transcript or the plan read from S3.
	maxArtifactSize = 32 << 20
	// Codes of the app's own technical failures (the worker's are editjob.FailureCode).
	workerUnavailableCode = "worker_unavailable"
	resultKeysCode        = "result_keys"
	modelUnavailableCode  = "model_unavailable"
	modelFailedCode       = "model_failed"
)

var (
	errNoSource          = errors.New("the task has no source video")
	errWorkerUnavailable = errors.New("no media worker is running")
	errArtifactMissing   = errors.New("the transcript or the plan of the edit is missing")
	errEmptyRevision     = errors.New("the revision has no text")
	errRevisionRefused   = errors.New("the editor did not apply the revision")
)

type board interface {
	Submit(ctx context.Context, job *editjob.Job) (bool, error)
	Replace(ctx context.Context, job *editjob.Job) (bool, error)
	Retry(ctx context.Context, job *editjob.Job) (bool, error)
	Job(ctx context.Context, id string) (editjob.Job, bool, error)
	Result(ctx context.Context, id string) (editjob.Result, bool, error)
	Forget(ctx context.Context, id string) error
	Started(ctx context.Context, id string) (time.Time, bool, error)
	Locked(ctx context.Context, id string) (bool, error)
	WorkerAlive(ctx context.Context) (bool, error)
}

// store computes the keys of the result files and reads the transcript and the plans.
type store interface {
	ResultKey(sourceKey, name, ext string) (string, error)
	Read(ctx context.Context, key string, maxSize int64) ([]byte, error)
}

type decider interface {
	Decide(ctx context.Context, req *editplan.Request) (*editplan.Outcome, error)
}

type techReporter interface {
	Report(ctx context.Context, agent, text string) error
}

// Executor is the tasks.Executor of the editor's tasks; see the package description.
type Executor struct {
	board        board
	store        store
	decider      decider
	tech         techReporter
	jobTimeout   time.Duration
	queueTimeout time.Duration
	workerWait   time.Duration
	pollInterval time.Duration
	log          *slog.Logger
	now          func() time.Time
}

func NewExecutor(b board, s store, d decider, tech techReporter, cfg *config.Editor, log *slog.Logger) *Executor {
	return &Executor{
		board: b, store: s, decider: d, tech: tech, jobTimeout: cfg.JobTimeout, queueTimeout: cfg.QueueTimeout,
		workerWait: cfg.WorkerWait, pollInterval: cfg.PollInterval, log: log, now: time.Now,
	}
}

// Execute returns the video draft when the worker has finished, a permanent or temporary error when it failed, and
// a tasks.DeferredError while the job is waiting or running. The job is submitted by the first run; a planned job is
// decided and replaced by its render by the run that finds it.
//
// A result stored right after the check below is not lost: the worker's announcement may come while this run is still
// going (Wake leaves a running task alone), and then the next poll finds the result.
func (e *Executor) Execute(ctx context.Context, data *tasks.ExecuteData) (tasks.Output, error) {
	if len(data.Media) == 0 {
		return tasks.Output{}, &tasks.PermanentError{Message: noSourceText, Err: errNoSource}
	}

	id := editjob.JobID(data.TaskID, data.Version)

	result, found, err := e.board.Result(ctx, id)
	if err != nil {
		return tasks.Output{}, err
	}

	if found && result.Status == editjob.PlannedStatus {
		return tasks.Output{}, e.decidePlanned(ctx, data, &result)
	}

	if found {
		return e.finish(ctx, data, &result)
	}

	job, found, err := e.board.Job(ctx, id)
	if err != nil {
		return tasks.Output{}, err
	}

	switch {
	case found:
		return tasks.Output{}, e.wait(ctx, data, &job)
	case data.Version > 1:
		return tasks.Output{}, e.revise(ctx, data)
	default:
		return tasks.Output{}, e.submit(ctx, e.autoJob(data))
	}
}

// newJob is a job of the version of the task, due by the configured limits from now.
func (e *Executor) newJob(data *tasks.ExecuteData) *editjob.Job {
	now := e.now()

	return &editjob.Job{
		ID: editjob.JobID(data.TaskID, data.Version), TaskID: data.TaskID, Version: data.Version,
		SourceKey: data.Media[0], CreatedAt: now, QueueDeadline: now.Add(e.queueTimeout), Timeout: e.jobTimeout,
	}
}

// autoJob is the first edit: the worker stops after the plan when the caption is to be applied or a repeat decided.
func (e *Executor) autoJob(data *tasks.ExecuteData) *editjob.Job {
	job := e.newJob(data)
	job.Stage = editjob.AutoStage
	job.Decide = strings.TrimSpace(data.Input) != ""

	return job
}

// decidePlanned decides on the plan of a stopped first edit (the caption, the disputed repeats) and replaces the job
// with its render. The first version is made even when the caption cannot be applied: the automatic cut is rendered
// and the description says why the caption was not applied.
func (e *Executor) decidePlanned(ctx context.Context, data *tasks.ExecuteData, result *editjob.Result) error {
	files, err := editjob.Files(e.store, data.Media[0], data.Version)
	if err != nil {
		return e.technical(ctx, data, resultKeysCode, err)
	}

	req, err := e.loadRequest(ctx, data, files.Transcript, files.Plan)
	if err != nil {
		// Without its files the planned job is of no use: a retry of the task starts over.
		var permanent *tasks.PermanentError
		if errors.As(err, &permanent) {
			e.forget(ctx, result.JobID)
		}

		return err
	}

	req.Caption = data.Input

	outcome, err := e.decide(ctx, data, req)
	if err != nil {
		return err
	}

	job := e.newJob(data)
	job.Stage = editjob.RenderStage
	job.Settings = new(req.Plan.Settings)
	job.Summary = firstSummary(req, outcome)

	if outcome.Kind == editplan.EditOutcome {
		job.Edit = new(outcome.Edit)
	}

	replaced, err := e.board.Replace(ctx, job)
	if err != nil {
		return fmt.Errorf("failed to replace the planned edit job: %w", err)
	}

	if replaced {
		e.log.InfoContext(ctx, "editor: decided, render submitted", "job_id", job.ID, "task_id", data.TaskID,
			"decision", outcome.Kind, "changes", len(outcome.Changes), "cut", outcome.Cut, "kept", outcome.Kept)
	}

	return &tasks.DeferredError{After: e.pollInterval}
}

// revise decides on the revision of the previous version and submits the render of the new one. A revision the editor
// cannot do or did not understand fails with a text for the trainer; the previous draft stays.
func (e *Executor) revise(ctx context.Context, data *tasks.ExecuteData) error {
	revision := strings.TrimSpace(data.Revision)
	if revision == "" {
		return &tasks.PermanentError{Message: unclearRevisionText, Err: errEmptyRevision}
	}

	previous, err := editjob.Files(e.store, data.Media[0], data.Version-1)
	if err != nil {
		return e.technical(ctx, data, resultKeysCode, err)
	}

	req, err := e.loadRequest(ctx, data, previous.Transcript, previous.Plan)
	if err != nil {
		return err
	}

	req.Revision = revision

	outcome, err := e.decide(ctx, data, req)
	if err != nil {
		return err
	}

	switch outcome.Kind {
	case editplan.EditOutcome:
	case editplan.CannotOutcome:
		return &tasks.PermanentError{Message: cannotRevisionText, Err: refusal(outcome)}
	default:
		return &tasks.PermanentError{Message: unclearRevisionText, Err: refusal(outcome)}
	}

	job := e.newJob(data)
	job.Stage = editjob.RenderStage
	job.Settings = new(req.Plan.Settings)
	job.Edit = new(outcome.Edit)
	job.Summary = &editjob.Summary{Changes: outcome.Changes, PreviousDuration: req.Plan.Duration}

	return e.submit(ctx, job)
}

func refusal(outcome *editplan.Outcome) error {
	return fmt.Errorf("%w: %s: %s", errRevisionRefused, outcome.Kind, outcome.Reason)
}

// decide asks the model. A model that refused is an outcome without an edit (a revision not understood). A run that
// failed on our side (max_tokens, too many steps, a broken response) is technical: «Техничка» is told, a revision
// fails with the technical text, the first version is cut automatically. The API that refuses the key or the request
// is a technical failure; anything else (the network, 429, 5xx) is retried by the task.
func (e *Executor) decide(ctx context.Context, data *tasks.ExecuteData, req *editplan.Request) (*editplan.Outcome, error) {
	outcome, err := e.decider.Decide(ctx, req)
	if err == nil {
		return outcome, nil
	}

	var apiErr *anthropic.APIError

	switch {
	case errors.Is(err, agents.ErrRefusal):
		e.log.WarnContext(ctx, "editor: the model refused to decide", "task_id", data.TaskID, "error", err)

		return &editplan.Outcome{Kind: editplan.NoAnswerOutcome, Reason: err.Error()}, nil
	case errors.Is(err, agents.ErrMaxTokens) || errors.Is(err, agents.ErrIterationLimit) ||
		errors.Is(err, agents.ErrInvalidResponse):
		if data.Version > 1 {
			return nil, e.technical(ctx, data, modelFailedCode, err)
		}

		e.report(ctx, data, modelFailedCode, err)

		return &editplan.Outcome{Kind: editplan.FailedOutcome, Reason: err.Error()}, nil
	case errors.Is(err, anthropic.ErrAPIKeyMissing) || errors.Is(err, agents.ErrAgentNotFound) ||
		errors.Is(err, editplan.ErrInvalidRequest) || (errors.As(err, &apiErr) && !apiErr.Retryable()):
		return nil, e.technical(ctx, data, modelUnavailableCode, err)
	default:
		return nil, &tasks.TemporaryError{Message: forVersion(data.Version, modelFailedText, modelFailedRevisionText), Err: err}
	}
}

// loadRequest reads the transcript of the source and the plan the decision starts from. A missing file cannot be
// fixed by a retry: the trainer sends the source again.
func (e *Executor) loadRequest(
	ctx context.Context,
	data *tasks.ExecuteData,
	transcriptKey, planKey string,
) (*editplan.Request, error) {
	var (
		transcript transcription.Result
		plan       roughcut.Plan
	)

	for _, f := range []struct {
		key string
		v   any
	}{{key: transcriptKey, v: &transcript}, {key: planKey, v: &plan}} {
		if err := e.readJSON(ctx, f.key, f.v); err != nil {
			return nil, err
		}
	}

	if len(transcript.Words) == 0 || len(plan.Keep) == 0 {
		return nil, &tasks.PermanentError{Message: artifactsMissingText,
			Err: fmt.Errorf("task %d: empty transcript or plan: %w", data.TaskID, errArtifactMissing)}
	}

	return &editplan.Request{TaskID: data.TaskID, Version: data.Version, Transcript: &transcript, Plan: &plan}, nil
}

func (e *Executor) readJSON(ctx context.Context, key string, v any) error {
	raw, err := e.store.Read(ctx, key, maxArtifactSize)

	switch {
	case errors.Is(err, media.ErrFileNotFound) || errors.Is(err, media.ErrInvalidKey) || errors.Is(err, media.ErrFileTooLarge):
		return &tasks.PermanentError{Message: artifactsMissingText, Err: fmt.Errorf("%s: %w: %w", key, errArtifactMissing, err)}
	case err != nil:
		return fmt.Errorf("failed to read %s: %w", key, err)
	}

	if err := json.Unmarshal(raw, v); err != nil {
		return &tasks.PermanentError{Message: artifactsMissingText, Err: fmt.Errorf("failed to decode %s: %w", key, err)}
	}

	return nil
}

// wait defers the run of a submitted job, or fails it: it ran out of time since a worker took it, it waited in the
// queue too long, or no worker is running at all.
func (e *Executor) wait(ctx context.Context, data *tasks.ExecuteData, job *editjob.Job) error {
	now := e.now()

	started, found, err := e.board.Started(ctx, job.ID)
	if err != nil {
		return err
	}

	if found {
		deadline := started.Add(e.jobTimeout + startGrace)
		if !now.Before(deadline) {
			e.forget(ctx, job.ID)

			return &tasks.PermanentError{Message: runningTimeoutText(e.jobTimeout), Err: timeoutError(job.ID, "running")}
		}

		return &tasks.DeferredError{After: min(e.pollInterval, deadline.Sub(now))}
	}

	if !now.Before(job.QueueDeadline) {
		e.forget(ctx, job.ID)

		return &tasks.PermanentError{Message: queueTimeoutText(e.queueTimeout), Err: timeoutError(job.ID, "queue")}
	}

	if now.Sub(job.CreatedAt) >= e.workerWait {
		if err := e.checkWorker(ctx, data, job.ID); err != nil {
			return err
		}
	}

	return &tasks.DeferredError{After: min(e.pollInterval, job.QueueDeadline.Sub(now))}
}

// checkWorker fails a job nobody took when no worker has sent a heartbeat: the media-worker service is not running.
func (e *Executor) checkWorker(ctx context.Context, data *tasks.ExecuteData, id string) error {
	alive, err := e.board.WorkerAlive(ctx)
	if err != nil {
		return err
	}

	locked, err := e.board.Locked(ctx, id)
	if err != nil {
		return err
	}

	if alive || locked {
		return nil
	}

	e.forget(ctx, id)

	return e.technical(ctx, data, workerUnavailableCode, fmt.Errorf("job %s is not taken: %w", id, errWorkerUnavailable))
}

func timeoutError(id, phase string) error {
	return fmt.Errorf("job %s: %s timeout: %w", id, phase, context.DeadlineExceeded)
}

// submit gives the job to the worker and defers the run.
func (e *Executor) submit(ctx context.Context, job *editjob.Job) error {
	submitted, err := e.board.Submit(ctx, job)
	if err != nil {
		return fmt.Errorf("failed to submit edit job: %w", err)
	}

	if submitted {
		e.log.InfoContext(ctx, "editor: job submitted", "job_id", job.ID, "task_id", job.TaskID, "stage", job.Stage,
			"queue_deadline", job.QueueDeadline)
	}

	return &tasks.DeferredError{After: e.pollInterval}
}

// finish turns the worker's result into the draft or the failure. A failed job is forgotten, so a retry of the task
// submits it again; a finished one is kept (a repeated run of the task finds the same result).
func (e *Executor) finish(ctx context.Context, data *tasks.ExecuteData, result *editjob.Result) (tasks.Output, error) {
	if result.Status == editjob.DoneStatus {
		files, err := editjob.Files(e.store, data.Media[0], data.Version)
		if err != nil {
			return tasks.Output{}, e.technical(ctx, data, resultKeysCode, err)
		}

		// The summary of a decided version is kept in its job; a job forgotten by Redis leaves only the numbers.
		var summary *editjob.Summary
		if job, found, err := e.board.Job(ctx, result.JobID); err == nil && found {
			summary = job.Summary
		}

		e.log.InfoContext(ctx, "editor: reel ready", "task_id", data.TaskID, "job_id", result.JobID, "key", files.Reel,
			"source_duration", result.SourceDuration, "duration", result.Duration, "audio_seconds", result.AudioSeconds)

		return tasks.Output{Text: Describe(result, data.Version, data.Input, summary), MediaKey: files.Reel}, nil
	}

	err := fmt.Errorf("edit job %s failed: %s", result.JobID, result.Code)
	temporary := &tasks.TemporaryError{Message: forVersion(data.Version, editFailedText, editFailedRevisionText), Err: err}

	// A decided render is queued again as it is: no new transcription, no new decision of the model.
	if result.Code == editjob.TemporaryCode && e.retryRender(ctx, result.JobID) {
		return tasks.Output{}, temporary
	}

	e.forget(ctx, result.JobID)

	switch {
	case result.Code == editjob.TemporaryCode:
		return tasks.Output{}, temporary
	case technicalCodes[result.Code]:
		return tasks.Output{}, e.technical(ctx, data, string(result.Code), err)
	default:
		return tasks.Output{}, &tasks.PermanentError{Message: e.failureText(result), Err: err}
	}
}

// retryRender queues the render stage of the job again with new deadlines, in place of its failed result; false
// when the job is not a render (an auto job starts over) or could not be queued (then it is forgotten).
func (e *Executor) retryRender(ctx context.Context, id string) bool {
	job, found, err := e.board.Job(ctx, id)
	if err != nil || !found || job.Stage != editjob.RenderStage {
		return false
	}

	now := e.now()
	job.CreatedAt, job.QueueDeadline, job.Timeout = now, now.Add(e.queueTimeout), e.jobTimeout

	retried, err := e.board.Retry(ctx, &job)
	if err != nil {
		e.log.ErrorContext(ctx, "editor: failed to queue the render again", "error", err, "job_id", id)

		return false
	}

	if retried {
		e.log.InfoContext(ctx, "editor: render queued again after a temporary failure", "job_id", id)
	}

	return retried
}

// technicalCodes are failures of the setup, not of the video: the trainer gets technicalText, the team the code.
var technicalCodes = map[editjob.FailureCode]bool{
	editjob.NotConfiguredCode:         true,
	editjob.TranscriptionRejectedCode: true,
	editjob.FontsCode:                 true,
	editjob.InvalidEditCode:           true,
}

// technical fails the task with the plain text for the trainer and tells the team the code in «Техничка».
func (e *Executor) technical(ctx context.Context, data *tasks.ExecuteData, code string, cause error) error {
	e.report(ctx, data, code, cause)

	return &tasks.PermanentError{Message: forVersion(data.Version, technicalText, technicalRevisionText), Err: cause}
}

// report logs a technical failure and tells the team the code in «Техничка».
func (e *Executor) report(ctx context.Context, data *tasks.ExecuteData, code string, cause error) {
	e.log.ErrorContext(ctx, "editor: editing is unavailable", "task_id", data.TaskID, "code", code, "error", cause)

	if err := e.tech.Report(ctx, AgentName, techReportText(data.TaskID, code)); err != nil {
		e.log.ErrorContext(ctx, "editor: failed to report to «Техничка»", "error", err, "task_id", data.TaskID)
	}
}

func (e *Executor) forget(ctx context.Context, id string) {
	if err := e.board.Forget(ctx, id); err != nil {
		e.log.ErrorContext(ctx, "editor: failed to forget the job, a retry finds its old result", "error", err, "job_id", id)
	}
}
