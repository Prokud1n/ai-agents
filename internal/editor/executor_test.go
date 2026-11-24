package editor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/providers/anthropic"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/tasks"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	testTaskID    = int64(17)
	testJobID     = "task-17-v1"
	testJob2ID    = "task-17-v2"
	testSourceKey = "incoming/-1001234567890/42/uniq.mov"
	testReelKey   = "edits/-1001234567890/42/uniq-v1-reel.mp4"
	testReel2Key  = "edits/-1001234567890/42/uniq-v2-reel.mp4"
	testCaption   = "про планку"
	testRevision  = "убери первые 3 секунды"
	testChange    = "Обрезал начало: убрал первые 3 с."
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
	replaced  []editjob.Job
	retried   []editjob.Job
	forgotten []string
	err       error
}

// Retry queues the job again in place of a failed result, like the Redis board.
func (b *fakeBoard) Retry(_ context.Context, job *editjob.Job) (bool, error) {
	if b.result == nil || b.result.Status != editjob.FailedStatus {
		return false, nil
	}

	b.retried = append(b.retried, *job)
	b.job, b.result = job, nil

	return true, nil
}

// Replace replaces the job of a planned result, like the Redis board.
func (b *fakeBoard) Replace(_ context.Context, job *editjob.Job) (bool, error) {
	if b.err != nil {
		return false, b.err
	}

	if b.result == nil || b.result.Status != editjob.PlannedStatus {
		return false, nil
	}

	b.replaced = append(b.replaced, *job)
	b.job, b.result = job, nil

	return true, nil
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

// testStore makes result keys like media.Store with the prefix "edits" and keeps the result files in memory.
type testStore struct {
	files map[string][]byte
	err   error
}

func (testStore) ResultKey(sourceKey, name, ext string) (string, error) {
	info, err := media.ParseKey(sourceKey)
	if err != nil {
		return "", err
	}

	return media.ResultKey("edits", info, name, ext)
}

func (s testStore) Read(_ context.Context, key string, _ int64) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}

	data, ok := s.files[key]
	if !ok {
		return nil, fmt.Errorf("failed to read: %w", media.ErrFileNotFound)
	}

	return data, nil
}

// fakeDecider answers with the outcome or the error and records the requests.
type fakeDecider struct {
	outcome  *editplan.Outcome
	err      error
	requests []editplan.Request
}

func (d *fakeDecider) Decide(_ context.Context, req *editplan.Request) (*editplan.Outcome, error) {
	d.requests = append(d.requests, *req)

	return d.outcome, d.err
}

type testDeps struct {
	board   *fakeBoard
	store   testStore
	decider *fakeDecider
	tech    *fakeTech
}

func newTestExecutor(b *fakeBoard) *Executor {
	return newTestExecutorWithTech(b, &fakeTech{})
}

func newTestExecutorWithTech(b *fakeBoard, tech *fakeTech) *Executor {
	return newTestExecutorWith(&testDeps{board: b, store: testStore{}, decider: &fakeDecider{}, tech: tech})
}

func newTestExecutorWith(d *testDeps) *Executor {
	e := NewExecutor(d.board, d.store, d.decider, d.tech, &config.Editor{
		JobTimeout: testTimeout, QueueTimeout: testQueue, WorkerWait: testWorker, PollInterval: testPoll,
	}, slog.New(slog.DiscardHandler))
	e.now = func() time.Time { return testNow }

	return e
}

func editData() *tasks.ExecuteData {
	return &tasks.ExecuteData{
		TaskID: testTaskID, Agent: AgentName, Kind: "reels-edit", Input: testCaption, Media: []string{testSourceKey},
		Version: 1,
	}
}

func revisionData() *tasks.ExecuteData {
	data := editData()
	data.Version, data.Revision = 2, testRevision

	return data
}

// leftOneRepeat: the first draft kept the disputed repeat of testPlan.
const leftOneRepeat = "Спорные повторы оставил в ролике: 1."

// testPlan is the plan the worker stored: 45 s of a 60 s source, one disputed repeat.
func testPlan() *roughcut.Plan {
	return &roughcut.Plan{
		Version: roughcut.PlanVersion, SourceDuration: 60, Duration: 45,
		Settings: roughcut.Settings{MaxPause: 0.4, PadBefore: 0.08, PadAfter: 0.12, RetakeSimilarity: 0.7},
		Keep:     []roughcut.Interval{{Start: 0.4, End: 45.4}},
		Candidates: []roughcut.Candidate{{
			ID: 0, EarlierWords: roughcut.WordRange{From: 1, To: 2}, LaterWords: roughcut.WordRange{From: 3, To: 4},
		}},
	}
}

// storedFiles are the transcript of the source and the plan of version 1, as the worker stores them.
func storedFiles(t *testing.T) map[string][]byte {
	t.Helper()

	files, err := editjob.Files(testStore{}, testSourceKey, 1)
	require.NoError(t, err)

	transcript, err := json.Marshal(&transcription.Result{Language: "ru", Duration: 60, Words: []transcription.Word{
		{Word: "привет", Start: 0.5, End: 1}, {Word: "держим", Start: 1.2, End: 1.6}, {Word: "планку", Start: 1.7, End: 2.1},
		{Word: "держим", Start: 3, End: 3.4}, {Word: "планку", Start: 3.5, End: 3.9},
	}})
	require.NoError(t, err)

	plan, err := json.Marshal(testPlan())
	require.NoError(t, err)

	return map[string][]byte{files.Transcript: transcript, files.Plan: plan}
}

func editOutcome() *editplan.Outcome {
	return &editplan.Outcome{
		Kind:    editplan.EditOutcome,
		Edit:    roughcut.Edit{Remove: []roughcut.WordRange{{From: 0, To: 0}}, Notes: []string{"начало убрано"}},
		Changes: []string{testChange},
		Cut:     1,
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

// TestExecutorSubmitsAndDefers: the first version is one auto job; with the trainer's caption the worker stops after
// the plan for the decision, without it the worker renders at once unless there are disputed repeats. The model is
// not asked before the worker's plan.
func TestExecutorSubmitsAndDefers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		caption string
		decide  bool
	}{
		{name: "with a caption", caption: testCaption, decide: true},
		{name: "without a caption", caption: "  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b, decider := &fakeBoard{}, &fakeDecider{}
			data := editData()
			data.Input = tt.caption

			_, err := newTestExecutorWith(&testDeps{board: b, decider: decider, tech: &fakeTech{}}).Execute(t.Context(), data)

			var deferred *tasks.DeferredError
			require.ErrorAs(t, err, &deferred)
			assert.Equal(t, testPoll, deferred.After)

			require.Len(t, b.submitted, 1)
			assert.Equal(t, editjob.Job{
				ID: testJobID, TaskID: testTaskID, Version: 1, SourceKey: testSourceKey,
				CreatedAt: testNow, QueueDeadline: testNow.Add(testQueue), Timeout: testTimeout,
				Stage: editjob.AutoStage, Decide: tt.decide, RevisionHash: editjob.TextHash(tt.caption),
			}, b.submitted[0])
			assert.Empty(t, decider.requests, "no model call before the plan")
		})
	}
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

	decider := &fakeDecider{}

	out, err := newTestExecutorWith(&testDeps{board: &fakeBoard{result: done}, decider: decider, tech: &fakeTech{}}).
		Execute(t.Context(), editData())
	require.NoError(t, err)
	assert.Equal(t, testReelKey, out.MediaKey, "the key is computed by the app, not taken from the worker")
	assert.Equal(t, Describe(done, 1, testCaption, nil), out.Text)
	assert.Empty(t, decider.requests, "nothing to decide: no model call")
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
			name:      "the worker could not build the decided plan: technical",
			board:     &fakeBoard{result: &editjob.Result{JobID: testJobID, Status: editjob.FailedStatus, Code: editjob.InvalidEditCode}},
			permanent: technicalText,
			forgotten: true,
			reported:  "editor: Монтажёр: задача 17 не выполнена, код invalid_edit. Подробности — в логах app и media-worker.",
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

// TestExecutorDecidesPlannedJob: the worker stopped after the plan; the model decides on the caption and the repeats
// with the stored transcript and plan, and the job is replaced by its render with the checked edit and the plan's
// settings. A caption the editor cannot apply still gives the first draft: the automatic cut with a note.
func TestExecutorDecidesPlannedJob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		decider  *fakeDecider
		edit     *roughcut.Edit
		expected *editjob.Summary
	}{
		{
			name: "the caption applied", decider: &fakeDecider{outcome: editOutcome()}, edit: &editOutcome().Edit,
			expected: &editjob.Summary{Changes: []string{
				testChange, "Спорные повторы: вырезал 1, оставил 0.",
			}},
		},
		{
			name:    "the caption cannot be applied: the automatic cut",
			decider: &fakeDecider{outcome: &editplan.Outcome{Kind: editplan.CannotOutcome, Reason: "музыка"}},
			expected: &editjob.Summary{
				Changes: []string{leftOneRepeat}, Note: captionCannotNote,
			},
		},
		{
			name:    "the model's answer was cut: the automatic cut, a technical note",
			decider: &fakeDecider{err: fmt.Errorf("run: %w", agents.ErrMaxTokens)},
			expected: &editjob.Summary{
				Changes: []string{leftOneRepeat}, Note: captionFailedNote,
			},
		},
		{
			name:    "the model refused: the automatic cut",
			decider: &fakeDecider{err: fmt.Errorf("run: %w", agents.ErrRefusal)},
			expected: &editjob.Summary{
				Changes: []string{leftOneRepeat}, Note: captionUnclearNote,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			planned := pendingJob(time.Hour)
			planned.Stage, planned.Decide = editjob.AutoStage, true
			b := &fakeBoard{
				job:    planned,
				result: &editjob.Result{JobID: testJobID, Status: editjob.PlannedStatus, Candidates: 1},
			}
			deps := &testDeps{board: b, store: testStore{files: storedFiles(t)}, decider: tt.decider, tech: &fakeTech{}}

			_, err := newTestExecutorWith(deps).Execute(t.Context(), editData())

			var deferred *tasks.DeferredError
			require.ErrorAs(t, err, &deferred)

			require.Len(t, tt.decider.requests, 1)
			req := tt.decider.requests[0]
			assert.Equal(t, testCaption, req.Caption)
			assert.Equal(t, 1, req.Version)
			assert.Equal(t, testPlan(), req.Plan)
			assert.Len(t, req.Transcript.Words, 5)

			require.Len(t, b.replaced, 1)
			render := b.replaced[0]
			assert.Equal(t, testJobID, render.ID, "the same job ID: the version's job")
			assert.Equal(t, editjob.RenderStage, render.Stage)
			assert.Equal(t, &testPlan().Settings, render.Settings, "the settings of the plan the model saw")
			assert.Equal(t, tt.edit, render.Edit)
			assert.Equal(t, tt.expected, render.Summary)
			assert.Empty(t, b.submitted)
		})
	}
}

// TestExecutorRevision: a revision of version 1 asks the model with the transcript and the plan of version 1 and the
// revision text, and submits the render of version 2 (from the stored transcript: no transcription).
func TestExecutorRevision(t *testing.T) {
	t.Parallel()

	b, decider := &fakeBoard{}, &fakeDecider{outcome: editOutcome()}
	deps := &testDeps{board: b, store: testStore{files: storedFiles(t)}, decider: decider, tech: &fakeTech{}}

	_, err := newTestExecutorWith(deps).Execute(t.Context(), revisionData())

	var deferred *tasks.DeferredError
	require.ErrorAs(t, err, &deferred)

	require.Len(t, decider.requests, 1)
	assert.Equal(t, testRevision, decider.requests[0].Revision)
	assert.Equal(t, 2, decider.requests[0].Version)
	assert.True(t, decider.requests[0].IsRevision())

	require.Len(t, b.submitted, 1)
	job := b.submitted[0]
	assert.Equal(t, testJob2ID, job.ID)
	assert.Equal(t, editjob.RenderStage, job.Stage, "the worker renders from the stored transcript")
	assert.Equal(t, &editOutcome().Edit, job.Edit)
	assert.Equal(t, &testPlan().Settings, job.Settings)
	assert.Equal(t, &editjob.Summary{Changes: editOutcome().Changes, PreviousDuration: 45}, job.Summary)
	require.NoError(t, job.Validate())

	b.result = &editjob.Result{JobID: job.ID, Status: editjob.DoneStatus, SourceDuration: 60, Duration: 42}

	out, err := newTestExecutorWith(deps).Execute(t.Context(), revisionData())
	require.NoError(t, err)
	assert.Equal(t, testReel2Key, out.MediaKey, "version 2 has its own video")
	assert.Equal(t, "Новая версия рилс: было 0:45, стало 0:42.\n"+testChange, out.Text)
	assert.Len(t, decider.requests, 1, "a finished version is not decided again")
}

// TestExecutorRevisionRefused: a revision the editor cannot do or did not understand gets the trainer's text and no
// job; so does an empty revision (without a model call) and a revision of a version whose files are gone.
func TestExecutorRevisionRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		outcome   *editplan.Outcome
		data      func(d *tasks.ExecuteData)
		noFiles   bool
		expected  string
		noRequest bool
	}{
		{name: "cannot", outcome: &editplan.Outcome{Kind: editplan.CannotOutcome}, expected: cannotRevisionText},
		{name: "unclear", outcome: &editplan.Outcome{Kind: editplan.UnclearOutcome}, expected: unclearRevisionText},
		{
			name: "invalid twice", outcome: &editplan.Outcome{Kind: editplan.InvalidOutcome, Reason: "нет слова 99"},
			expected: unclearRevisionText,
		},
		{
			name: "no decision", outcome: &editplan.Outcome{Kind: editplan.NoAnswerOutcome},
			expected: unclearRevisionText,
		},
		{
			name: "empty revision", data: func(d *tasks.ExecuteData) { d.Revision = " " },
			expected: unclearRevisionText, noRequest: true,
		},
		{name: "files of the version are gone", noFiles: true, expected: artifactsMissingText, noRequest: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			files := storedFiles(t)
			if tt.noFiles {
				files = nil
			}

			b, decider := &fakeBoard{}, &fakeDecider{outcome: tt.outcome}
			data := revisionData()

			if tt.data != nil {
				tt.data(data)
			}

			_, err := newTestExecutorWith(&testDeps{board: b, store: testStore{files: files}, decider: decider,
				tech: &fakeTech{}}).Execute(t.Context(), data)

			var permanent *tasks.PermanentError
			require.ErrorAs(t, err, &permanent)
			assert.Equal(t, tt.expected, permanent.Message)
			assert.Empty(t, b.submitted, "no new version")
			assert.Equal(t, tt.noRequest, len(decider.requests) == 0)
		})
	}
}

// TestExecutorModelErrors: an unavailable model never leaves the task hanging: a refused key is a technical failure
// told to «Техничка», a temporary one is retried by the task and ends with a plain text, a refusal of the model is
// a revision not understood.
func TestExecutorModelErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		permanent string
		temporary bool
		reported  bool
	}{
		{
			name: "no API key", err: fmt.Errorf("run: %w", anthropic.ErrAPIKeyMissing), permanent: technicalRevisionText,
			reported: true,
		},
		{
			name: "the key is refused", err: &anthropic.APIError{StatusCode: http.StatusUnauthorized},
			permanent: technicalRevisionText, reported: true,
		},
		{name: "overloaded", err: &anthropic.APIError{StatusCode: http.StatusServiceUnavailable}, temporary: true},
		{name: "the network", err: errors.New("connection reset"), temporary: true},
		{name: "the model refused", err: fmt.Errorf("run: %w", agents.ErrRefusal), permanent: unclearRevisionText},
		{
			name: "too many steps: ours, not the trainer's", err: fmt.Errorf("run: %w", agents.ErrIterationLimit),
			permanent: technicalRevisionText, reported: true,
		},
		{
			name: "the answer was cut", err: fmt.Errorf("run: %w", agents.ErrMaxTokens),
			permanent: technicalRevisionText, reported: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b, tech := &fakeBoard{}, &fakeTech{}
			deps := &testDeps{board: b, store: testStore{files: storedFiles(t)}, decider: &fakeDecider{err: tt.err},
				tech: tech}

			_, err := newTestExecutorWith(deps).Execute(t.Context(), revisionData())
			require.Error(t, err)

			if tt.temporary {
				var temporary *tasks.TemporaryError
				require.ErrorAs(t, err, &temporary)
				assert.Equal(t, modelFailedRevisionText, temporary.Message)
			} else {
				var permanent *tasks.PermanentError
				require.ErrorAs(t, err, &permanent)
				assert.Equal(t, tt.permanent, permanent.Message)
			}

			assert.Equal(t, tt.reported, len(tech.reports) == 1)
			assert.Empty(t, b.submitted)
		})
	}
}

// TestExecutorStorageError: S3 down while reading the plan is retried by the task.
func TestExecutorStorageError(t *testing.T) {
	t.Parallel()

	errS3Down := errors.New("s3 is down")
	deps := &testDeps{board: &fakeBoard{}, store: testStore{err: errS3Down}, decider: &fakeDecider{}, tech: &fakeTech{}}

	_, err := newTestExecutorWith(deps).Execute(t.Context(), revisionData())
	require.ErrorIs(t, err, errS3Down)

	var permanent *tasks.PermanentError
	assert.NotErrorAs(t, err, &permanent)
}

// TestExecutorRetriesRender: a temporary failure of a decided render queues the same render again with new deadlines
// (the edit is kept: no new transcription, no new model call); the attempt is counted by the task.
func TestExecutorRetriesRender(t *testing.T) {
	t.Parallel()

	render := pendingJob(2 * time.Hour)
	render.ID, render.Version, render.Stage = testJob2ID, 2, editjob.RenderStage
	render.Settings, render.Edit = &testPlan().Settings, &editOutcome().Edit
	render.RevisionHash = editjob.TextHash(testRevision)
	b := &fakeBoard{
		job:    render,
		result: &editjob.Result{JobID: render.ID, Status: editjob.FailedStatus, Code: editjob.TemporaryCode},
	}
	decider := &fakeDecider{}

	_, err := newTestExecutorWith(&testDeps{board: b, decider: decider, tech: &fakeTech{}}).Execute(t.Context(), revisionData())

	var temporary *tasks.TemporaryError
	require.ErrorAs(t, err, &temporary)
	assert.Equal(t, editFailedRevisionText, temporary.Message)

	require.Len(t, b.retried, 1)
	assert.Equal(t, editjob.RenderStage, b.retried[0].Stage)
	assert.Equal(t, render.Edit, b.retried[0].Edit, "the same decision")
	assert.Equal(t, testNow, b.retried[0].CreatedAt)
	assert.Equal(t, testNow.Add(testQueue), b.retried[0].QueueDeadline)
	assert.Empty(t, b.forgotten, "the render is kept")
	assert.Empty(t, decider.requests, "no new model call")

	_, err = newTestExecutorWith(&testDeps{board: b, decider: decider, tech: &fakeTech{}}).Execute(t.Context(), revisionData())

	var deferred *tasks.DeferredError
	require.ErrorAs(t, err, &deferred, "the next run waits for the queued render")
}

// TestExecutorLastAttemptForgetsRender: on the last attempt a failed render is not queued again (the task is given
// up, its result would wait for the next revision): it is forgotten.
func TestExecutorLastAttemptForgetsRender(t *testing.T) {
	t.Parallel()

	render := pendingJob(time.Hour)
	render.ID, render.Version, render.Stage = testJob2ID, 2, editjob.RenderStage
	render.Settings, render.RevisionHash = &testPlan().Settings, editjob.TextHash(testRevision)
	b := &fakeBoard{
		job:    render,
		result: &editjob.Result{JobID: render.ID, Status: editjob.FailedStatus, Code: editjob.TemporaryCode},
	}
	data := revisionData()
	data.LastAttempt = true

	_, err := newTestExecutorWith(&testDeps{board: b, decider: &fakeDecider{}, tech: &fakeTech{}}).Execute(t.Context(), data)

	var temporary *tasks.TemporaryError
	require.ErrorAs(t, err, &temporary)
	assert.Empty(t, b.retried, "not queued again")
	assert.Equal(t, []string{render.ID}, b.forgotten)
}

// TestExecutorStaleRevision: a job or a result of this version left by another revision (given up, finished late) is
// not given to the new revision: it is forgotten and the new revision is decided and submitted.
func TestExecutorStaleRevision(t *testing.T) {
	t.Parallel()

	old := pendingJob(time.Hour)
	old.ID, old.Version, old.Stage = testJob2ID, 2, editjob.RenderStage
	old.Settings, old.RevisionHash = &testPlan().Settings, editjob.TextHash("оставь второй дубль")
	done := &editjob.Result{JobID: old.ID, Status: editjob.DoneStatus, SourceDuration: 60, Duration: 30}

	tests := []struct {
		name string
		job  *editjob.Job
	}{
		{name: "a job of another revision", job: old},
		{name: "a result without its job"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := &fakeBoard{job: tt.job, result: done}
			decider := &fakeDecider{outcome: editOutcome()}
			deps := &testDeps{board: b, store: testStore{files: storedFiles(t)}, decider: decider, tech: &fakeTech{}}

			_, err := newTestExecutorWith(deps).Execute(t.Context(), revisionData())

			var deferred *tasks.DeferredError
			require.ErrorAs(t, err, &deferred, "the old reel is not the draft")
			assert.Equal(t, []string{old.ID}, b.forgotten)
			require.Len(t, decider.requests, 1)
			assert.Equal(t, testRevision, decider.requests[0].Revision)
			require.Len(t, b.submitted, 1)
			assert.Equal(t, editjob.TextHash(testRevision), b.submitted[0].RevisionHash)
		})
	}
}
