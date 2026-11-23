package editjob

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/redistest"
)

const (
	testOwner      = "worker-a"
	testBlock      = 100 * time.Millisecond
	integrationTTL = 300 * time.Millisecond
	waitFor        = 5 * time.Second
	tick           = 20 * time.Millisecond
)

func newTestBoard(t *testing.T) *Board {
	t.Helper()

	client := redistest.Connect(t)

	board, err := NewBoard(client, &config.Editor{
		Stream: redistest.Prefix(t, client) + "editor:jobs", JobTimeout: time.Minute, PollInterval: time.Minute,
	}, slog.New(slog.DiscardHandler))
	require.NoError(t, err)

	board.block = testBlock
	board.lockTTL = integrationTTL
	board.claimIdle = 2 * integrationTTL

	require.NoError(t, board.Init(context.Background()))
	require.NoError(t, board.Init(context.Background()), "init is idempotent")

	return board
}

func testJob(taskID int64) *Job {
	return &Job{
		ID: JobID(taskID, 1), TaskID: taskID, Version: 1, SourceKey: testSourceKey,
		CreatedAt: testDeadline.Add(-time.Hour), Deadline: testDeadline,
	}
}

// TestIntegrationBoardJobLifecycle: a job is submitted once, delivered to a worker with its fields, locked by one
// worker at a time, finished once, announced to the listener and forgotten.
func TestIntegrationBoardJobLifecycle(t *testing.T) {
	t.Parallel()

	board := newTestBoard(t)
	ctx := context.Background()
	job := testJob(7)

	woken := listen(t, board)

	submitted, err := board.Submit(ctx, job)
	require.NoError(t, err)
	assert.True(t, submitted)

	again, err := board.Submit(ctx, job)
	require.NoError(t, err)
	assert.False(t, again, "a submitted job is not queued twice")

	stored, found, err := board.Job(ctx, job.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, job.SourceKey, stored.SourceKey)
	assert.True(t, job.Deadline.Equal(stored.Deadline))

	d, err := board.Fetch(ctx, testOwner)
	require.NoError(t, err)
	require.NotNil(t, d)
	require.NotNil(t, d.Job)
	assert.Equal(t, job.ID, d.Job.ID)

	checkLock(t, board, job.ID)

	result := &Result{JobID: job.ID, Status: DoneStatus, SourceDuration: 180, Duration: 141, FinishedAt: testDeadline}
	require.NoError(t, board.Finish(ctx, job, result))
	require.NoError(t, board.Finish(ctx, job, Failed(job.ID, TemporaryCode, testDeadline)), "a duplicate is ignored")
	require.NoError(t, board.Ack(ctx, d.MessageID))

	got, found, err := board.Result(ctx, job.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, DoneStatus, got.Status, "the first result is kept")
	assert.InDelta(t, 141, got.Duration, 1e-9)

	require.Eventually(t, func() bool { return woken.has(job.TaskID) }, waitFor, tick, "the app is told the task")

	next, err := board.Fetch(ctx, testOwner)
	require.NoError(t, err)
	assert.Nil(t, next, "an acked job is not delivered again")

	require.NoError(t, board.Forget(ctx, job.ID))
	_, found, err = board.Result(ctx, job.ID)
	require.NoError(t, err)
	assert.False(t, found)
}

// checkLock: one owner at a time; another may take the job only after the lock expires without a refresh.
func checkLock(t *testing.T, board *Board, jobID string) {
	t.Helper()

	ctx := context.Background()

	locked, err := board.Lock(ctx, jobID, testOwner)
	require.NoError(t, err)
	assert.True(t, locked)

	locked, err = board.Lock(ctx, jobID, "worker-b")
	require.NoError(t, err)
	assert.False(t, locked, "held by another worker")

	held, err := board.Refresh(ctx, jobID, testOwner)
	require.NoError(t, err)
	assert.True(t, held)

	held, err = board.Refresh(ctx, jobID, "worker-b")
	require.NoError(t, err)
	assert.False(t, held, "only the owner refreshes")

	require.NoError(t, board.Unlock(ctx, jobID, "worker-b"))
	locked, err = board.Lock(ctx, jobID, "worker-b")
	require.NoError(t, err)
	assert.False(t, locked, "another worker cannot unlock")

	require.NoError(t, board.Unlock(ctx, jobID, testOwner))
	locked, err = board.Lock(ctx, jobID, "worker-b")
	require.NoError(t, err)
	assert.True(t, locked)

	require.Eventually(t, func() bool {
		locked, err := board.Lock(ctx, jobID, testOwner)

		return err == nil && locked
	}, waitFor, tick, "a lock without refreshes expires")

	require.NoError(t, board.Unlock(ctx, jobID, testOwner))
}

// TestIntegrationBoardClaimsJobOfDeadWorker: an entry not acked by a dead worker is delivered again after the
// claim time, and a malformed entry comes without a job.
func TestIntegrationBoardClaimsJobOfDeadWorker(t *testing.T) {
	t.Parallel()

	board := newTestBoard(t)
	ctx := context.Background()

	_, err := board.Submit(ctx, testJob(8))
	require.NoError(t, err)

	first, err := board.Fetch(ctx, "dead")
	require.NoError(t, err)
	require.NotNil(t, first)

	var claimed *Delivery

	require.Eventually(t, func() bool {
		claimed, err = board.Fetch(ctx, "alive")

		return err == nil && claimed != nil
	}, waitFor, tick)
	assert.Equal(t, first.MessageID, claimed.MessageID)
	require.NoError(t, board.Ack(ctx, claimed.MessageID))

	require.NoError(t, board.rdb.XAdd(ctx, &goredis.XAddArgs{
		Stream: board.stream, Values: map[string]any{jobField: "not a job"},
	}).Err())

	malformed, err := board.Fetch(ctx, "alive")
	require.NoError(t, err)
	require.NotNil(t, malformed)
	assert.Nil(t, malformed.Job)
}

// woken collects the task IDs the board announced.
type woken struct {
	mu  sync.Mutex
	ids []int64
}

func (w *woken) add(_ context.Context, taskID int64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.ids = append(w.ids, taskID)
}

func (w *woken) has(taskID int64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()

	return slices.Contains(w.ids, taskID)
}

// listen subscribes to finished jobs until the test ends; it returns once the subscription is active.
func listen(t *testing.T, board *Board) *woken {
	t.Helper()

	got := &woken{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = board.ListenDone(ctx, got.add)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	// The subscription is made inside ListenDone: wait until Redis counts the subscriber.
	require.Eventually(t, func() bool {
		counts, err := board.rdb.PubSubNumSub(context.Background(), board.doneChannel()).Result()

		return err == nil && counts[board.doneChannel()] > 0
	}, waitFor, tick)

	return got
}
