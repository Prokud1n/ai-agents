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
	testPoll      = time.Minute
)

var (
	testNow      = time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	errRedisDown = errors.New("redis is down")
)

// fakeBoard keeps one job and one result.
type fakeBoard struct {
	job       *editjob.Job
	result    *editjob.Result
	submitted []editjob.Job
	forgotten []string
	err       error
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
	e := NewExecutor(b, testKeys{}, &config.Editor{JobTimeout: testTimeout, PollInterval: testPoll},
		slog.New(slog.DiscardHandler))
	e.now = func() time.Time { return testNow }

	return e
}

func editData() *tasks.ExecuteData {
	return &tasks.ExecuteData{
		TaskID: testTaskID, Agent: AgentName, Kind: "reels-edit", Input: "про планку", Media: []string{testSourceKey},
		Version: 1,
	}
}

func pendingJob(deadline time.Time) *editjob.Job {
	return &editjob.Job{
		ID: editjob.JobID(testTaskID, 1), TaskID: testTaskID, Version: 1, SourceKey: testSourceKey,
		CreatedAt: testNow.Add(-time.Minute), Deadline: deadline,
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
		CreatedAt: testNow, Deadline: testNow.Add(testTimeout),
	}, b.submitted[0])
}

func TestExecutorWaitsForTheWorker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		deadline time.Time
		expected time.Duration
	}{
		{name: "far deadline: the poll interval", deadline: testNow.Add(10 * time.Minute), expected: testPoll},
		{name: "near deadline: until it", deadline: testNow.Add(20 * time.Second), expected: 20 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := &fakeBoard{job: pendingJob(tt.deadline)}

			_, err := newTestExecutor(b).Execute(t.Context(), editData())

			var deferred *tasks.DeferredError
			require.ErrorAs(t, err, &deferred)
			assert.Equal(t, tt.expected, deferred.After)
			assert.Empty(t, b.submitted, "the job is not submitted again")
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
			name:      "no result by the deadline",
			board:     &fakeBoard{job: pendingJob(testNow)},
			permanent: timeoutText(testTimeout),
			forgotten: true,
		},
		{
			name:      "a source above the limit",
			board:     &fakeBoard{result: &editjob.Result{JobID: testJobID, Status: editjob.FailedStatus, Code: editjob.SourceTooLongCode, SourceDuration: 750, MaxDuration: 600}},
			permanent: "Исходник длится 12:30, а монтирую я видео до 10 мин. Разрежьте его на части и пришлите по одной.",
			forgotten: true,
		},
		{
			name:      "no OPENAI_API_KEY in the worker",
			board:     &fakeBoard{result: &editjob.Result{JobID: testJobID, Status: editjob.FailedStatus, Code: editjob.NotConfiguredCode}},
			permanent: notConfiguredText,
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

			_, err := newTestExecutor(tt.board).Execute(t.Context(), data)
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
