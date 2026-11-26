package mediaworker

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/redistest"
)

const (
	leaseTask = 41
	// leaseTTL of the test: short, so the lease of the «killed» run expires soon after its last refresh.
	leaseTTL = time.Second
	// leaseCancelled: the lease expires (leaseTTL) and the worker sees it at its next check of the board.
	leaseCancelled = leaseTTL + boardCheckInterval + 2*time.Second
)

// TestIntegrationLeaseOfKilledRun is make edit killed (docker kill) while the worker transcribes its job: the job
// carries a lease that nobody refreshes any more; the worker cancels it within the lease TTL and its board check —
// Whisper gets a cancelled context, no result is stored, the entry is acked — instead of rendering it for nobody.
func TestIntegrationLeaseOfKilledRun(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.DiscardHandler)
	client := redistest.Connect(t)

	cfg := &config.Editor{
		Stream: redistest.Prefix(t, client) + "editor:jobs", JobTimeout: time.Hour, PollInterval: time.Second,
		QueueTimeout: time.Hour, WorkerWait: time.Minute,
	}

	board, err := editjob.NewBoard(client, cfg, log)
	require.NoError(t, err)

	fakes := newPipelineFakes(t)
	speech := newBlockingTranscriber()

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
	t.Cleanup(func() { require.NoError(t, worker.Stop(context.Background())) })

	ctx := t.Context()
	now := time.Now()
	job := &editjob.Job{
		ID: editjob.JobID(leaseTask, 1), TaskID: leaseTask, Version: 1, SourceKey: testSourceKey, CreatedAt: now,
		QueueDeadline: now.Add(time.Hour), Timeout: time.Hour, Lease: "make-edit-run",
	}

	// The run holds the lease while it waits, as editlocal does.
	require.NoError(t, board.Lease(ctx, job.ID, job.Lease, leaseTTL))

	submitted, err := board.Submit(ctx, job)
	require.NoError(t, err)
	require.True(t, submitted)

	refresh := time.NewTicker(leaseTTL / 4)
	defer refresh.Stop()

	waitStarted := time.After(queueWait)

wait:
	for {
		select {
		case <-speech.started:
			break wait
		case <-refresh.C:
			require.NoError(t, board.Lease(ctx, job.ID, job.Lease, leaseTTL))
		case <-waitStarted:
			require.Fail(t, "the worker did not start the job")
		}
	}

	// The run is killed: the lease is not refreshed any more.
	refresh.Stop()

	killed := time.Now()

	select {
	case got := <-speech.errs:
		require.ErrorIs(t, got, context.Canceled, "Whisper is cancelled")
		assert.Less(t, time.Since(killed), leaseCancelled, "cancelled within the lease TTL and a board check")
	case <-time.After(2 * leaseCancelled):
		require.Fail(t, "the job of the killed run was not cancelled")
	}

	require.Eventually(t, func() bool { return queueEmpty(ctx, client, cfg.Stream) }, queueWait, queuePoll,
		"the entry is acked")

	_, found, err := board.Result(ctx, job.ID)
	require.NoError(t, err)
	assert.False(t, found, "no result for nobody")
}
