package mediaworker

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
)

var testNow = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

const testJobID = "task-17-v1"

// fakeBoard records what the worker did with one delivery.
type fakeBoard struct {
	mu sync.Mutex

	hasResult bool
	lockHeld  bool // another worker holds the lock
	lost      bool // Refresh reports the lock lost
	finishErr error
	// stored is the job on the board (nil: the delivered one, a job with only the ID and zero time).
	stored *editjob.Job
	// forgotten: no job on the board, as after Forget; forgetOnLock forgets it when the worker locks it.
	forgotten    bool
	forgetOnLock bool

	acked    []string
	finished []editjob.Result
	notified int
	unlocked int
	// started is the recorded start of the job (zero: not taken yet).
	started time.Time
}

func (b *fakeBoard) Started(context.Context, string) (time.Time, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.started, !b.started.IsZero(), nil
}

func (b *fakeBoard) MarkStarted(_ context.Context, job *editjob.Job, now time.Time) (time.Time, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.current(job) {
		return time.Time{}, editjob.ErrStale
	}

	if b.started.IsZero() {
		b.started = now
	}

	return b.started, nil
}

func (*fakeBoard) Init(context.Context) error { return nil }

// Fetch has nothing to give: it waits a little like the real block, so a started worker does not spin.
func (*fakeBoard) Fetch(ctx context.Context, _ string) (*editjob.Delivery, error) {
	sleep(ctx, 10*time.Millisecond)

	return nil, nil
}

func (b *fakeBoard) Ack(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.acked = append(b.acked, id)

	return nil
}

func (b *fakeBoard) HasResult(context.Context, string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.hasResult, nil
}

func (b *fakeBoard) Job(_ context.Context, id string) (editjob.Job, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	job, found := b.job(id)

	return job, found, nil
}

// job is the job on the board; the caller holds the mutex.
func (b *fakeBoard) job(id string) (editjob.Job, bool) {
	switch {
	case b.forgotten:
		return editjob.Job{}, false
	case b.stored == nil:
		return editjob.Job{ID: id}, true
	default:
		return *b.stored, true
	}
}

// current: the job on the board is the given one, as Board.Finish and MarkStarted check; the caller holds the mutex.
func (b *fakeBoard) current(job *editjob.Job) bool {
	stored, found := b.job(job.ID)

	return found && stored.Same(job)
}

// forget deletes the job, as the app's Board.Forget.
func (b *fakeBoard) forget() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.forgotten, b.hasResult = true, false
}

func (b *fakeBoard) Lock(context.Context, string, string) (bool, error) {
	if b.forgetOnLock {
		b.forget()
	}

	return !b.lockHeld, nil
}

func (b *fakeBoard) Refresh(context.Context, string, string) (bool, error) { return !b.lost, nil }

func (b *fakeBoard) Unlock(context.Context, string, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.unlocked++

	return nil
}

func (b *fakeBoard) Finish(_ context.Context, job *editjob.Job, r *editjob.Result) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.finishErr != nil {
		return b.finishErr
	}

	if !b.current(job) {
		return editjob.ErrStale
	}

	b.finished = append(b.finished, *r)
	b.hasResult = true

	return nil
}

func (b *fakeBoard) NotifyDone(context.Context, *editjob.Job) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.notified++
}

func (*fakeBoard) Heartbeat(context.Context, string) error { return nil }

func (*fakeBoard) LockTTL() time.Duration { return 30 * time.Millisecond }

// fakeRunner returns the result and error; block waits for the context instead (a long job).
type fakeRunner struct {
	result *editjob.Result
	err    error
	block  bool
	panics bool
	calls  int
	// onRun is called when the job runs (the app forgets it meanwhile).
	onRun func()
}

func (r *fakeRunner) Run(ctx context.Context, job *editjob.Job) (*editjob.Result, error) {
	r.calls++

	if r.onRun != nil {
		r.onRun()
	}

	if r.panics {
		panic("nil map")
	}

	if r.block {
		<-ctx.Done()

		return &editjob.Result{JobID: job.ID}, ctx.Err()
	}

	return r.result, r.err
}

func newTestWorker(board *fakeBoard, runner *fakeRunner) *Worker {
	w := NewWorker(board, runner, &config.MediaWorker{}, slog.New(slog.DiscardHandler))
	w.now = func() time.Time { return testNow }

	return w
}

// delivery is a job that may wait in the queue until queueDeadline and runs for an hour.
func delivery(queueDeadline time.Time) *editjob.Delivery {
	return deliveryWithTimeout(queueDeadline, time.Hour)
}

func deliveryWithTimeout(queueDeadline time.Time, timeout time.Duration) *editjob.Delivery {
	return &editjob.Delivery{MessageID: "1-0", Job: &editjob.Job{
		ID: editjob.JobID(17, 1), TaskID: 17, Version: 1, SourceKey: testSourceKey, QueueDeadline: queueDeadline,
		Timeout: timeout,
	}}
}

func TestWorkerHandle(t *testing.T) {
	t.Parallel()

	done := &editjob.Result{JobID: testJobID, Status: editjob.DoneStatus}
	later := testNow.Add(time.Hour)

	tests := []struct {
		name           string
		board          *fakeBoard
		runner         *fakeRunner
		delivery       *editjob.Delivery
		expectedRuns   int
		expectedAcks   int
		expectedStatus editjob.Status
		expectedCode   editjob.FailureCode
		expectedNotify int
	}{
		{
			name: "a job is run, its result stored, the entry acked", board: &fakeBoard{},
			runner: &fakeRunner{result: done}, delivery: delivery(later),
			expectedRuns: 1, expectedAcks: 1, expectedStatus: editjob.DoneStatus,
		},
		{
			name: "a failure is a result with its code", board: &fakeBoard{},
			runner:       &fakeRunner{result: &editjob.Result{JobID: testJobID}, err: fail(editjob.NoAudioCode, errNoAudio)},
			delivery:     delivery(later),
			expectedRuns: 1, expectedAcks: 1, expectedStatus: editjob.FailedStatus, expectedCode: editjob.NoAudioCode,
		},
		{
			name: "a panic is a temporary failure", board: &fakeBoard{}, runner: &fakeRunner{panics: true},
			delivery: delivery(later), expectedRuns: 1, expectedAcks: 1,
			expectedStatus: editjob.FailedStatus, expectedCode: editjob.TemporaryCode,
		},
		{
			name: "a finished job is announced again and skipped", board: &fakeBoard{hasResult: true},
			runner: &fakeRunner{}, delivery: delivery(later), expectedAcks: 1, expectedNotify: 1,
		},
		{
			name: "a job that waited past its queue deadline is not run", board: &fakeBoard{}, runner: &fakeRunner{},
			delivery: delivery(testNow), expectedAcks: 1, expectedStatus: editjob.FailedStatus,
			expectedCode: editjob.QueueTimeoutCode,
		},
		{
			name:  "a job taken before (the worker died) runs again past the queue deadline",
			board: &fakeBoard{started: testNow.Add(-time.Minute)}, runner: &fakeRunner{result: done},
			delivery: delivery(testNow), expectedRuns: 1, expectedAcks: 1, expectedStatus: editjob.DoneStatus,
		},
		{
			name:  "a job taken before whose time is over is a timeout without a run",
			board: &fakeBoard{started: testNow.Add(-2 * time.Hour)}, runner: &fakeRunner{},
			delivery: delivery(later), expectedAcks: 1, expectedStatus: editjob.FailedStatus,
			expectedCode: editjob.TimeoutCode,
		},
		{
			name: "a locked job is left pending for a later claim", board: &fakeBoard{lockHeld: true},
			runner: &fakeRunner{}, delivery: delivery(later),
		},
		{
			name:  "a planned job replaced by its render stage is skipped",
			board: &fakeBoard{stored: &editjob.Job{ID: testJobID, Stage: editjob.RenderStage}}, runner: &fakeRunner{},
			delivery: delivery(later), expectedAcks: 1,
		},
		{
			name: "a render queued again later is skipped in its old delivery",
			board: &fakeBoard{stored: &editjob.Job{
				ID: testJobID, Stage: editjob.AutoStage, CreatedAt: testNow.Add(time.Minute),
			}},
			runner: &fakeRunner{}, delivery: delivery(later), expectedAcks: 1,
		},
		{
			name:   "the current stage is run",
			board:  &fakeBoard{stored: &editjob.Job{ID: testJobID, Stage: editjob.AutoStage}},
			runner: &fakeRunner{result: done}, delivery: delivery(later),
			expectedRuns: 1, expectedAcks: 1, expectedStatus: editjob.DoneStatus,
		},
		{
			name:  "a forgotten job is skipped without a run, a result or an announcement",
			board: &fakeBoard{forgotten: true}, runner: &fakeRunner{result: done}, delivery: delivery(later),
			expectedAcks: 1,
		},
		{
			name:  "a forgotten job past its queue deadline gets no queue timeout result",
			board: &fakeBoard{forgotten: true}, runner: &fakeRunner{}, delivery: delivery(testNow), expectedAcks: 1,
		},
		{
			name:  "a job forgotten after the check is not started",
			board: &fakeBoard{forgetOnLock: true}, runner: &fakeRunner{result: done}, delivery: delivery(later),
			expectedAcks: 1,
		},
		{
			name: "a malformed entry is acked", board: &fakeBoard{}, runner: &fakeRunner{},
			delivery: &editjob.Delivery{MessageID: "1-0"}, expectedAcks: 1,
		},
		{
			name:  "the result could not be stored: not acked, the job is run again",
			board: &fakeBoard{finishErr: errS3Down}, runner: &fakeRunner{result: done}, delivery: delivery(later),
			expectedRuns: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			newTestWorker(tt.board, tt.runner).handle(t.Context(), tt.delivery)

			assert.Equal(t, tt.expectedRuns, tt.runner.calls)
			assert.Len(t, tt.board.acked, tt.expectedAcks)
			assert.Equal(t, tt.expectedNotify, tt.board.notified)

			if tt.expectedStatus == "" {
				assert.Empty(t, tt.board.finished)

				return
			}

			require.Len(t, tt.board.finished, 1)
			assert.Equal(t, tt.expectedStatus, tt.board.finished[0].Status)
			assert.Equal(t, tt.expectedCode, tt.board.finished[0].Code)
		})
	}
}

// TestWorkerDropsResultOfForgottenJob: the app forgot the job while it ran (a timeout): the result is not stored, so
// a retry submitted under the same ID does not find it, and nothing is announced; the entry is acked.
func TestWorkerDropsResultOfForgottenJob(t *testing.T) {
	t.Parallel()

	board := &fakeBoard{}
	runner := &fakeRunner{result: &editjob.Result{JobID: testJobID, Status: editjob.DoneStatus}, onRun: board.forget}

	newTestWorker(board, runner).handle(t.Context(), delivery(testNow.Add(time.Hour)))

	assert.Equal(t, 1, runner.calls)
	assert.Empty(t, board.finished)
	assert.Zero(t, board.notified)
	assert.Len(t, board.acked, 1)
	assert.Equal(t, 1, board.unlocked)
}

// TestWorkerRunsResubmittedJobOnce: after Forget the app submits the job again under the same ID with a new creation
// time; the old entry left in the stream is skipped, the new one runs once, and its redelivery is not run again.
func TestWorkerRunsResubmittedJobOnce(t *testing.T) {
	t.Parallel()

	old := delivery(testNow.Add(time.Hour))
	old.Job.CreatedAt = testNow.Add(-time.Hour)

	resubmitted := delivery(testNow.Add(time.Hour))
	resubmitted.MessageID = "2-0"
	resubmitted.Job.CreatedAt = testNow

	board := &fakeBoard{stored: new(*resubmitted.Job)}
	runner := &fakeRunner{result: &editjob.Result{JobID: testJobID, Status: editjob.DoneStatus}}
	w := newTestWorker(board, runner)

	tests := []struct {
		name          string
		delivery      *editjob.Delivery
		expectedRuns  int
		expectedAcks  []string
		expectedStore int
	}{
		{name: "the entry of the forgotten job", delivery: old, expectedAcks: []string{"1-0"}},
		{
			name: "the entry of the new submit", delivery: resubmitted, expectedRuns: 1,
			expectedAcks: []string{"1-0", "2-0"}, expectedStore: 1,
		},
		{
			name: "a redelivery of the new submit", delivery: resubmitted, expectedRuns: 1,
			expectedAcks: []string{"1-0", "2-0", "2-0"}, expectedStore: 1,
		},
		{
			name: "the old entry claimed again", delivery: old, expectedRuns: 1,
			expectedAcks: []string{"1-0", "2-0", "2-0", "1-0"}, expectedStore: 1,
		},
	}

	for _, tt := range tests { // in order, not in parallel: each step continues the previous one
		t.Run(tt.name, func(t *testing.T) {
			w.handle(t.Context(), tt.delivery)

			assert.Equal(t, tt.expectedRuns, runner.calls)
			assert.Equal(t, tt.expectedAcks, board.acked)
			assert.Len(t, board.finished, tt.expectedStore)
		})
	}
}

// TestWorkerStopInterruptsJob: stop cancels the running job; it is neither finished nor acked, so it is taken again
// after the restart; the lock is released.
func TestWorkerStopInterruptsJob(t *testing.T) {
	t.Parallel()

	board, runner := &fakeBoard{}, &fakeRunner{block: true}
	ctx, cancel := context.WithCancel(t.Context())

	time.AfterFunc(50*time.Millisecond, cancel)
	newTestWorker(board, runner).handle(ctx, delivery(testNow.Add(time.Hour)))

	assert.Equal(t, 1, runner.calls)
	assert.Empty(t, board.finished)
	assert.Empty(t, board.acked)
	assert.Equal(t, 1, board.unlocked)
}

// TestWorkerStopsJobWithLostLock: a lost lock (another worker took the job) stops the run without a result.
func TestWorkerStopsJobWithLostLock(t *testing.T) {
	t.Parallel()

	board, runner := &fakeBoard{lost: true}, &fakeRunner{block: true}

	newTestWorker(board, runner).handle(t.Context(), delivery(testNow.Add(time.Hour)))

	assert.Empty(t, board.finished, "the other worker's result counts")
	assert.Empty(t, board.acked)
}

// TestWorkerDeadline: the timeout counts from the start, not from the submit: a job that waited in the queue runs
// its full time, and one that runs longer fails with a timeout.
func TestWorkerDeadline(t *testing.T) {
	t.Parallel()

	board, runner := &fakeBoard{}, &fakeRunner{block: true}
	w := newTestWorker(board, runner)
	w.now = time.Now

	started := time.Now()
	w.handle(t.Context(), deliveryWithTimeout(time.Now().Add(time.Hour), 100*time.Millisecond))

	require.Len(t, board.finished, 1)
	assert.Equal(t, editjob.TimeoutCode, board.finished[0].Code)
	assert.GreaterOrEqual(t, time.Since(started), 100*time.Millisecond, "the job had its whole timeout")
	assert.Len(t, board.acked, 1)
	assert.False(t, board.started.IsZero(), "the start is recorded for the app")
}

func TestCheckHeartbeat(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), heartbeatName)

	require.Error(t, CheckHeartbeat(path, HeartbeatMaxAge, testNow), "no heartbeat yet")

	require.NoError(t, touch(path, testNow))
	require.NoError(t, CheckHeartbeat(path, HeartbeatMaxAge, testNow.Add(HeartbeatMaxAge/2)))

	err := CheckHeartbeat(path, HeartbeatMaxAge, testNow.Add(2*HeartbeatMaxAge))
	require.ErrorIs(t, err, ErrUnhealthy)

	require.NoError(t, touch(path, testNow.Add(2*HeartbeatMaxAge)), "touch updates an existing file")
	require.NoError(t, CheckHeartbeat(path, HeartbeatMaxAge, testNow.Add(2*HeartbeatMaxAge)))
}

func TestHeartbeatFile(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "/work/.heartbeat", HeartbeatFile(&config.MediaWorker{WorkDir: "/work"}))
	assert.Equal(t, "/run/hb", HeartbeatFile(&config.MediaWorker{WorkDir: "/work", HeartbeatFile: "/run/hb"}))
}

// TestWorkerRemovesLeftovers: on start the directories a killed job left are removed, anything else stays.
func TestWorkerRemovesLeftovers(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	for _, name := range []string{"job-123", "subtitles-9", "ffmpeg-filter-1", "keep-me"} {
		require.NoError(t, os.Mkdir(filepath.Join(dir, name), 0o700))
	}

	require.NoError(t, os.WriteFile(filepath.Join(dir, heartbeatName), nil, 0o600))

	w := NewWorker(&fakeBoard{}, &fakeRunner{}, &config.MediaWorker{WorkDir: dir}, slog.New(slog.DiscardHandler))
	require.NoError(t, w.Start(t.Context()))
	require.NoError(t, w.Stop(t.Context()))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}

	assert.ElementsMatch(t, []string{"keep-me", heartbeatName}, names)
}

func TestCodeOf(t *testing.T) {
	t.Parallel()

	assert.Equal(t, editjob.NoSpeechCode, CodeOf(errors.Join(errors.New("x"), fail(editjob.NoSpeechCode, errNoVideo))))
	assert.Equal(t, editjob.TemporaryCode, CodeOf(errS3Down))
}
