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

const testTaskID = TaskIDBase + 7

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
}

func (*fakeBoard) Job(context.Context, string) (editjob.Job, bool, error) {
	return editjob.Job{}, false, nil
}

func (b *fakeBoard) WorkerAlive(context.Context) (bool, error) { return b.alive, nil }

func (*fakeBoard) ListenDone(ctx context.Context, _ func(context.Context, int64)) error {
	<-ctx.Done()

	return nil
}

// fakeStore keeps the saved source keys and gives every result file a content of its key.
type fakeStore struct {
	mu    sync.Mutex
	saved []media.File
}

func (s *fakeStore) Save(_ context.Context, f *media.File, _ io.ReadSeeker) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.saved = append(s.saved, *f)

	return media.Key("incoming", f)
}

func (*fakeStore) ResultKey(sourceKey, name, ext string) (string, error) {
	info, err := media.ParseKey(sourceKey)
	if err != nil {
		return "", err
	}

	return media.ResultKey("edits", info, name, ext)
}

func (*fakeStore) Download(_ context.Context, key, dir string, _ int64) (*media.LocalFile, error) {
	file, err := os.CreateTemp(dir, media.TempFilePrefix+"*")
	if err != nil {
		return nil, err
	}

	if _, err := file.WriteString(key); err != nil {
		return nil, errors.Join(err, file.Close())
	}

	return &media.LocalFile{Path: file.Name(), Size: int64(len(key))}, file.Close()
}

type fakeSequence struct{}

func (fakeSequence) NextTaskID(context.Context) (int64, error) { return testTaskID, nil }

type runnerEnv struct {
	runner   *Runner
	executor *fakeExecutor
	store    *fakeStore
	out      string
	source   string
}

func newRunnerEnv(t *testing.T, alive bool, steps ...step) *runnerEnv {
	t.Helper()

	env := &runnerEnv{executor: &fakeExecutor{steps: steps}, store: &fakeStore{}, out: t.TempDir()}
	env.source = filepath.Join(t.TempDir(), "Clip.MOV")
	require.NoError(t, os.WriteFile(env.source, []byte("video"), 0o600))

	env.runner = NewRunner(&Params{
		Executor: env.executor, Board: &fakeBoard{alive: alive}, Store: env.store, Sequence: fakeSequence{},
		Settings: Settings{MaxFileSize: 1 << 20, MaxAttempts: 3, RetryBaseDelay: time.Millisecond,
			RetryMaxDelay: time.Millisecond, PollInterval: time.Millisecond},
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

	first, err := env.runner.First(t.Context(), &FirstData{Path: env.source, Caption: "подпись", OutDir: env.out})
	require.NoError(t, err)
	assert.True(t, first.Done)
	assert.Equal(t, "v1", first.Dir)
	assert.Equal(t, testTaskID, first.TaskID)

	require.Len(t, env.store.saved, 1)
	assert.Equal(t, media.File{ChatID: LocalChatID, MessageID: testTaskID, FileUniqueID: "local", FileName: "Clip.MOV"},
		env.store.saved[0])

	for _, name := range VersionFiles {
		assert.FileExists(t, filepath.Join(env.out, "v1", name))
	}

	reel, err := os.ReadFile(filepath.Join(env.out, "v1", ReelFile))
	require.NoError(t, err)
	assert.Equal(t, "edits/1/1000000000000000007/local-v1-reel.mp4", string(reel))

	description, err := os.ReadFile(filepath.Join(env.out, "v1", DescriptionFile))
	require.NoError(t, err)
	assert.Equal(t, "Черновик 1\n", string(description))

	second, err := env.runner.Revise(t.Context(), &ReviseData{OutDir: env.out, Revision: "  сделай 40 секунд "})
	require.NoError(t, err)
	assert.Equal(t, 2, second.Version)
	assert.FileExists(t, filepath.Join(env.out, "v2", ReelFile))

	runs := env.executor.runs
	require.Len(t, runs, 5)
	assert.Equal(t, editor.AgentName, runs[0].Agent)
	assert.Equal(t, "reels-edit", runs[0].Kind)
	assert.Equal(t, "подпись", runs[0].Input)
	assert.Equal(t, []string{"incoming/1/1000000000000000007/local.mov"}, runs[0].Media)
	assert.Equal(t, 1, runs[2].Version)
	assert.Empty(t, runs[2].Revision)
	assert.Equal(t, 2, runs[4].Version)
	assert.Equal(t, "сделай 40 секунд", runs[4].Revision)
	assert.Equal(t, "подпись", runs[4].Input, "the caption stays the task's input")

	state, err := LoadState(env.out)
	require.NoError(t, err)
	assert.Equal(t, 2, state.Version)
	assert.Equal(t, []Revision{{Version: 2, Text: "сделай 40 секунд"}}, state.Revisions)
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
		{name: "no worker", alive: false, file: "clip.mp4", content: "video", expected: ErrWorkerNotRunning},
		{name: "not a video", alive: true, file: "notes.txt", content: "text", expected: ErrInvalidSource},
		{name: "empty", alive: true, file: "clip.mp4", content: "", expected: ErrInvalidSource},
		{name: "too large", alive: true, file: "clip.mp4", content: strings.Repeat("x", 1<<20+1), expected: ErrInvalidSource},
		{name: "an edit is there", alive: true, file: "clip.mp4", content: "video", existing: true, expected: ErrOutInUse},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := newRunnerEnv(t, tt.alive)
			path := filepath.Join(t.TempDir(), tt.file)
			require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o600))

			if tt.existing {
				require.NoError(t, SaveState(env.out, &State{TaskID: testTaskID, SourceKey: testSourceKey, Version: 1}))
			}

			_, err := env.runner.First(t.Context(), &FirstData{Path: path, OutDir: env.out})
			require.ErrorIs(t, err, tt.expected)
			assert.Empty(t, env.store.saved, "nothing is uploaded")
			assert.Empty(t, env.executor.runs)
		})
	}
}

func TestRunnerReviseNeedsState(t *testing.T) {
	t.Parallel()

	env := newRunnerEnv(t, true)

	_, err := env.runner.Revise(t.Context(), &ReviseData{OutDir: env.out, Revision: "короче"})
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
