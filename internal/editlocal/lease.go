package editlocal

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"github.com/Prokud1n/ai-agents/internal/editjob"
)

const (
	// DefaultLeaseTTL of the lease of the job a run waits for: the worker drops the job of a killed run (docker kill)
	// at most this long (plus its board check) after the kill.
	DefaultLeaseTTL = 20 * time.Second
	// DefaultLeaseRenew: how often a waiting run refreshes its lease; a few refreshes may fail within the TTL.
	DefaultLeaseRenew = 5 * time.Second
	// leaseReleaseTimeout bounds the release of the lease after the run, made even when the run was interrupted.
	leaseReleaseTimeout = 5 * time.Second
)

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

func NewLeaseBoard(board *editjob.Board) *LeaseBoard {
	return &LeaseBoard{Board: board}
}

func (b *LeaseBoard) Submit(ctx context.Context, job *editjob.Job) (bool, error) {
	return b.Board.Submit(ctx, leasedJob(ctx, job))
}

func (b *LeaseBoard) Replace(ctx context.Context, job *editjob.Job) (bool, error) {
	return b.Board.Replace(ctx, leasedJob(ctx, job))
}

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

// lease is the lease a run holds on its job while it waits; release stops the refresh and deletes it.
type lease struct {
	token   string
	release func()
}

// holdLease takes the lease of the job for a new token and refreshes it until release. A failed refresh is only
// logged: the next one may succeed within the TTL.
func (r *Runner) holdLease(ctx context.Context, jobID string) (*lease, error) {
	token := rand.Text()

	if err := r.board.Lease(ctx, jobID, token, r.settings.LeaseTTL); err != nil {
		return nil, fmt.Errorf("failed to take the lease of the job: %w", err)
	}

	renewCtx, cancel := context.WithCancel(ctx)

	var done sync.WaitGroup

	done.Go(func() { r.renewLease(renewCtx, jobID, token) })

	return &lease{token: token, release: func() {
		cancel()
		done.Wait()

		releaseCtx, cancelRelease := context.WithTimeout(context.WithoutCancel(ctx), leaseReleaseTimeout)
		defer cancelRelease()

		if err := r.board.ReleaseLease(releaseCtx, jobID, token); err != nil {
			r.log.WarnContext(releaseCtx, "editlocal: failed to release the lease, it expires by itself", "error", err,
				"job_id", jobID)
		}
	}}, nil
}

func (r *Runner) renewLease(ctx context.Context, jobID, token string) {
	ticker := time.NewTicker(r.settings.LeaseRenew)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.board.Lease(ctx, jobID, token, r.settings.LeaseTTL); err != nil && ctx.Err() == nil {
				r.log.WarnContext(ctx, "editlocal: failed to refresh the lease", "error", err, "job_id", jobID)
			}
		}
	}
}

// dropStale takes off the board a job of the version left by an earlier run that was killed (its lease is another
// token): nobody would run it, and the executor would wait for it until its timeout.
func (r *Runner) dropStale(ctx context.Context, jobID, token string) {
	job, found, err := r.board.Job(ctx, jobID)
	if err != nil {
		r.log.WarnContext(ctx, "editlocal: failed to read the job", "error", err, "job_id", jobID)

		return
	}

	if !found || job.Lease == "" || job.Lease == token {
		return
	}

	if err := r.board.Forget(ctx, jobID); err != nil {
		r.log.WarnContext(ctx, "editlocal: failed to take the job of a killed run off the board", "error", err,
			"job_id", jobID)

		return
	}

	r.progressf("задание %s прерванного запуска снято с доски, монтирую заново", jobID)
}
