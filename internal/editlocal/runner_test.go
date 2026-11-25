package editlocal

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editor"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	testTaskID  = TaskIDBase + 7
	testContent = "video"
)

// step is one run of the fake executor: its output or error.
type step struct {
	output tasks.Output
	err    error
}

type fakeExecutor struct {
	mu    sync.Mutex
	steps []step
	runs  []tasks.ExecuteData
}

func (e *fakeExecutor) Execute(_ context.Context, data *tasks.ExecuteData) (tasks.Output, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.runs = append(e.runs, *data)
	if len(e.runs) > len(e.steps) {
		return tasks.Output{}, errors.New("unexpected run")
	}

	s := e.steps[len(e.runs)-1]

	return s.output, s.err
}

type fakeBoard struct {
	alive bool

	mu        sync.Mutex
	forgotten []string
}

func (b *fakeBoard) Forget(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.forgotten = append(b.forgotten, id)

	return nil
}

func (*fakeBoard) Job(context.Context, string) (editjob.Job, bool, error) {
	return editjob.Job{}, false, nil
}

func (b *fakeBoard) WorkerAlive(context.Context) (bool, error) { return b.alive, nil }

func (*fakeBoard) ListenDone(ctx context.Context, _ func(context.Context, int64)) error {
	<-ctx.Done()

	return nil
}

// fakeStore keeps the saved source keys and gives every result file a content of its key. Objects are the saved
// sources and the keys in taken; downloads fail while failDownload is set.
type fakeStore struct {
	mu           sync.Mutex
	saved        []media.File
	keys         map[string]bool
	failDownload bool
	// takeAll: every key of this task is taken (an old source after a reset of the counter).
	takeAll int64
}

func (*fakeStore) Key(f *media.File) (string, error) {
	return media.Key(testKeyPrefix, f)
}

func (s *fakeStore) Save(_ context.Context, f *media.File, _ io.ReadSeeker) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key, err := media.Key(testKeyPrefix, f)
	if err != nil {
		return "", err
	}

	s.saved = append(s.saved, *f)
	s.keys[key] = true

	return key, nil
}

func (s *fakeStore) Read(_ context.Context, key string, _ int64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	info, err := media.ParseKey(key)
	if err != nil {
		return nil, err
	}

	if !s.keys[key] && info.MessageID != s.takeAll {
		return nil, media.ErrFileNotFound
	}

	return []byte(testContent), nil
}

func (s *fakeStore) setFailDownload(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failDownload = fail
}

func (*fakeStore) ResultKey(sourceKey, name, ext string) (string, error) {
	info, err := media.ParseKey(sourceKey)
	if err != nil {
		return "", err
	}

	return media.ResultKey("edits", info, name, ext)
}

func (s *fakeStore) Download(_ context.Context, key, dir string, _ int64) (*media.LocalFile, error) {
	s.mu.Lock()
	fail := s.failDownload
	s.mu.Unlock()

	if fail {
		return nil, errors.New("network")
	}

	file, err := os.CreateTemp(dir, media.TempFilePrefix+"*")
	if err != nil {
		return nil, err
	}

	if _, err := file.WriteString(key); err != nil {
		return nil, errors.Join(err, file.Close())
	}

	return &media.LocalFile{Path: file.Name(), Size: int64(len(key))}, file.Close()
}

// fakeSequence gives testTaskID, then the next ones.
type fakeSequence struct {
	mu   sync.Mutex
	next int64
}

func (s *fakeSequence) NextTaskID(context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := testTaskID + s.next
	s.next++

	return id, nil
}

type runnerEnv struct {
	runner   *Runner
	executor *fakeExecutor
	board    *fakeBoard
	store    *fakeStore
	out      string
	source   string
}

func newRunnerEnv(t *testing.T, alive bool, steps ...step) *runnerEnv {
	t.Helper()

	env := &runnerEnv{
		executor: &fakeExecutor{steps: steps}, board: &fakeBoard{alive: alive}, store: &fakeStore{keys: map[string]bool{}},
		out: t.TempDir(),
	}
	env.source = filepath.Join(t.TempDir(), "Clip.MOV")
	require.NoError(t, os.WriteFile(env.source, []byte(testContent), 0o600))

	env.runner = NewRunner(&Params{
		Executor: env.executor, Board: env.board, Store: env.store, Sequence: &fakeSequence{},
		Settings: Settings{MaxFileSize: 1 << 20, MaxAttempts: 3, RetryBaseDelay: time.Millisecond,
			RetryMaxDelay: time.Millisecond, PollInterval: time.Millisecond, LockStaleAfter: time.Hour},
		Log: slog.New(slog.DiscardHandler),
	})

	return env
}

func done(text string) step {
	return step{output: tasks.Output{Text: text, MediaKey: "edits/1/1/x-reel.mp4"}}
}

func deferred() step {
	return step{err: &tasks.DeferredError{After: time.Millisecond}}
}

func TestRunnerFirstAndRevise(t *testing.T) {
	t.Parallel()

	env := newRunnerEnv(t, true, deferred(), deferred(), done("Черновик 1"), deferred(), done("Черновик 2"))

	first, err := env.runner.First(t.Context(), &FirstData{Path: env.source, Caption: testCaption, OutDir: env.out})
	require.NoError(t, err)
	assert.True(t, first.Done)
	v1 := VersionDir(1)
	assert.Equal(t, v1, first.Dir)
	assert.Equal(t, testTaskID, first.TaskID)

	require.Len(t, env.store.saved, 1)
	saved := env.store.saved[0]
	assert.Equal(t, LocalChatID, saved.ChatID)
	assert.Equal(t, testTaskID, saved.MessageID)
	assert.Regexp(t, `^local-[0-9a-f]{8}$`, saved.FileUniqueID)
	assert.Equal(t, "Clip.MOV", saved.FileName)

	sourceKey := "incoming/1/1000000000000000007/" + saved.FileUniqueID + ".mov"

	for _, name := range VersionFiles {
		assert.FileExists(t, filepath.Join(env.out, v1, name))
	}

	reel, err := os.ReadFile(filepath.Join(env.out, v1, ReelFile))
	require.NoError(t, err)
	assert.Equal(t, "edits/1/1000000000000000007/"+saved.FileUniqueID+"-v1-reel.mp4", string(reel))

	description, err := os.ReadFile(filepath.Join(env.out, v1, DescriptionFile))
	require.NoError(t, err)
	assert.Equal(t, "Черновик 1\n", string(description))

	second, err := env.runner.Revise(t.Context(), &ReviseData{OutDir: env.out, Revision: "  " + testRevision + " "})
	require.NoError(t, err)
	assert.Equal(t, 2, second.Version)
	assert.FileExists(t, filepath.Join(env.out, "v2", ReelFile))

	runs := env.executor.runs
	require.Len(t, runs, 5)
	assert.Equal(t, editor.AgentName, runs[0].Agent)
	assert.Equal(t, "reels-edit", runs[0].Kind)
	assert.Equal(t, testCaption, runs[0].Input)
	assert.Equal(t, []string{sourceKey}, runs[0].Media)
	assert.Equal(t, 1, runs[2].Version)
	assert.Empty(t, runs[2].Revision)
	assert.Equal(t, 2, runs[4].Version)
	assert.Equal(t, testRevision, runs[4].Revision)
	assert.Equal(t, testCaption, runs[4].Input, "the caption stays the task's input")

	state, err := LoadState(env.out)
	require.NoError(t, err)
	assert.Equal(t, 2, state.Version)
	assert.Zero(t, state.Pending)
	assert.Equal(t, sourceKey, state.SourceKey)
	assert.Equal(t, []Revision{{Version: 2, Text: testRevision}}, state.Revisions)
	assert.NoFileExists(t, filepath.Join(env.out, LockFile), "the lock is released")
}

func TestRunnerRefusedRevisionKeepsVersion(t *testing.T) {
	t.Parallel()

	refusal := &tasks.PermanentError{Message: "Не понял, что именно поменять.", Err: errors.New("unclear")}
	env := newRunnerEnv(t, true, done("Черновик 1"), step{err: refusal})

	_, err := env.runner.First(t.Context(), &FirstData{Path: env.source, OutDir: env.out})
	require.NoError(t, err)

	report, err := env.runner.Revise(t.Context(), &ReviseData{OutDir: env.out, Revision: "сделай красиво"})

	var permanent *tasks.PermanentError
	require.ErrorAs(t, err, &permanent)
	assert.Equal(t, "Не понял, что именно поменять.", permanent.Message)
	assert.False(t, report.Done)
	assert.Len(t, env.executor.runs, 2, "a permanent error is not retried")

	state, err := LoadState(env.out)
	require.NoError(t, err)
	assert.Equal(t, 1, state.Version, "the version does not grow")
	assert.NoDirExists(t, filepath.Join(env.out, "v2"))
}

func TestRunnerRetriesTemporaryErrors(t *testing.T) {
	t.Parallel()

	temporary := step{err: &tasks.TemporaryError{Message: "Монтаж не удался.", Err: errors.New("network")}}

	tests := []struct {
		name      string
		steps     []step
		runs      int
		expectErr bool
	}{
		{name: "a retry succeeds", steps: []step{temporary, deferred(), done("ok")}, runs: 3},
		{name: "attempts run out", steps: []step{temporary, temporary, temporary}, runs: 3, expectErr: true},
		{name: "deferred runs are not attempts", steps: []step{temporary, deferred(), deferred(), temporary, done("ok")}, runs: 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := newRunnerEnv(t, true, tt.steps...)

			report, err := env.runner.First(t.Context(), &FirstData{Path: env.source, OutDir: env.out})
			if tt.expectErr {
				var failed *tasks.TemporaryError
				require.ErrorAs(t, err, &failed)
				assert.False(t, report.Done)
				assert.NoFileExists(t, filepath.Join(env.out, StateFile))
			} else {
				require.NoError(t, err)
				assert.True(t, report.Done)
			}

			runs := env.executor.runs
			require.Len(t, runs, tt.runs)

			for i, run := range runs {
				attempt := 1
				for _, previous := range tt.steps[:i] {
					var temp *tasks.TemporaryError
					if errors.As(previous.err, &temp) {
						attempt++
					}
				}

				assert.Equal(t, attempt == 3, run.LastAttempt, "run %d", i)
			}
		})
	}
}

func TestRunnerChecksBeforeUpload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		alive    bool
		file     string
		content  string
		existing bool
		expected error
	}{
		{name: "no worker", alive: false, file: testClip, content: testContent, expected: ErrWorkerNotRunning},
		{name: "not a video", alive: true, file: "notes.txt", content: "text", expected: ErrInvalidSource},
		{name: "empty", alive: true, file: testClip, content: "", expected: ErrInvalidSource},
		{name: "too large", alive: true, file: testClip, content: strings.Repeat("x", 1<<20+1), expected: ErrInvalidSource},
		{name: "an edit is there", alive: true, file: testClip, content: testContent, existing: true, expected: ErrOutInUse},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := newRunnerEnv(t, tt.alive)
			path := filepath.Join(t.TempDir(), tt.file)
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o600))

			if tt.existing {
				require.NoError(t, SaveState(env.out, &State{
					Format: StateFormat, TaskID: testTaskID, SourceKey: testSourceKey, Version: 1,
				}))
			}

			_, err := env.runner.First(t.Context(), &FirstData{Path: path, OutDir: env.out})
			require.ErrorIs(t, err, tt.expected)
			assert.Empty(t, env.store.saved, "nothing is uploaded")
			assert.Empty(t, env.executor.runs)
			assert.NoFileExists(t, filepath.Join(env.out, LockFile), "the lock is released")
		})
	}
}

// TestRunnerInterruptForgetsJob: Ctrl+C (the context cancelled) while the worker edits takes the job of the version
// off the board, so the worker cancels it; no state is written.
func TestRunnerInterruptForgetsJob(t *testing.T) {
	t.Parallel()

	steps := make([]step, 0, 1000)
	for range cap(steps) {
		steps = append(steps, deferred())
	}

	env := newRunnerEnv(t, true, steps...)
	ctx, cancel := context.WithCancel(t.Context())

	go func() {
		for {
			env.executor.mu.Lock()
			runs := len(env.executor.runs)
			env.executor.mu.Unlock()

			if runs >= 3 {
				cancel()

				return
			}

			time.Sleep(time.Millisecond)
		}
	}()

	report, err := env.runner.First(ctx, &FirstData{Path: env.source, OutDir: env.out})
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, report.Done)

	env.board.mu.Lock()
	defer env.board.mu.Unlock()

	assert.Equal(t, []string{"task-1000000000000000007-v1"}, env.board.forgotten)
	assert.NoFileExists(t, filepath.Join(env.out, StateFile))
	assert.NoFileExists(t, filepath.Join(env.out, LockFile))
}

// TestRunnerResumesDownload: a version made but not downloaded stays pending in state.json; a revision waits for it,
// and a run with the output directory alone downloads it without running the edit again.
func TestRunnerResumesDownload(t *testing.T) {
	t.Parallel()

	env := newRunnerEnv(t, true, done("Черновик 1"))
	env.store.setFailDownload(true)

	_, err := env.runner.First(t.Context(), &FirstData{Path: env.source, OutDir: env.out})
	require.Error(t, err)

	state, err := LoadState(env.out)
	require.NoError(t, err)
	assert.Equal(t, 1, state.Pending)
	assert.FileExists(t, filepath.Join(env.out, VersionDir(1), DescriptionFile), "the description is written first")

	_, err = env.runner.Revise(t.Context(), &ReviseData{OutDir: env.out, Revision: testRevision})
	require.ErrorIs(t, err, ErrPendingDownload)

	env.store.setFailDownload(false)

	report, err := env.runner.Resume(t.Context(), env.out)
	require.NoError(t, err)
	assert.True(t, report.Done)
	assert.FileExists(t, filepath.Join(env.out, VersionDir(1), ReelFile))
	assert.Len(t, env.executor.runs, 1, "the edit is not run again")

	state, err = LoadState(env.out)
	require.NoError(t, err)
	assert.Zero(t, state.Pending)

	_, err = env.runner.Resume(t.Context(), env.out)
	require.ErrorIs(t, err, ErrNothingPending)
}

// TestRunnerSkipsTakenKey: after a reset of the Redis counter the task ID of an old edit comes again; its source in S3
// is not overwritten, the next ID is taken.
func TestRunnerSkipsTakenKey(t *testing.T) {
	t.Parallel()

	env := newRunnerEnv(t, true, done("Черновик 1"))
	env.runner.sequence = &takenSequence{store: env.store}

	report, err := env.runner.First(t.Context(), &FirstData{Path: env.source, OutDir: env.out})
	require.NoError(t, err)
	assert.Equal(t, testTaskID+1, report.TaskID)
}

// takenSequence gives testTaskID first and marks every key of it taken, as an old source would be.
type takenSequence struct {
	store *fakeStore
	calls int
}

func (s *takenSequence) NextTaskID(context.Context) (int64, error) {
	s.calls++
	if s.calls == 1 {
		s.store.mu.Lock()
		s.store.takeAll = testTaskID
		s.store.mu.Unlock()
	}

	return testTaskID + int64(s.calls) - 1, nil
}

func TestRunnerReviseNeedsSource(t *testing.T) {
	t.Parallel()

	env := newRunnerEnv(t, true)
	require.NoError(t, SaveState(env.out, &State{
		Format: StateFormat, TaskID: testTaskID, SourceKey: testSourceKey, Version: 1,
	}))

	_, err := env.runner.Revise(t.Context(), &ReviseData{OutDir: env.out, Revision: testRevision})
	require.ErrorIs(t, err, ErrSourceMissing)
	assert.Empty(t, env.executor.runs)
}

func TestRunnerLockedOut(t *testing.T) {
	t.Parallel()

	env := newRunnerEnv(t, true)

	release, err := acquireLock(env.out, time.Hour, time.Now())
	require.NoError(t, err)

	defer release()

	_, err = env.runner.Revise(t.Context(), &ReviseData{OutDir: env.out, Revision: testRevision})
	require.ErrorIs(t, err, ErrOutLocked)
}

func TestRunnerReviseNeedsState(t *testing.T) {
	t.Parallel()

	env := newRunnerEnv(t, true)

	_, err := env.runner.Revise(t.Context(), &ReviseData{OutDir: env.out, Revision: testRevision})
	require.ErrorIs(t, err, ErrNoState)

	_, err = env.runner.Revise(t.Context(), &ReviseData{OutDir: env.out, Revision: "  "})
	require.ErrorIs(t, err, ErrEmptyRevision)
}

func TestBackoff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		attempt  int
		expected time.Duration
	}{
		{name: "first", attempt: 1, expected: 30 * time.Second},
		{name: "second", attempt: 2, expected: time.Minute},
		{name: "third", attempt: 3, expected: 2 * time.Minute},
		{name: "capped", attempt: 10, expected: 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, backoff(30*time.Second, 5*time.Minute, tt.attempt))
		})
	}
}

func TestTimings(t *testing.T) {
	t.Parallel()

	var timings Timings

	timings.add(planStage, 10*time.Second, 0)
	timings.add(planStage, 5*time.Second, 4*time.Second)
	timings.add(renderStage, 20*time.Second, 0)
	timings.add(renderStage, time.Second, 3*time.Second)
	timings.Total = 40 * time.Second

	assert.Equal(t, 11*time.Second, timings.Plan)
	assert.Equal(t, 5*time.Second, timings.Model, "the model's time is at most the elapsed time")
	assert.Equal(t, 20*time.Second, timings.Render)
	assert.Contains(t, timings.Format(), "модель Монтажёра: 5.0 с\n")
	assert.Contains(t, timings.Summary(), "итого 40.0 с")
}

func TestUsageSummary(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "вызовов API 2, $0.012000000", Usage{Calls: 2, CostNano: 12_000_000}.Summary())
	assert.Equal(t, "вызовов API 1, $0.000000000 (ещё 1 без цены в LLM_PRICES)", Usage{Calls: 1, UnpricedCalls: 1}.Summary())
}
