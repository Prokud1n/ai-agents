package tasks

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
	"time"
)

// revision is what a revision run needs besides the task: the previous version and the trainer's text.
type revision struct {
	previous *Version
	feedback *Feedback
}

// Run runs the task once: starts it, runs the agent and stores the outcome (a new version awaiting
// approval, a retry, or a given-up run). A message for a task that is not runnable (a duplicate,
// a decided task) is skipped with nil. The outcome is stored even if ctx is cancelled after the agent
// answered. An error means the outcome could not be stored: the task was returned to the queue if
// possible, and the caller schedules it again.
func (s *Service) Run(ctx context.Context, taskID int64) error {
	task, err := s.repo.Update(ctx, taskID, UpdateToRunning(s.now(), s.staleAfter(), s.cfg.MaxAttempts, rand.Text()))
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		s.log.DebugContext(ctx, "tasks: skipping task that is not runnable", "task_id", taskID, "reason", err.Error())

		return nil
	}

	if err != nil {
		return err
	}

	if task.Status != RunningStatus { // its last attempt died: given up instead of started
		s.log.ErrorContext(ctx, "tasks: run given up: attempts died without finishing", "task_id", task.ID,
			"attempts", task.Attempts, "status", task.Status)
		s.afterGiveUp(ctx, &task, staleRunReason)

		return nil
	}

	s.log.InfoContext(ctx, "tasks: running task",
		"task_id", task.ID, "agent", task.Agent, "version", task.Version, "attempt", task.Attempts)

	output, runErr := s.runAgent(ctx, &task)

	finalCtx, cancel := finalContext(ctx)
	defer cancel()

	if runErr == nil {
		err = s.completeRun(ctx, finalCtx, &task, output)
	} else {
		err = s.handleRunError(ctx, finalCtx, &task, runErr)
	}

	if err != nil {
		s.release(finalCtx, &task, err)
	}

	return err
}

// runAgent loads the revision context and runs the executor; a panic in the executor is an error.
func (s *Service) runAgent(ctx context.Context, task *Task) (Output, error) {
	rev, err := s.loadRevision(ctx, task)
	if err != nil {
		return Output{}, err
	}

	return s.execute(ctx, task, rev)
}

// loadRevision loads the previous version and its revision for a task that already has a version
// (only a revision brings such a task back to the queue).
func (s *Service) loadRevision(ctx context.Context, task *Task) (revision, error) {
	if task.Version == 0 {
		return revision{}, nil
	}

	previous, err := s.repo.GetVersion(ctx, task.ID, task.Version)
	if err != nil {
		return revision{}, err
	}

	feedback, err := s.repo.LatestFeedback(ctx, task.ID, task.Version, ReviseFeedbackKind)
	if err != nil {
		return revision{}, err
	}

	return revision{previous: &previous, feedback: &feedback}, nil
}

func (s *Service) execute(ctx context.Context, task *Task, rev revision) (output Output, err error) {
	runCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	defer func() {
		if r := recover(); r != nil {
			s.log.ErrorContext(ctx, "tasks: executor panicked",
				"task_id", task.ID, "agent", task.Agent, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))

			output, err = Output{}, fmt.Errorf("failed to run agent %q: %w: %v", task.Agent, errExecutorPanic, r)
		}
	}()

	output, err = s.executor.Execute(runCtx, &ExecuteData{
		TaskID:  task.ID,
		Agent:   task.Agent,
		Kind:    task.Kind,
		Input:   task.Input,
		Media:   slices.Clone(task.Media),
		Source:  task.Source,
		Version: task.Version + 1,
		Prompt:  BuildPrompt(task, rev.previous, rev.feedback),
	})
	if err != nil {
		return Output{}, err
	}

	if strings.TrimSpace(output.Text) == "" {
		return Output{}, fmt.Errorf("failed to run agent %q: empty answer", task.Agent)
	}

	return output, nil
}

// completeRun stores the new version (writes with finalCtx) and sends its draft. A video draft is sent with the run's
// context: its upload may take minutes and is interrupted by stop (the sweeper sends it after the restart).
func (s *Service) completeRun(ctx, finalCtx context.Context, task *Task, output Output) error {
	updated, err := s.repo.Update(finalCtx, task.ID, UpdateToNewVersion(task.RunID, output, s.now()))
	if errors.Is(err, ErrConflict) {
		s.log.WarnContext(finalCtx, "tasks: run is no longer current, result dropped", "task_id", task.ID, "error", err)

		return nil
	}

	if err != nil {
		return err
	}

	s.log.InfoContext(finalCtx, "tasks: draft ready", "task_id", updated.ID, "version", updated.Version,
		"video", output.MediaKey != "")
	s.deliver(ctx, updated.ID, updated.Version)

	return nil
}

// handleRunError decides what a failed run means: interrupted by stop (ctx cancelled: back to the queue,
// the attempt is not counted), permanent or out of attempts (given up), otherwise a retry with backoff.
// Writes use finalCtx.
func (s *Service) handleRunError(ctx, finalCtx context.Context, task *Task, runErr error) error {
	if ctx.Err() != nil {
		return s.interrupt(finalCtx, task)
	}

	var deferred *DeferredError
	if errors.As(runErr, &deferred) {
		return s.deferRun(finalCtx, task, deferred.After)
	}

	var permanent *PermanentError
	if errors.As(runErr, &permanent) || task.Attempts >= s.cfg.MaxAttempts {
		return s.giveUpRun(finalCtx, task, runErr)
	}

	now := s.now()
	runAfter := now.Add(Backoff(task.Attempts, s.cfg.RetryBaseDelay, s.cfg.RetryMaxDelay))

	_, err := s.repo.Update(finalCtx, task.ID, UpdateToRetry(task.RunID, runErr.Error(), runAfter, now))
	if errors.Is(err, ErrConflict) {
		return nil // the run was taken over: its owner decides
	}

	if err != nil {
		return err
	}

	s.log.WarnContext(finalCtx, "tasks: run failed, will retry",
		"error", runErr, "task_id", task.ID, "attempt", task.Attempts, "run_after", runAfter)
	s.schedule(finalCtx, task.ID, runAfter)

	return nil
}

// deferRun puts the task back to the queue while its work goes on elsewhere; the attempt is not counted.
func (s *Service) deferRun(ctx context.Context, task *Task, after time.Duration) error {
	now := s.now()
	runAfter := now.Add(max(after, 0))

	_, err := s.repo.Update(ctx, task.ID, UpdateToDeferred(task.RunID, runAfter, now))
	if errors.Is(err, ErrConflict) {
		return nil
	}

	if err != nil {
		return err
	}

	s.log.DebugContext(ctx, "tasks: run deferred", "task_id", task.ID, "run_after", runAfter)
	s.schedule(ctx, task.ID, runAfter)

	return nil
}

// Wake runs a deferred task of the agent now: its work elsewhere has finished. A task of another agent, or one that
// is not waiting in the queue (running, decided, unknown), is left alone.
func (s *Service) Wake(ctx context.Context, taskID int64, agent string) error {
	now := s.now()

	_, err := s.repo.Update(ctx, taskID, UpdateToWoken(agent, now))
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		return nil
	}

	if err != nil {
		return err
	}

	s.schedule(ctx, taskID, now)

	return nil
}

// giveUpRun ends a run that will not be retried: see giveUp.
func (s *Service) giveUpRun(ctx context.Context, task *Task, runErr error) error {
	reason := failureReason(runErr, task.Attempts)

	updated, err := s.repo.Update(ctx, task.ID, UpdateToRunFailed(task.RunID, runErr.Error(), reason, s.now()))
	if errors.Is(err, ErrConflict) {
		return nil
	}

	if err != nil {
		return err
	}

	s.log.ErrorContext(ctx, "tasks: run given up", "error", runErr, "task_id", task.ID,
		"attempt", task.Attempts, "status", updated.Status)
	s.afterGiveUp(ctx, &updated, reason)

	return nil
}

// afterGiveUp tells the trainer: a failed task gets a failure message, a failed revision sends the
// previous draft again with the reason (stored with the version as its note).
func (s *Service) afterGiveUp(ctx context.Context, task *Task, reason string) {
	if task.Status == FailedStatus {
		s.notifyFailure(ctx, task, failedTaskMessage(reason))

		return
	}

	s.deliver(ctx, task.ID, task.Version)
}

// interrupt returns the task to the queue after the worker's context was cancelled (stop). The sweeper
// queues it on the next start.
func (s *Service) interrupt(ctx context.Context, task *Task) error {
	_, err := s.repo.Update(ctx, task.ID, UpdateToInterrupted(task.RunID, s.now()))
	if err != nil && !errors.Is(err, ErrConflict) {
		return err
	}

	s.log.InfoContext(ctx, "tasks: run interrupted, task returned to the queue", "task_id", task.ID)

	return nil
}

// release returns a started task to the queue when its outcome could not be stored, so it does not wait
// for the sweeper as a stale run. If even that fails (the database is down), the sweeper restarts it.
func (s *Service) release(ctx context.Context, task *Task, cause error) {
	now := s.now()
	runAfter := now.Add(s.cfg.RetryBaseDelay)

	_, err := s.repo.Update(ctx, task.ID,
		UpdateToRetry(task.RunID, "failed to store the run outcome: "+cause.Error(), runAfter, now))
	if err != nil {
		s.log.ErrorContext(ctx, "tasks: failed to release task", "error", err, "task_id", task.ID)

		return
	}

	s.schedule(ctx, task.ID, runAfter)
}

// schedule puts the task into the delayed queue; a failure is logged: the sweeper finds the task.
func (s *Service) schedule(ctx context.Context, taskID int64, at time.Time) {
	if err := s.queue.EnqueueAt(ctx, taskID, at); err != nil {
		s.log.ErrorContext(ctx, "tasks: failed to schedule task, the sweeper will queue it", "error", err, "task_id", taskID)
	}
}

// Sweep queues what the queue lost; PostgreSQL is the source of truth. On start every due queued task is
// queued again (an interrupted one, or one never queued); later only tasks last queued more than the stale
// time ago are, so a busy queue does not get a duplicate every sweep. Running tasks older than the stale
// time (dead worker) are queued too, and drafts never sent are delivered.
func (s *Service) Sweep(ctx context.Context, startup bool) error {
	now := s.now()
	staleBefore := now.Add(-s.staleAfter())

	queuedBefore := staleBefore
	if startup {
		queuedBefore = now
	}

	ids, err := s.repo.ClaimLost(ctx, now, queuedBefore, staleBefore)
	if err != nil {
		return err
	}

	for _, id := range ids {
		// The delayed set keeps one entry per task, so a sweep never adds a duplicate to the stream.
		if err := s.queue.EnqueueAt(ctx, id, now); err != nil {
			return err
		}
	}

	drafts, err := s.repo.FindUndelivered(ctx, now)
	if err != nil {
		return err
	}

	for _, d := range drafts {
		s.deliver(ctx, d.TaskID, d.Version)
	}

	if len(ids)+len(drafts) > 0 {
		s.log.InfoContext(ctx, "tasks: sweeper queued tasks", "tasks", len(ids), "drafts", len(drafts))
	}

	return nil
}
