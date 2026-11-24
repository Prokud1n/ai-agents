package editor

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	testTaskID    = int64(17)
	testJobID     = "task-17-v1"
	testSourceKey = "incoming/-1001234567890/42/uniq.mov"
	testReelKey   = "edits/-1001234567890/42/uniq-v1-reel.mp4"
	testTimeout   = 45 * time.Minute
	testQueue     = 3 * time.Hour
	testWorker    = 3 * time.Minute
	testPoll      = time.Minute
)

var (
	testNow      = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	errRedisDown = errors.New("redis is down")
)

// fakeBoard keeps one job and one result; a worker is alive unless noWorker.
type fakeBoard struct {
	job       *editjob.Job
	result    *editjob.Result
	started   time.Time
	locked    bool
	noWorker  bool
	submitted []editjob.Job
	forgotten []string
	err       error
}

func (b *fakeBoard) Started(context.Context, string) (time.Time, bool, error) {
	return b.started, !b.started.IsZero(), nil
}

func (b *fakeBoard) Locked(context.Context, string) (bool, error) { return b.locked, nil }

func (b *fakeBoard) WorkerAlive(context.Context) (bool, error) { return !b.noWorker, nil }

// fakeTech records the reports to «Техничка».
type fakeTech struct {
	reports []string
}

func (f *fakeTech) Report(_ context.Context, agent, text string) error {
	f.reports = append(f.reports, agent+": "+text)

	return nil
}

func (b *fakeBoard) Submit(_ context.Context, job *editjob.Job) (bool, error) {
	if b.err != nil {
		return false, b.err
	}

	b.submitted = append(b.submitted, *job)
	b.job = job

	return true, nil
}

func (b *fakeBoard) Job(context.Context, string) (editjob.Job, bool, error) {
	if b.job == nil {
		return editjob.Job{}, false, b.err
	}

	return *b.job, true, b.err
}

func (b *fakeBoard) Result(context.Context, string) (editjob.Result, bool, error) {
	if b.result == nil {
		return editjob.Result{}, false, b.err
	}

	return *b.result, true, b.err
}

func (b *fakeBoard) Forget(_ context.Context, id string) error {
	b.forgotten = append(b.forgotten, id)
	b.job, b.result = nil, nil

	return nil
}

// testKeys makes result keys like media.Store with the prefix "edits".
type testKeys struct{}

func (testKeys) ResultKey(sourceKey, name, ext string) (string, error) {
	info, err := media.ParseKey(sourceKey)
	if err != nil {
		return "", err
	}

	return media.ResultKey("edits", info, name, ext)
}

func newTestExecutor(b *fakeBoard) *Executor {
	return newTestExecutorWithTech(b, &fakeTech{})
}

func newTestExecutorWithTech(b *fakeBoard, tech *fakeTech) *Executor {
	e := NewExecutor(b, testKeys{}, tech, &config.Editor{
		JobTimeout: testTimeout, QueueTimeout: testQueue, WorkerWait: testWorker, PollInterval: testPoll,
	}, slog.New(slog.DiscardHandler))
	e.now = func() time.Time { return testNow }

	return e
}

func editData() *tasks.ExecuteData {
	return &tasks.ExecuteData{
		TaskID: testTaskID, Agent: AgentName, Kind: "reels-edit", Input: "про планку", Media: []string{testSourceKey},
		Version: 1,
	}
}

// pendingJob was submitted the given time ago.
func pendingJob(ago time.Duration) *editjob.Job {
	created := testNow.Add(-ago)

	return &editjob.Job{
		ID: editjob.JobID(testTaskID, 1), TaskID: testTaskID, Version: 1, SourceKey: testSourceKey,
		CreatedAt: created, QueueDeadline: created.Add(testQueue), Timeout: testTimeout,
	}
}

func TestExecutorSubmitsAndDefers(t *testing.T) {
	t.Parallel()

	b := &fakeBoard{}

	_, err := newTestExecutor(b).Execute(t.Context(), editData())

	var deferred *tasks.DeferredError
	require.ErrorAs(t, err, &deferred)
	assert.Equal(t, testPoll, deferred.After)

	require.Len(t, b.submitted, 1)
	assert.Equal(t, editjob.Job{
		ID: testJobID, TaskID: testTaskID, Version: 1, SourceKey: testSourceKey,
		CreatedAt: testNow, QueueDeadline: testNow.Add(testQueue), Timeout: testTimeout,
	}, b.submitted[0])
}

// TestExecutorWaitsForTheWorker: a job in the queue and a running job wait; the timeout counts from the start, so
// a job that waited for hours behind others still gets its whole time.
func TestExecutorWaitsForTheWorker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		board    *fakeBoard
		expected time.Duration
	}{
		{name: "just submitted", board: &fakeBoard{job: pendingJob(time.Minute)}, expected: testPoll},
		{
			name:     "waits behind other sources for two hours, a worker is alive",
			board:    &fakeBoard{job: pendingJob(2 * time.Hour)},
			expected: testPoll,
		},
		{
			name:     "taken 2 h after the submit, running for 10 min",
			board:    &fakeBoard{job: pendingJob(2*time.Hour + 10*time.Minute), started: testNow.Add(-10 * time.Minute)},
			expected: testPoll,
		},
		{
			name:     "near the end of its time: until the end",
			board:    &fakeBoard{job: pendingJob(time.Hour), started: testNow.Add(-testTimeout - startGrace + 20*time.Second)},
			expected: 20 * time.Second,
		},
		{
			name:     "no heartbeat, but the job is locked (a worker has it)",
			board:    &fakeBoard{job: pendingJob(10 * time.Minute), noWorker: true, locked: true},
			expected: testPoll,
		},
		{
			name:     "no heartbeat yet, shortly after the submit",
			board:    &fakeBoard{job: pendingJob(time.Minute), noWorker: true},
			expected: testPoll,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := newTestExecutor(tt.board).Execute(t.Context(), editData())

			var deferred *tasks.DeferredError
			require.ErrorAs(t, err, &deferred)
			assert.Equal(t, tt.expected, deferred.After)
			assert.Empty(t, tt.board.submitted, "the job is not submitted again")
			assert.Empty(t, tt.board.forgotten)
		})
	}
}

func TestExecutorResult(t *testing.T) {
	t.Parallel()

	done := &editjob.Result{
		JobID: testJobID, Status: editjob.DoneStatus, SourceDuration: 182, Duration: 141, Candidates: 2,
	}

	out, err := newTestExecutor(&fakeBoard{result: done}).Execute(t.Context(), editData())
	require.NoError(t, err)
	assert.Equal(t, testReelKey, out.MediaKey, "the key is computed by the app, not taken from the worker")
	assert.Equal(t, Describe(done, "про планку"), out.Text)
}

func TestExecutorFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		board     *fakeBoard
		data      func(d *tasks.ExecuteData)
		permanent string
		temporary bool
		forgotten bool
		reported  string
	}{
		{
			name:      "no source video",
			board:     &fakeBoard{},
			data:      func(d *tasks.ExecuteData) { d.Media = nil },
			permanent: noSourceText,
		},
		{
			name:      "revision of a video",
			board:     &fakeBoard{},
			data:      func(d *tasks.ExecuteData) { d.Version = 2 },
			permanent: tasks.VideoRevisionText,
		},
		{
			name:      "ran out of time since a worker took it",
			board:     &fakeBoard{job: pendingJob(time.Hour), started: testNow.Add(-testTimeout - startGrace)},
			permanent: runningTimeoutText(testTimeout),
			forgotten: true,
		},
		{
			name:      "no worker took it within the queue timeout",
			board:     &fakeBoard{job: pendingJob(testQueue)},
			permanent: queueTimeoutText(testQueue),
			forgotten: true,
		},
		{
			name:      "no worker is running: the technical text, the team is told",
			board:     &fakeBoard{job: pendingJob(testWorker), noWorker: true},
			permanent: technicalText,
			forgotten: true,
			reported:  "editor: Монтажёр: задача 17 не выполнена, код worker_unavailable. Подробности — в логах app и media-worker.",
		},
		{
			name:      "the worker reports a timeout",
			board:     &fakeBoard{result: &editjob.Result{JobID: testJobID, Status: editjob.FailedStatus, Code: editjob.TimeoutCode}},
			permanent: runningTimeoutText(testTimeout),
			forgotten: true,
		},
		{
			name:      "the worker reports a queue timeout",
			board:     &fakeBoard{result: &editjob.Result{JobID: testJobID, Status: editjob.FailedStatus, Code: editjob.QueueTimeoutCode}},
			permanent: queueTimeoutText(testQueue),
			forgotten: true,
		},
		{
			name:      "a source above the limit",
			board:     &fakeBoard{result: &editjob.Result{JobID: testJobID, Status: editjob.FailedStatus, Code: editjob.SourceTooLongCode, SourceDuration: 750, MaxDuration: 600}},
			permanent: "Исходник длится 12:30, а монтирую я видео до 10 мин. Разрежьте его на части и пришлите по одной.",
			forgotten: true,
		},
		{
			name:      "no OPENAI_API_KEY in the worker: no details for the trainer",
			board:     &fakeBoard{result: &editjob.Result{JobID: testJobID, Status: editjob.FailedStatus, Code: editjob.NotConfiguredCode}},
			permanent: technicalText,
			forgotten: true,
			reported:  "editor: Монтажёр: задача 17 не выполнена, код not_configured. Подробности — в логах app и media-worker.",
		},
		{
			name:      "the brand fonts are missing",
			board:     &fakeBoard{result: &editjob.Result{JobID: testJobID, Status: editjob.FailedStatus, Code: editjob.FontsCode}},
			permanent: technicalText,
			forgotten: true,
			reported:  "editor: Монтажёр: задача 17 не выполнена, код fonts. Подробности — в логах app и media-worker.",
		},
		{
			name:      "an unknown code: a neutral text, not «tried several times»",
			board:     &fakeBoard{result: &editjob.Result{JobID: testJobID, Status: editjob.FailedStatus, Code: "new_code"}},
			permanent: otherFailureText,
			forgotten: true,
		},
		{
			name:      "a temporary failure is retried by the task",
			board:     &fakeBoard{result: &editjob.Result{JobID: testJobID, Status: editjob.FailedStatus, Code: editjob.TemporaryCode}},
			temporary: true,
			forgotten: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			data := editData()
			if tt.data != nil {
				tt.data(data)
			}

			tech := &fakeTech{}

			_, err := newTestExecutorWithTech(tt.board, tech).Execute(t.Context(), data)
			require.Error(t, err)

			var (
				permanent *tasks.PermanentError
				temporary *tasks.TemporaryError
			)

			if tt.temporary {
				require.ErrorAs(t, err, &temporary)
				assert.Equal(t, editFailedText, temporary.Message)
			} else {
				require.ErrorAs(t, err, &permanent)
				assert.Equal(t, tt.permanent, permanent.Message)
			}

			assert.Equal(t, tt.forgotten, len(tt.board.forgotten) == 1, "a failed job is forgotten for a retry")

			if tt.reported == "" {
				assert.Empty(t, tech.reports)
			} else {
				assert.Equal(t, []string{tt.reported}, tech.reports)
			}
		})
	}
}

func TestExecutorBoardError(t *testing.T) {
	t.Parallel()

	_, err := newTestExecutor(&fakeBoard{err: errRedisDown}).Execute(t.Context(), editData())
	require.ErrorIs(t, err, errRedisDown)

	var permanent *tasks.PermanentError
	assert.NotErrorAs(t, err, &permanent, "Redis down is retried")
}
