package mediaworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editor"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
	"github.com/Prokud1n/ai-agents/internal/redistest"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	forgottenTask = 31
	// queueWait: the worker reads the stream every 2 s.
	queueWait = 30 * time.Second
	queuePoll = 50 * time.Millisecond
)

// TestIntegrationForgottenJobIsSkipped is the case of a stopped worker: the app gives the task up (no worker took the
// job) and forgets the job, but its entry stays in the stream. The worker started later skips it: no transcription
// (Whisper bills every call), no result, the entry acked. A retry of the task submits the job again under the same ID,
// and it is run once.
func TestIntegrationForgottenJobIsSkipped(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.DiscardHandler)
	client := redistest.Connect(t)

	cfg := &config.Editor{
		Stream: redistest.Prefix(t, client) + "editor:jobs", JobTimeout: time.Minute, PollInterval: time.Second,
		// The app gives up a job nobody took at once when no worker is alive.
		QueueTimeout: time.Hour, WorkerWait: time.Millisecond,
	}

	board, err := editjob.NewBoard(client, cfg, log)
	require.NoError(t, err)

	fakes := newPipelineFakes(t)
	executor := editor.NewExecutor(board, fakes.store, &scriptedDecider{}, noTech{}, cfg, log)
	data := &tasks.ExecuteData{
		TaskID: forgottenTask, Agent: editor.AgentName, Kind: "reels-edit", Media: []string{testSourceKey}, Version: 1,
	}
	id := editjob.JobID(forgottenTask, 1)

	// The worker is stopped: the first run submits the job, the next one finds no worker and fails the task.
	_, err = executor.Execute(t.Context(), data)
	requireDeferred(t, err)

	time.Sleep(2 * cfg.WorkerWait)

	_, err = executor.Execute(t.Context(), data)

	var permanent *tasks.PermanentError
	require.ErrorAs(t, err, &permanent, "no worker: the task fails")

	_, found, err := board.Job(t.Context(), id)
	require.NoError(t, err)
	require.False(t, found, "the job is forgotten")
	entries, err := streamLen(t.Context(), client, cfg.Stream)
	require.NoError(t, err)
	require.Equal(t, int64(1), entries, "its entry is still in the stream")

	// Whisper is counted atomically: the worker transcribes in its own goroutine.
	speech := &countingTranscriber{}

	pipeline, err := NewPipeline(&PipelineParams{
		Store: fakes.store, Prober: fakes.prober, Keys: fakes.keys, Transcriber: speech, Cutter: fakes.cutter,
		Burner: fakes.burner, Log: log,
		Config: &config.MediaWorker{
			WorkDir: t.TempDir(), MaxSourceDuration: 10 * time.Minute, MaxSourceSize: 1 << 20,
			Fit: string(ffmpeg.PadFitMode),
		},
	})
	require.NoError(t, err)

	worker := NewWorker(board, pipeline, &config.MediaWorker{WorkDir: t.TempDir()}, log)
	require.NoError(t, worker.Start(t.Context()))

	stopped := false

	t.Cleanup(func() {
		if !stopped {
			require.NoError(t, worker.Stop(context.Background()))
		}
	})

	require.Eventually(t, func() bool { return queueEmpty(t.Context(), client, cfg.Stream) }, queueWait, queuePoll,
		"the entry of the forgotten job is acked")
	assert.Zero(t, speech.calls.Load(), "the forgotten job is not transcribed")

	_, found, err = board.Result(t.Context(), id)
	require.NoError(t, err)
	assert.False(t, found, "the forgotten job gets no result")

	// A retry of the task submits the job again under the same ID: it is run once.
	out := runTask(t, executor, data)
	assert.NotEmpty(t, out.MediaKey)

	require.Eventually(t, func() bool { return queueEmpty(t.Context(), client, cfg.Stream) }, queueWait, queuePoll)
	require.NoError(t, worker.Stop(context.Background()))

	stopped = true

	assert.Equal(t, int32(1), speech.calls.Load(), "only the job submitted again is transcribed")
}

func requireDeferred(t *testing.T, err error) {
	t.Helper()

	var deferred *tasks.DeferredError
	require.ErrorAs(t, err, &deferred)
}

// runTask runs the task like the task workers do until it is no longer deferred.
func runTask(t *testing.T, executor *editor.Executor, data *tasks.ExecuteData) tasks.Output {
	t.Helper()

	deadline := time.Now().Add(queueWait)

	for {
		out, err := executor.Execute(t.Context(), data)

		var deferred *tasks.DeferredError
		if !errors.As(err, &deferred) {
			require.NoError(t, err)

			return out
		}

		require.True(t, time.Now().Before(deadline), "the job did not finish in %s", queueWait)
		time.Sleep(queuePoll)
	}
}

func streamLen(ctx context.Context, client *redis.Client, stream string) (int64, error) {
	n, err := client.Conn().XLen(ctx, stream).Result()
	if err != nil {
		return 0, fmt.Errorf("failed to read stream length: %w", err)
	}

	return n, nil
}

// queueEmpty: no entry is left in the stream and none is pending (taken and not acked). It runs in the goroutine of
// Eventually, so it reports errors as false instead of failing the test.
func queueEmpty(ctx context.Context, client *redis.Client, stream string) bool {
	pending, err := client.Conn().XPending(ctx, stream, editjob.ConsumerGroup).Result()
	if err != nil {
		return false
	}

	n, err := streamLen(ctx, client, stream)

	return err == nil && pending.Count == 0 && n == 0
}
