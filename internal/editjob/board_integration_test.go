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
	"github.com/Prokud1n/ai-agents/internal/roughcut"
)

const (
	testOwner      = "worker-a"
	testBlock      = 100 * time.Millisecond
	integrationTTL = time.Second
	waitFor        = 5 * time.Second
	tick           = 20 * time.Millisecond
	presenceJob    = "task-1-v1"
)

func newTestBoard(t *testing.T) *Board {
	t.Helper()

	client := redistest.Connect(t)

	board, err := NewBoard(client, &config.Editor{
		Stream: redistest.Prefix(t, client) + "editor:jobs", JobTimeout: time.Minute, PollInterval: time.Minute,
		QueueTimeout: time.Hour, WorkerWait: time.Minute,
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
		CreatedAt: testDeadline.Add(-time.Hour), QueueDeadline: testDeadline, Timeout: time.Hour,
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
	assert.True(t, job.QueueDeadline.Equal(stored.QueueDeadline))
	assert.Equal(t, job.Timeout, stored.Timeout)

	d, err := board.Fetch(ctx, testOwner)
	require.NoError(t, err)
	require.NotNil(t, d)
	require.NotNil(t, d.Job)
	assert.Equal(t, job.ID, d.Job.ID)

	checkLock(t, board, job.ID)
	checkStarted(t, board, job.ID)

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

// checkStarted: the first take of the job is its start; a second one (after a crash) keeps it.
func checkStarted(t *testing.T, board *Board, jobID string) {
	t.Helper()

	ctx := context.Background()

	_, found, err := board.Started(ctx, jobID)
	require.NoError(t, err)
	assert.False(t, found)

	first, err := board.MarkStarted(ctx, jobID, testDeadline.Add(-30*time.Minute))
	require.NoError(t, err)

	again, err := board.MarkStarted(ctx, jobID, testDeadline)
	require.NoError(t, err)
	assert.True(t, first.Equal(again), "the first start counts")

	started, found, err := board.Started(ctx, jobID)
	require.NoError(t, err)
	assert.True(t, found)
	assert.True(t, started.Equal(first))
}

// TestIntegrationBoardPresence: the app sees a worker by its heartbeat and a running job by its lock.
func TestIntegrationBoardPresence(t *testing.T) {
	t.Parallel()

	board := newTestBoard(t)
	ctx := context.Background()

	alive, err := board.WorkerAlive(ctx)
	require.NoError(t, err)
	assert.False(t, alive, "no worker yet")

	require.NoError(t, board.Heartbeat(ctx, testOwner))
	alive, err = board.WorkerAlive(ctx)
	require.NoError(t, err)
	assert.True(t, alive)

	locked, err := board.Locked(ctx, presenceJob)
	require.NoError(t, err)
	assert.False(t, locked)

	_, err = board.Lock(ctx, presenceJob, testOwner)
	require.NoError(t, err)
	locked, err = board.Locked(ctx, presenceJob)
	require.NoError(t, err)
	assert.True(t, locked)
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

// TestIntegrationBoardReplace: a planned job is replaced by its render stage once: the new job is stored and queued,
// the planned result and the start are gone; a job without a planned result is not replaced.
func TestIntegrationBoardReplace(t *testing.T) {
	t.Parallel()

	board := newTestBoard(t)
	ctx := context.Background()
	job := testJob(9)
	job.Decide = true

	_, err := board.Submit(ctx, job)
	require.NoError(t, err)

	d, err := board.Fetch(ctx, testOwner)
	require.NoError(t, err)
	require.NotNil(t, d)

	render := *job
	render.Stage = RenderStage
	render.Settings = &roughcut.Settings{MaxPause: 0.5, RetakeSimilarity: 0.7}
	render.Edit = &roughcut.Edit{Accept: []int{0}, Notes: []string{"повтор 0 вырезан"}}

	replaced, err := board.Replace(ctx, &render)
	require.NoError(t, err)
	assert.False(t, replaced, "a job without a result is not replaced")

	_, err = board.MarkStarted(ctx, job.ID, testDeadline)
	require.NoError(t, err)

	locked, err := board.Lock(ctx, job.ID, testOwner)
	require.NoError(t, err)
	require.True(t, locked)

	require.NoError(t, board.Finish(ctx, job, &Result{JobID: job.ID, Status: PlannedStatus, Candidates: 1}))
	require.NoError(t, board.Ack(ctx, d.MessageID))

	replaced, err = board.Replace(ctx, &render)
	require.NoError(t, err)
	assert.True(t, replaced)

	again, err := board.Replace(ctx, &render)
	require.NoError(t, err)
	assert.False(t, again, "replaced once")

	_, found, err := board.Result(ctx, job.ID)
	require.NoError(t, err)
	assert.False(t, found, "the planned result is gone")

	_, started, err := board.Started(ctx, job.ID)
	require.NoError(t, err)
	assert.False(t, started, "the render's timeout counts from its own start")

	assertUnlocked(t, board, job.ID)

	stored, found, err := board.Job(ctx, job.ID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, RenderStage, stored.Stage)
	assert.Equal(t, render.Edit, stored.Edit)

	next, err := board.Fetch(ctx, testOwner)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.NotNil(t, next.Job)
	assert.Equal(t, RenderStage, next.Job.Stage)
	assert.Equal(t, render.Settings, next.Job.Settings)

	checkRetry(t, board, next, &render)
}

// assertUnlocked: a replaced result means the worker is done, so its lock goes too.
func assertUnlocked(t *testing.T, board *Board, id string) {
	t.Helper()

	locked, err := board.Locked(context.Background(), id)
	require.NoError(t, err)
	assert.False(t, locked)
}

// checkRetry: a render that failed is queued again once, with the same edit and new deadlines.
func checkRetry(t *testing.T, board *Board, delivery *Delivery, render *Job) {
	t.Helper()

	ctx := context.Background()

	require.NoError(t, board.Finish(ctx, render, Failed(render.ID, TemporaryCode, testDeadline)))
	require.NoError(t, board.Ack(ctx, delivery.MessageID))

	again := *render
	again.QueueDeadline = testDeadline.Add(time.Hour)

	retried, err := board.Retry(ctx, &again)
	require.NoError(t, err)
	assert.True(t, retried)

	twice, err := board.Retry(ctx, &again)
	require.NoError(t, err)
	assert.False(t, twice, "queued once")

	_, found, err := board.Result(ctx, render.ID)
	require.NoError(t, err)
	assert.False(t, found, "the failed result is gone")

	next, err := board.Fetch(ctx, testOwner)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.NotNil(t, next.Job)
	assert.Equal(t, render.Edit, next.Job.Edit, "the same decision")
	assert.True(t, again.QueueDeadline.Equal(next.Job.QueueDeadline))
}
