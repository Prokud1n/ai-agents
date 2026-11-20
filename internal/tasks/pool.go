package tasks

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/Prokud1n/ai-agents/internal/config"
)

const (
	moveInterval = time.Second     // how often due retries are moved to the stream
	errorPause   = 2 * time.Second // pause of a worker after a queue error, so a Redis outage is not a busy loop
	ackTimeout   = 5 * time.Second
	// stopReserve is left of the stop deadline after the grace and the final writes of interrupted runs.
	stopReserve = time.Second
)

type taskRunner interface {
	Run(ctx context.Context, taskID int64) error
	Sweep(ctx context.Context, startup bool) error
}

type workerQueue interface {
	Init(ctx context.Context) error
	Fetch(ctx context.Context, consumer string) (*message, error)
	Ack(ctx context.Context, messageID string) error
	EnqueueAt(ctx context.Context, taskID int64, at time.Time) error
	MoveDue(ctx context.Context, now time.Time) (int, error)
}

// Pool runs TASKS_WORKERS workers that take tasks from the queue, a mover of due retries and the sweeper.
//
// Stop: workers stop taking messages at once; running tasks get TASKS_SHUTDOWN_GRACE to finish (shortened
// so that the grace and the final writes fit the stop deadline, APP_STOP_TIMEOUT), then their context is
// cancelled and they return to the queue (the attempt is not counted) to be run again after the restart.
type Pool struct {
	cfg      config.Tasks
	runner   taskRunner
	queue    workerQueue
	log      *slog.Logger
	consumer string

	stopPolling context.CancelFunc
	cancelRuns  context.CancelFunc
	workers     sync.WaitGroup
	background  sync.WaitGroup
}

func NewPool(cfg *config.Tasks, runner taskRunner, queue workerQueue, log *slog.Logger) *Pool {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "app"
	}

	return &Pool{
		cfg:      *cfg,
		runner:   runner,
		queue:    queue,
		log:      log,
		consumer: host + "-" + strconv.Itoa(os.Getpid()),
	}
}

// Start creates the consumer group and starts the goroutines; they outlive the start context.
func (p *Pool) Start(ctx context.Context) error {
	if err := p.queue.Init(ctx); err != nil {
		return fmt.Errorf("failed to init task queue: %w", err)
	}

	base := context.WithoutCancel(ctx)
	pollCtx, stopPolling := context.WithCancel(base)
	runCtx, cancelRuns := context.WithCancel(base)
	p.stopPolling, p.cancelRuns = stopPolling, cancelRuns

	for i := range p.cfg.Workers {
		consumer := p.consumer + "-" + strconv.Itoa(i)

		p.workers.Go(func() { p.work(pollCtx, runCtx, consumer) })
	}

	p.background.Go(func() { p.moveDue(pollCtx) })
	p.background.Go(func() { p.sweep(pollCtx) })

	p.log.InfoContext(ctx, "tasks: workers started", "workers", p.cfg.Workers, "stream", p.cfg.Stream)

	return nil
}

// Stop stops taking tasks, waits for running ones up to the shutdown grace, then interrupts them.
func (p *Pool) Stop(ctx context.Context) error {
	if p.stopPolling == nil {
		return nil
	}

	p.stopPolling()

	grace := time.NewTimer(shutdownGrace(ctx, p.cfg.ShutdownGrace, time.Now()))
	defer grace.Stop()

	select {
	case <-waitDone(&p.workers):
	case <-grace.C:
		p.log.WarnContext(ctx, "tasks: interrupting running tasks on stop")
	case <-ctx.Done():
	}

	p.cancelRuns()

	select {
	case <-waitDone(&p.workers):
	case <-ctx.Done():
		return fmt.Errorf("failed to stop task workers: %w", ctx.Err())
	}

	select {
	case <-waitDone(&p.background):
	case <-ctx.Done():
		return fmt.Errorf("failed to stop task sweeper: %w", ctx.Err())
	}

	p.log.InfoContext(ctx, "tasks: workers stopped")

	return nil
}

// shutdownGrace is the configured grace, shortened to leave time for the final writes of interrupted
// runs (sideEffectTimeout) and a reserve before the stop deadline.
func shutdownGrace(ctx context.Context, configured time.Duration, now time.Time) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return configured
	}

	return max(min(configured, deadline.Sub(now)-sideEffectTimeout-stopReserve), 0)
}

func waitDone(wg *sync.WaitGroup) <-chan struct{} {
	done := make(chan struct{})

	go func() {
		wg.Wait()
		close(done)
	}()

	return done
}

func (p *Pool) work(pollCtx, runCtx context.Context, consumer string) {
	for pollCtx.Err() == nil {
		msg, err := p.queue.Fetch(pollCtx, consumer)
		if err != nil {
			if pollCtx.Err() == nil {
				p.log.ErrorContext(pollCtx, "tasks: failed to fetch from queue", "error", err, "consumer", consumer)
				sleep(pollCtx, errorPause)
			}

			continue
		}

		if msg != nil {
			p.handle(runCtx, msg)
		}
	}
}

// handle runs the task and acks the message. If the outcome could not be stored or the run panicked,
// the task is scheduled again (if that fails too, the sweeper finds it in PostgreSQL).
func (p *Pool) handle(ctx context.Context, msg *message) {
	if err := p.run(ctx, msg.TaskID); err != nil {
		p.log.ErrorContext(ctx, "tasks: failed to run task", "error", err, "task_id", msg.TaskID)

		if ctx.Err() == nil {
			if err := p.queue.EnqueueAt(ctx, msg.TaskID, time.Now().Add(p.cfg.RetryBaseDelay)); err != nil {
				p.log.ErrorContext(ctx, "tasks: failed to schedule task again", "error", err, "task_id", msg.TaskID)
			}
		}
	}

	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ackTimeout)
	defer cancel()

	if err := p.queue.Ack(ackCtx, msg.ID); err != nil {
		p.log.ErrorContext(ackCtx, "tasks: failed to ack message", "error", err, "message_id", msg.ID)
	}
}

// run runs the task; a panic is turned into an error with its stack in the log, so one task cannot
// crash the process.
func (p *Pool) run(ctx context.Context, taskID int64) (err error) {
	defer func() {
		if r := recover(); r != nil {
			p.log.ErrorContext(ctx, "tasks: task run panicked",
				"task_id", taskID, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))

			err = fmt.Errorf("failed to run task %d: panic: %v", taskID, r)
		}
	}()

	return p.runner.Run(ctx, taskID)
}

func (p *Pool) moveDue(ctx context.Context) {
	ticker := time.NewTicker(moveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if _, err := p.queue.MoveDue(ctx, now); err != nil && ctx.Err() == nil {
				p.log.ErrorContext(ctx, "tasks: failed to move due retries", "error", err)
			}
		}
	}
}

// sweep runs the startup sweep at once, then every TASKS_SWEEP_INTERVAL.
func (p *Pool) sweep(ctx context.Context) {
	ticker := time.NewTicker(p.cfg.SweepInterval)
	defer ticker.Stop()

	startup := true

	for {
		if err := p.sweepOnce(ctx, startup); err != nil && ctx.Err() == nil {
			p.log.ErrorContext(ctx, "tasks: sweep failed", "error", err, "startup", startup)
		} else if err == nil {
			startup = false
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// sweepOnce runs one sweep; a panic is turned into an error with its stack in the log, so the sweeper
// keeps running.
func (p *Pool) sweepOnce(ctx context.Context, startup bool) (err error) {
	defer func() {
		if r := recover(); r != nil {
			p.log.ErrorContext(ctx, "tasks: sweep panicked", "panic", fmt.Sprint(r), "stack", string(debug.Stack()))

			err = fmt.Errorf("failed to sweep: panic: %v", r)
		}
	}()

	return p.runner.Sweep(ctx, startup)
}

func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
