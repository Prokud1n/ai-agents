package mediaworker

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editlocal"
	"github.com/Prokud1n/ai-agents/internal/editor"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/redistest"
	"github.com/Prokud1n/ai-agents/internal/s3test"
)

const (
	localRevision  = "убери «привет» в начале"
	localCaption   = "сделай без «привет» в начале"
	localKeyPrefix = "incoming"
	localStream    = "editor:jobs"
)

// localEdit is make edit wired to the media worker with the real pipeline through a real Redis and S3.
type localEdit struct {
	runner      *editlocal.Runner
	transcriber *countingTranscriber
	decider     *scriptedDecider
}

func startLocalEdit(t *testing.T) *localEdit {
	t.Helper()

	requireIntegrationFFmpeg(t)

	log := slog.New(slog.DiscardHandler)
	client := redistest.Connect(t)
	prefix := redistest.Prefix(t, client)

	files, err := media.NewStore(s3test.Connect(t),
		&config.Media{KeyPrefix: localKeyPrefix, ResultPrefix: testResultPrefix, UploadTimeout: time.Minute}, log)
	require.NoError(t, err)

	cfg := &config.Editor{
		Stream: prefix + localStream, JobTimeout: editWait, PollInterval: time.Second, QueueTimeout: time.Hour,
		WorkerWait: time.Minute,
	}

	board, err := editjob.NewBoard(client, cfg, log)
	require.NoError(t, err)

	env := &localEdit{transcriber: &countingTranscriber{}, decider: &scriptedDecider{}}

	worker := NewWorker(board, realPipelineWith(t, files, env.transcriber), &config.MediaWorker{WorkDir: t.TempDir()}, log)
	require.NoError(t, worker.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, worker.Stop(context.Background())) })

	env.runner = editlocal.NewRunner(&editlocal.Params{
		Executor: editor.NewExecutor(board, files, env.decider, noTech{}, cfg, log), Board: board, Store: files,
		Sequence: editlocal.NewRedisSequence(client, prefix+editlocal.SequenceKey),
		Settings: editlocal.Settings{MaxFileSize: 1 << 30, MaxAttempts: 3, RetryBaseDelay: time.Second,
			RetryMaxDelay: time.Second, PollInterval: editPoll},
		Log: log,
	})

	return env
}

// TestIntegrationEditLocalRevision is make edit end to end against a real Redis, S3 and the media worker with the real
// pipeline (ffmpeg; the speech recognition and the editor's model are faked): a local clip becomes version 1 in
// <out>/v1 with its files, then a revision makes version 2 in <out>/v2 from the stored transcript — no new
// transcription — and state.json follows. The synthetic task ID is above editlocal.TaskIDBase.
func TestIntegrationEditLocalRevision(t *testing.T) {
	t.Parallel()

	env := startLocalEdit(t)

	clip := filepath.Join(t.TempDir(), "source.mkv")
	makeSpeechClip(t, clip, verticalSize, 30, false)

	ctx, cancel := context.WithTimeout(t.Context(), 2*editWait)
	defer cancel()

	out := t.TempDir()
	v1, v2 := filepath.Join(out, editlocal.VersionDir(1)), filepath.Join(out, editlocal.VersionDir(2))

	first, err := env.runner.First(ctx, &editlocal.FirstData{Path: clip, OutDir: out})
	require.NoError(t, err)
	assert.True(t, first.Done)
	assert.Greater(t, first.TaskID, editlocal.TaskIDBase)
	assert.Equal(t, int32(1), env.transcriber.calls.Load())
	assert.Empty(t, env.decider.Requests(), "nothing to decide in the first version")

	for _, name := range editlocal.VersionFiles {
		assert.FileExists(t, filepath.Join(v1, name))
	}

	v1Duration := fileDuration(t, filepath.Join(v1, editlocal.ReelFile))

	second, err := env.runner.Revise(ctx, &editlocal.ReviseData{OutDir: out, Revision: localRevision})
	require.NoError(t, err)
	assert.Equal(t, 2, second.Version)
	assert.Equal(t, first.TaskID, second.TaskID)
	assert.Equal(t, int32(1), env.transcriber.calls.Load(), "the revision is not transcribed again")

	requests := env.decider.Requests()
	require.Len(t, requests, 1)
	assert.Equal(t, localRevision, requests[0].Revision)

	for _, name := range editlocal.VersionFiles {
		assert.FileExists(t, filepath.Join(v2, name))
	}

	assert.Less(t, fileDuration(t, filepath.Join(v2, editlocal.ReelFile)), v1Duration-0.3, "the first word is cut")

	description, err := os.ReadFile(filepath.Join(v2, editlocal.DescriptionFile))
	require.NoError(t, err)
	assert.Contains(t, string(description), "Вырезал фрагмент «"+cutHello+"».")

	plan, err := os.ReadFile(filepath.Join(v2, editlocal.PlanFile))
	require.NoError(t, err)
	assert.Contains(t, string(plan), `"edit"`, "the plan of version 2 keeps the edit")

	state, err := editlocal.LoadState(out)
	require.NoError(t, err)
	assert.Equal(t, 2, state.Version)
	assert.Equal(t, []editlocal.Revision{{Version: 2, Text: localRevision}}, state.Revisions)
}

// fileDuration probes the duration of a local video.
func fileDuration(t *testing.T, path string) float64 {
	t.Helper()

	out := run(t, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path)

	duration, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	require.NoError(t, err)

	return duration
}

// TestIntegrationEditLocalCaption: with a caption the first version goes planned → the model → render; the timings
// count the time of the plan and of the render apart, and the files are in <out>/v1.
func TestIntegrationEditLocalCaption(t *testing.T) {
	t.Parallel()

	env := startLocalEdit(t)

	clip := filepath.Join(t.TempDir(), "source.mkv")
	makeSpeechClip(t, clip, verticalSize, 30, false)

	ctx, cancel := context.WithTimeout(t.Context(), 2*editWait)
	defer cancel()

	out := t.TempDir()

	report, err := env.runner.First(ctx, &editlocal.FirstData{Path: clip, Caption: localCaption, OutDir: out})
	require.NoError(t, err)
	assert.True(t, report.Done)
	assert.Equal(t, int32(1), env.transcriber.calls.Load(), "the render reads the stored transcript")

	requests := env.decider.Requests()
	require.Len(t, requests, 1)
	assert.Equal(t, localCaption, requests[0].Caption)

	assert.Positive(t, report.Timings.Plan, "the auto job: queue, transcription and plan")
	assert.Positive(t, report.Timings.Render, "the render job after the decision")

	for _, name := range editlocal.VersionFiles {
		assert.FileExists(t, filepath.Join(out, editlocal.VersionDir(1), name))
	}

	description, err := os.ReadFile(filepath.Join(out, editlocal.VersionDir(1), editlocal.DescriptionFile))
	require.NoError(t, err)
	assert.Contains(t, string(description), "Вырезал фрагмент «"+cutHello+"».")
}
