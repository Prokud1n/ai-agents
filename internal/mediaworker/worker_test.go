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

// fakeBoard records what the worker did with one delivery.
type fakeBoard struct {
	mu sync.Mutex

	hasResult bool
	lockHeld  bool // another worker holds the lock
	lost      bool // Refresh reports the lock lost
	finishErr error
	// stored is the job on the board (nil: none, as after Forget).
	stored *editjob.Job

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

func (b *fakeBoard) MarkStarted(_ context.Context, _ string, now time.Time) (time.Time, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

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

func (b *fakeBoard) HasResult(context.Context, string) (bool, error) { return b.hasResult, nil }

func (b *fakeBoard) Job(context.Context, string) (editjob.Job, bool, error) {
	if b.stored == nil {
		return editjob.Job{}, false, nil
	}

	return *b.stored, true, nil
}

func (b *fakeBoard) Lock(context.Context, string, string) (bool, error) { return !b.lockHeld, nil }

func (b *fakeBoard) Refresh(context.Context, string, string) (bool, error) { return !b.lost, nil }

func (b *fakeBoard) Unlock(context.Context, string, string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.unlocked++

	return nil
}

func (b *fakeBoard) Finish(_ context.Context, _ *editjob.Job, r *editjob.Result) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.finishErr != nil {
		return b.finishErr
	}

	b.finished = append(b.finished, *r)

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
}

func (r *fakeRunner) Run(ctx context.Context, job *editjob.Job) (*editjob.Result, error) {
	r.calls++

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

	done := &editjob.Result{JobID: "task-17-v1", Status: editjob.DoneStatus}
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
			runner:       &fakeRunner{result: &editjob.Result{JobID: "task-17-v1"}, err: fail(editjob.NoAudioCode, errNoAudio)},
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
			board: &fakeBoard{stored: &editjob.Job{ID: "task-17-v1", Stage: editjob.RenderStage}}, runner: &fakeRunner{},
			delivery: delivery(later), expectedAcks: 1,
		},
		{
			name:   "the current stage is run",
			board:  &fakeBoard{stored: &editjob.Job{ID: "task-17-v1", Stage: editjob.AutoStage}},
			runner: &fakeRunner{result: done}, delivery: delivery(later),
			expectedRuns: 1, expectedAcks: 1, expectedStatus: editjob.DoneStatus,
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

	assert.Equal(t, editjob.NoSpeechCode, CodeOf(errors.Join(errors.New("x"), fail(editjob.NoSpeechCode, errNoWords))))
	assert.Equal(t, editjob.TemporaryCode, CodeOf(errS3Down))
}
