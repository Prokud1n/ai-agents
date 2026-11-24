package editor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// listenPause: after the subscription fails (Redis is down) the waker tries again after it.
const listenPause = 5 * time.Second

type doneListener interface {
	ListenDone(ctx context.Context, handle func(ctx context.Context, taskID int64)) error
}

type taskWaker interface {
	Wake(ctx context.Context, taskID int64, agent string) error
}

// Waker runs the task of a finished job as soon as the worker announces it, instead of waiting for the next poll
// (EDITOR_POLL_INTERVAL). Announcements are best effort: a lost one only delays the draft until the poll.
type Waker struct {
	board  doneListener
	tasks  taskWaker
	log    *slog.Logger
	cancel context.CancelFunc
	done   sync.WaitGroup
}

func NewWaker(board doneListener, tasks taskWaker, log *slog.Logger) *Waker {
	return &Waker{board: board, tasks: tasks, log: log}
}

// Start listens in the background; the listener outlives the start context.
func (w *Waker) Start(ctx context.Context) error {
	listenCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	w.cancel = cancel

	w.done.Go(func() { w.listen(listenCtx) })

	return nil
}

// Stop ends the listener, at most until ctx is done.
func (w *Waker) Stop(ctx context.Context) error {
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
		return nil
	case <-ctx.Done():
		return fmt.Errorf("failed to stop editor waker: %w", ctx.Err())
	}
}

func (w *Waker) listen(ctx context.Context) {
	for ctx.Err() == nil {
		if err := w.board.ListenDone(ctx, w.wake); err != nil && ctx.Err() == nil {
			w.log.WarnContext(ctx, "editor: failed to listen for finished jobs, the tasks are polled", "error", err)
		}

		timer := time.NewTimer(listenPause)

		select {
		case <-ctx.Done():
			timer.Stop()

			return
		case <-timer.C:
		}
	}
}

func (w *Waker) wake(ctx context.Context, taskID int64) {
	// Only the editor's tasks: an ID from the channel cannot wake a task of another agent.
	if err := w.tasks.Wake(ctx, taskID, AgentName); err != nil {
		w.log.WarnContext(ctx, "editor: failed to wake the task, it is polled", "error", err, "task_id", taskID)

		return
	}

	w.log.DebugContext(ctx, "editor: task woken", "task_id", taskID)
}
