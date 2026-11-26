package editlocal

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	// DefaultLeaseTTL of the lease of the job a run waits for: the worker drops the job of a killed run (docker kill)
	// at most this long plus its board check (3 s) after the kill — up to 25 s with the jitter.
	DefaultLeaseTTL = 20 * time.Second
	// DefaultLeaseRenew: how often a waiting run refreshes its lease; a few refreshes may fail within the TTL.
	DefaultLeaseRenew = 5 * time.Second
	// leaseReleaseTimeout bounds the release of the lease after the run, made even when the run was interrupted.
	leaseReleaseTimeout = 5 * time.Second
)

// ErrLeaseTaken: another run of make edit took the lease of the version over; this one stops and leaves the job to it.
var ErrLeaseTaken = errors.New("another run edits this version")

type leaseContextKey struct{}

// withLease puts the lease token of the run into ctx: LeaseBoard marks the jobs submitted with it.
func withLease(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, leaseContextKey{}, token)
}

// leaseToken is the lease token of the run in ctx; empty outside a run.
func leaseToken(ctx context.Context) string {
	token, _ := ctx.Value(leaseContextKey{}).(string)

	return token
}

// LeaseBoard is the board of the editor's executor in a local edit: every job it submits, replaces or queues again
// carries the lease token of the run (editjob.Job.Lease), so the worker drops it once the run is gone. Outside a run
// (no token in the context) the jobs go as they are.
type LeaseBoard struct {
	*editjob.Board
}

// NewLeaseBoard wraps the board of the executor of make edit (see LeaseBoard).
func NewLeaseBoard(board *editjob.Board) *LeaseBoard {
	return &LeaseBoard{Board: board}
}

// Submit is editjob.Board.Submit of the job with the lease token of the run.
func (b *LeaseBoard) Submit(ctx context.Context, job *editjob.Job) (bool, error) {
	return b.Board.Submit(ctx, leasedJob(ctx, job))
}

// Replace is editjob.Board.Replace of the job with the lease token of the run.
func (b *LeaseBoard) Replace(ctx context.Context, job *editjob.Job) (bool, error) {
	return b.Board.Replace(ctx, leasedJob(ctx, job))
}

// Retry is editjob.Board.Retry of the job with the lease token of the run.
func (b *LeaseBoard) Retry(ctx context.Context, job *editjob.Job) (bool, error) {
	return b.Board.Retry(ctx, leasedJob(ctx, job))
}

// leasedJob is a copy of the job with the lease token of ctx; the job itself without a token.
func leasedJob(ctx context.Context, job *editjob.Job) *editjob.Job {
	token := leaseToken(ctx)
	if token == "" {
		return job
	}

	leased := *job
	leased.Lease = token

	return &leased
}

// lease is the lease a run holds on its job while it waits.
type lease struct {
	board board
	log   *slog.Logger
	jobID string
	token string
	ttl   time.Duration
	// lost cancels the run's context with ErrLeaseTaken.
	lost context.CancelCauseFunc
	// release stops the refresh and deletes the lease if it is still the run's.
	release func()
}

// renew extends the lease, or takes it again if it expired (the run slept); ErrLeaseTaken, with the run cancelled,
// when another run holds it: the lease is never taken over by a refresh.
func (l *lease) renew(ctx context.Context) error {
	held, err := l.board.RenewLease(ctx, l.jobID, l.token, l.ttl)
	if err != nil {
		return fmt.Errorf("failed to renew the lease: %w", err)
	}

	if !held {
		l.lost(ErrLeaseTaken)

		return fmt.Errorf("lease of job %s: %w", l.jobID, ErrLeaseTaken)
	}

	return nil
}

// keep renews the lease every interval until ctx is done or another run holds it; a failed refresh is only logged:
// the next one may succeed within the TTL.
func (l *lease) keep(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := l.renew(ctx)
			if errors.Is(err, ErrLeaseTaken) {
				l.log.WarnContext(ctx, "editlocal: another run took the lease of the job, stopping", "job_id", l.jobID)

				return
			}

			if err != nil && ctx.Err() == nil {
				l.log.WarnContext(ctx, "editlocal: failed to refresh the lease", "error", err, "job_id", l.jobID)
			}
		}
	}
}

// holdLease takes the lease of the job for a new token (over the lease of a killed run: the lock of the output
// directory keeps a second live run out) and renews it until release. The returned context carries the token and is
// cancelled with ErrLeaseTaken if another run takes the lease.
func (r *Runner) holdLease(ctx context.Context, jobID string) (*lease, context.Context, error) {
	token := rand.Text()

	if err := r.board.Lease(ctx, jobID, token, r.settings.LeaseTTL); err != nil {
		return nil, nil, fmt.Errorf("failed to take the lease of the job: %w", err)
	}

	runCtx, lost := context.WithCancelCause(ctx)
	renewCtx, stopRenew := context.WithCancel(runCtx)
	l := &lease{board: r.board, log: r.log, jobID: jobID, token: token, ttl: r.settings.LeaseTTL, lost: lost}

	var done sync.WaitGroup

	done.Go(func() { l.keep(renewCtx, r.settings.LeaseRenew) })

	l.release = func() {
		stopRenew()
		done.Wait()
		lost(nil)

		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), leaseReleaseTimeout)
		defer cancel()

		if err := r.board.ReleaseLease(releaseCtx, jobID, token); err != nil {
			r.log.WarnContext(releaseCtx, "editlocal: failed to release the lease, it expires by itself", "error", err,
				"job_id", jobID)
		}
	}

	return l, withLease(runCtx, token), nil
}

// dropStale takes off the board a job of the version left by an earlier run that was killed (its lease is another
// token): nobody would run it, and the executor would wait for it until its timeout. A job already done for the same
// text (caption or revision) stays: its reel is the one this run would make.
func (r *Runner) dropStale(ctx context.Context, data *tasks.ExecuteData, token string) {
	id := editjob.JobID(data.TaskID, data.Version)

	job, found, err := r.board.Job(ctx, id)
	if err != nil {
		r.log.WarnContext(ctx, "editlocal: failed to read the job", "error", err, "job_id", id)

		return
	}

	if !found || job.Lease == "" || job.Lease == token || r.doneFor(ctx, &job, data) {
		return
	}

	if err := r.board.Forget(ctx, id); err != nil {
		r.log.WarnContext(ctx, "editlocal: failed to take the job of a killed run off the board", "error", err,
			"job_id", id)

		return
	}

	r.progressf("задание %s прерванного запуска снято с доски, монтирую заново", id)
}

// doneFor: the job has a done result and was made for the text of the run (editjob.Job.RevisionHash, as the
// executor computes it: the revision, the caption of the first version).
func (r *Runner) doneFor(ctx context.Context, job *editjob.Job, data *tasks.ExecuteData) bool {
	text := data.Input
	if data.Version > 1 {
		text = data.Revision
	}

	if job.RevisionHash != editjob.TextHash(text) {
		return false
	}

	result, found, err := r.board.Result(ctx, job.ID)
	if err != nil {
		r.log.WarnContext(ctx, "editlocal: failed to read the result of the job", "error", err, "job_id", job.ID)

		return false
	}

	return found && result.Status == editjob.DoneStatus
}
