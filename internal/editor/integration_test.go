package editor

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/mediaworker"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
	"github.com/Prokud1n/ai-agents/internal/redistest"
	"github.com/Prokud1n/ai-agents/internal/s3test"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	integrationWait = 15 * time.Second
	integrationTick = 50 * time.Millisecond
	sourceContent   = "source-video"
)

// fakePipeline edits like the real one, without ffmpeg: it downloads the source from S3 and stores a "reel" made of
// it under the job's key; failWith makes it fail with the code instead.
type fakePipeline struct {
	store    *media.Store
	failWith editjob.FailureCode

	mu    sync.Mutex
	calls int
}

func (p *fakePipeline) Run(ctx context.Context, job *editjob.Job) (*editjob.Result, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()

	result := &editjob.Result{JobID: job.ID, SourceDuration: 182}
	if p.failWith != "" {
		return result, &mediaworker.JobError{Code: p.failWith, Err: os.ErrInvalid}
	}

	local, err := p.store.Download(ctx, job.SourceKey, os.TempDir(), 1<<20)
	if err != nil {
		return result, err
	}

	defer func() { _ = local.Remove() }()

	data, err := os.ReadFile(local.Path)
	if err != nil {
		return result, err
	}

	files, err := editjob.Files(p.store, job.SourceKey, job.Version)
	if err != nil {
		return result, err
	}

	if err := p.store.PutResult(ctx, files.Reel, strings.NewReader("reel of "+string(data)), editjob.VideoContentType); err != nil {
		return result, err
	}

	result.Status, result.Duration = editjob.DoneStatus, 141

	return result, nil
}

func (p *fakePipeline) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.calls
}

// recordingWaker records the woken tasks.
type recordingWaker struct {
	mu  sync.Mutex
	ids []int64
}

func (w *recordingWaker) Wake(_ context.Context, taskID int64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.ids = append(w.ids, taskID)

	return nil
}

func (w *recordingWaker) Count(taskID int64) int {
	w.mu.Lock()
	defer w.mu.Unlock()

	n := 0

	for _, id := range w.ids {
		if id == taskID {
			n++
		}
	}

	return n
}

type exchangeEnv struct {
	client   *redis.Client
	board    *editjob.Board
	store    *media.Store
	cfg      *config.Editor
	executor *Executor
	wakes    *recordingWaker
	source   string
}

// startExchange connects the app's side (Executor, Waker) and the worker's (mediaworker.Worker with the fake
// pipeline) through a real Redis and S3, as in the compose stack.
func startExchange(t *testing.T, pipeline *fakePipeline) (*exchangeEnv, *editjob.Board) {
	t.Helper()

	log := slog.New(slog.DiscardHandler)
	client := redistest.Connect(t)

	store, err := media.NewStore(s3test.Connect(t),
		&config.Media{KeyPrefix: "incoming", ResultPrefix: "edits", UploadTimeout: time.Minute}, log)
	require.NoError(t, err)

	cfg := &config.Editor{Stream: redistest.Prefix(t, client) + "editor:jobs", JobTimeout: time.Minute, PollInterval: time.Second}

	board, err := editjob.NewBoard(client, cfg, log)
	require.NoError(t, err)

	source, err := store.Save(t.Context(), &media.File{
		ChatID: -100123, MessageID: 42, FileUniqueID: "uniq", MIMEType: "video/mp4",
	}, strings.NewReader(sourceContent))
	require.NoError(t, err)

	pipeline.store = store
	env := &exchangeEnv{
		client: client, board: board, store: store, cfg: cfg, executor: NewExecutor(board, store, cfg, log),
		wakes: &recordingWaker{}, source: source,
	}

	waker := NewWaker(board, env.wakes, log)
	require.NoError(t, waker.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, waker.Stop(context.Background())) })

	// The waker subscribes in the background: wait until Redis counts it, so no announcement is missed.
	done := cfg.Stream + ":done"
	require.Eventually(t, func() bool {
		counts, err := client.Conn().PubSubNumSub(context.Background(), done).Result()

		return err == nil && counts[done] > 0
	}, integrationWait, integrationTick)

	return env, board
}

func startWorker(t *testing.T, board *editjob.Board, pipeline *fakePipeline) {
	t.Helper()

	worker := mediaworker.NewWorker(board, pipeline, &config.MediaWorker{WorkDir: t.TempDir()},
		slog.New(slog.DiscardHandler))
	require.NoError(t, worker.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, worker.Stop(context.Background())) })
}

func (env *exchangeEnv) data(taskID int64) *tasks.ExecuteData {
	return &tasks.ExecuteData{
		TaskID: taskID, Agent: AgentName, Kind: "reels-edit", Input: "подпись", Media: []string{env.source}, Version: 1,
	}
}

// TestIntegrationEditorWorkerExchange: the app submits the job once however many times the task runs, the worker
// edits it, stores the reel in S3 and wakes the task; the app's draft is the reel under the key it computes itself.
// A redelivered stream entry of a finished job is skipped by the worker (announced again, not edited again).
func TestIntegrationEditorWorkerExchange(t *testing.T) {
	t.Parallel()

	pipeline := &fakePipeline{}
	env, board := startExchange(t, pipeline)
	ctx := t.Context()
	data := env.data(31)

	for range 2 { // two runs before the worker starts: one job
		_, err := env.executor.Execute(ctx, data)

		var deferred *tasks.DeferredError
		require.ErrorAs(t, err, &deferred)
	}

	queued, err := env.client.Conn().XLen(ctx, env.cfg.Stream).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), queued, "the job is queued once")

	startWorker(t, board, pipeline)

	require.Eventually(t, func() bool { return env.wakes.Count(31) > 0 }, integrationWait, integrationTick,
		"the worker wakes the task")

	out, err := env.executor.Execute(ctx, data)
	require.NoError(t, err)
	assert.Equal(t, "edits/-100123/42/uniq-v1-reel.mp4", out.MediaKey)
	assert.Contains(t, out.Text, "было 3:02, стало 2:21")
	assert.Contains(t, out.Text, "«подпись»")

	reel, err := env.store.Download(ctx, out.MediaKey, t.TempDir(), 1<<20)
	require.NoError(t, err)

	content, err := os.ReadFile(reel.Path)
	require.NoError(t, err)
	assert.Equal(t, "reel of "+sourceContent, string(content))

	again, err := env.executor.Execute(ctx, data)
	require.NoError(t, err)
	assert.Equal(t, out, again, "a repeated run finds the same result")

	// The same job delivered again (a lost ack): skipped, announced once more.
	job, found, err := board.Job(ctx, editjob.JobID(31, 1))
	require.NoError(t, err)
	require.True(t, found)
	resubmitEntry(t, env, &job)

	require.Eventually(t, func() bool { return env.wakes.Count(31) > 1 }, integrationWait, integrationTick)
	assert.Equal(t, 1, pipeline.Calls(), "a finished job is not edited again")
}

// TestIntegrationEditorWorkerFailure: a failed edit reaches the app as a permanent error with the trainer's text, and
// is forgotten, so the next run submits the job again.
func TestIntegrationEditorWorkerFailure(t *testing.T) {
	t.Parallel()

	pipeline := &fakePipeline{failWith: editjob.NoAudioCode}
	env, board := startExchange(t, pipeline)
	ctx := t.Context()
	data := env.data(32)

	_, err := env.executor.Execute(ctx, data)

	var deferred *tasks.DeferredError
	require.ErrorAs(t, err, &deferred)

	startWorker(t, board, pipeline)
	require.Eventually(t, func() bool { return env.wakes.Count(32) > 0 }, integrationWait, integrationTick)

	_, err = env.executor.Execute(ctx, data)

	var permanent *tasks.PermanentError
	require.ErrorAs(t, err, &permanent)
	assert.Equal(t, noAudioText, permanent.Message)

	_, err = env.executor.Execute(ctx, data)
	require.ErrorAs(t, err, &deferred, "the forgotten job is submitted again by the next run")
	require.Eventually(t, func() bool { return pipeline.Calls() == 2 }, integrationWait, integrationTick)
}

// resubmitEntry adds the job to the stream again, as a redelivery would (the entry's field is "job").
func resubmitEntry(t *testing.T, env *exchangeEnv, job *editjob.Job) {
	t.Helper()

	data, err := json.Marshal(job)
	require.NoError(t, err)

	require.NoError(t, env.client.Conn().XAdd(t.Context(), &goredis.XAddArgs{
		Stream: env.cfg.Stream, Values: map[string]any{"job": data},
	}).Err())
}
