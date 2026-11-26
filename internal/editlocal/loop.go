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
// cancels a job that is not there (T3.14), so nothing goes on rendering for nobody. A killed run (docker kill) cannot
// do that: its job carries the lease of the run (LeaseBoard), refreshed while the run waits (and before each run of the
// executor), and the worker drops the job once the lease expires; the next run of the version takes the job of the
// killed one off the board first. A run whose lease another run took stops with ErrLeaseTaken and leaves the job.
func (r *Runner) execute(ctx context.Context, data *tasks.ExecuteData, timings *Timings) (tasks.Output, error) {
	id := editjob.JobID(data.TaskID, data.Version)

	held, runCtx, err := r.holdLease(ctx, id)
	if err != nil {
		return tasks.Output{}, err
	}
	defer held.release()

	r.dropStale(runCtx, data, held.token)

	wake, stop := r.listen(runCtx, data.TaskID)
	defer stop()

	output, err := r.loop(runCtx, data, timings, wake, held)

	switch {
	case err == nil:
		return output, nil
	case errors.Is(context.Cause(runCtx), ErrLeaseTaken):
		return tasks.Output{}, fmt.Errorf("failed to finish the edit of %s: %w", id, ErrLeaseTaken)
	case ctx.Err() != nil:
		r.forget(ctx, id)
	}

	return output, err
}

func (r *Runner) loop(
	ctx context.Context,
	data *tasks.ExecuteData,
	timings *Timings,
	wake <-chan struct{},
	held *lease,
) (tasks.Output, error) {
	run := *data
	attempt := 1
	current := r.stage(ctx, &run)

	r.progressf("задача %d: %s", run.TaskID, stageText(current))

	for {
		start, model := r.now(), r.meter.Snapshot().ModelTime
		run.LastAttempt = attempt >= r.settings.MaxAttempts

		// The lease is renewed before each run: a run that slept past the TTL takes it again before it submits.
		if err := held.renew(ctx); errors.Is(err, ErrLeaseTaken) {
			return tasks.Output{}, err
		}

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
	switch s {
	case renderStage:
		return "воркер рендерит"
	case decideStage:
		return "Монтажёр решает правку"
	default:
		return "воркер: очередь, транскрипция и план"
	}
}

// stage is the stage of the job of the version on the board. Before it is submitted: the plan of the first version
// (it is submitted at once), the decision of the model for a revision (the render is submitted only after it, and a
// refused revision has none).
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
		return decideStage
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
