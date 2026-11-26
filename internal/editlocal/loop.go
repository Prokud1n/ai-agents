package editlocal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	// minWait between two runs of the executor: a deferred run without a delay does not spin.
	minWait = 100 * time.Millisecond
	// forgetTimeout bounds taking the job off the board after the run was interrupted.
	forgetTimeout = 5 * time.Second
)

// execute runs the executor as the task workers do until the version is made or fails: a deferred run waits (the
// done channel wakes it early), any other failure but a permanent one is retried while attempts are left. Each run
// is bounded by RunTimeout (TASKS_TIMEOUT). An interrupted edit (Ctrl+C) takes its job off the board: the worker
// cancels a job that is not there (T3.14), so nothing goes on rendering for nobody.
func (r *Runner) execute(ctx context.Context, data *tasks.ExecuteData, timings *Timings) (tasks.Output, error) {
	wake, stop := r.listen(ctx, data.TaskID)
	defer stop()

	output, err := r.loop(ctx, data, timings, wake)
	if err != nil && ctx.Err() != nil {
		r.forget(ctx, editjob.JobID(data.TaskID, data.Version))
	}

	return output, err
}

func (r *Runner) loop(
	ctx context.Context,
	data *tasks.ExecuteData,
	timings *Timings,
	wake <-chan struct{},
) (tasks.Output, error) {
	run := *data
	attempt := 1
	current := r.stage(ctx, &run)

	r.progressf("задача %d: %s", run.TaskID, stageText(current))

	for {
		start, model := r.now(), r.meter.Snapshot().ModelTime
		run.LastAttempt = attempt >= r.settings.MaxAttempts

		output, err := r.runOnce(ctx, &run)
		timings.add(current, r.now().Sub(start), r.meter.Snapshot().ModelTime-model)

		if err == nil {
			return output, nil
		}

		delay, err := r.retryDelay(ctx, err, &attempt)
		if err != nil {
			return tasks.Output{}, err
		}

		// The run may have moved the job on (submitted, replaced by its render): the wait is of the new stage.
		if next := r.stage(ctx, &run); next != current {
			current = next
			r.progressf("задача %d: %s", run.TaskID, stageText(current))
		}

		waitStart := r.now()
		err = wait(ctx, delay, wake)
		timings.add(current, r.now().Sub(waitStart), 0)

		if err != nil {
			return tasks.Output{}, err
		}
	}
}

// runOnce runs the executor once within RunTimeout.
func (r *Runner) runOnce(ctx context.Context, run *tasks.ExecuteData) (tasks.Output, error) {
	runCtx, cancel := context.WithTimeout(ctx, r.settings.RunTimeout)
	defer cancel()

	return r.executor.Execute(runCtx, run)
}

func (r *Runner) forget(ctx context.Context, id string) {
	forgetCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), forgetTimeout)
	defer cancel()

	if err := r.board.Forget(forgetCtx, id); err != nil {
		r.log.ErrorContext(forgetCtx, "editlocal: failed to take the interrupted job off the board", "error", err,
			"job_id", id)

		return
	}

	r.progressf("монтаж остановлен, задание %s снято с доски", id)
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
// attempt; a permanent error, a failure of the last attempt and an interrupted edit end it.
func (r *Runner) retryDelay(ctx context.Context, err error, attempt *int) (time.Duration, error) {
	if ctx.Err() != nil {
		return 0, fmt.Errorf("failed to finish the edit: %w", errors.Join(ctx.Err(), err))
	}

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
