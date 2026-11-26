package mediaworker

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editlocal"
	"github.com/Prokud1n/ai-agents/internal/providers/ffmpeg"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
	"github.com/Prokud1n/ai-agents/internal/redistest"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	leaseTask = 41
	// leaseTTL of the test: short, so the lease of the «killed» run expires soon after its last refresh.
	leaseTTL = time.Second
	// leaseCancelled: the lease expires (leaseTTL) and the worker sees it at its next check of the board.
	leaseCancelled = leaseTTL + boardCheckInterval + 2*time.Second
	// leaseDropEvery: how often the sleeping run's lease is deleted while the first transcription hangs.
	leaseDropEvery = 50 * time.Millisecond
)

// TestIntegrationLeaseOfKilledRun is make edit killed (docker kill) while the worker transcribes its job: the job
// carries a lease that nobody refreshes any more; the worker cancels it within the lease TTL and its board check —
// Whisper gets a cancelled context, the entry is acked with a temporary failure — instead of rendering it for nobody.
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

	result, found, err := board.Result(ctx, job.ID)
	require.NoError(t, err)
	require.True(t, found, "a temporary failure: a run that only slept retries at once, a killed one's expires")
	assert.Equal(t, editjob.FailedStatus, result.Status)
	assert.Equal(t, editjob.TemporaryCode, result.Code)
}

// sleepyTranscriber is Whisper of a make edit run that slept past its lease TTL (a Mac asleep, a paused Docker VM): the
// first call hangs while the lease is gone (drop deletes it again and again, as if nobody refreshed it), the next ones
// answer with the speech of the clip.
type sleepyTranscriber struct {
	calls atomic.Int32
	drop  func(ctx context.Context)
}

func (s *sleepyTranscriber) Transcribe(ctx context.Context, _ transcription.TranscribeData) (*transcription.Result, error) {
	if s.calls.Add(1) > 1 {
		return clipTranscript(), nil
	}

	ticker := time.NewTicker(leaseDropEvery)
	defer ticker.Stop()

	for {
		s.drop(ctx)

		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("failed to transcribe: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// dropLeases deletes every lease of the stream.
func dropLeases(ctx context.Context, client *redis.Client, stream string) {
	iter := client.Conn().Scan(ctx, 0, stream+":lease:*", 0).Iterator()

	for iter.Next(ctx) {
		_ = client.Conn().Del(ctx, iter.Val()).Err() // best effort: the next tick deletes it again
	}
}

// TestIntegrationLeaseLostByLiveRun: the lease of a live make edit is lost (it slept past the TTL) while the worker
// transcribes: the worker stores a temporary failure at once, and the run retries — a new job with its lease taken
// again — and makes the version, instead of waiting for the job's timeout.
func TestIntegrationLeaseLostByLiveRun(t *testing.T) {
	t.Parallel()

	client := redistest.Connect(t)
	prefix := redistest.Prefix(t, client)
	speech := &sleepyTranscriber{drop: func(ctx context.Context) { dropLeases(ctx, client, prefix+localStream) }}
	runner := newLocalRunnerOn(t, client, prefix, speech, &scriptedDecider{}, false)

	clip := filepath.Join(t.TempDir(), "source.mkv")
	makeSpeechClip(t, clip, verticalSize, 30, false)

	ctx, cancel := context.WithTimeout(t.Context(), editWait)
	defer cancel()

	out := t.TempDir()
	began := time.Now()

	report, err := runner.First(ctx, &editlocal.FirstData{Path: clip, OutDir: out})
	require.NoError(t, err)
	assert.True(t, report.Done)
	assert.Equal(t, int32(2), speech.calls.Load(), "the job of the lost lease failed, the retry made the version")
	assert.Less(t, time.Since(began), editWait/2, "retried at once, not after the job's timeout")
	assert.FileExists(t, filepath.Join(out, editlocal.VersionDir(1), editlocal.ReelFile))
}
