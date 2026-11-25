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

// TestIntegrationEditLocalRevision is make edit end to end against a real Redis, S3 and the media worker with the real
// pipeline (ffmpeg; the speech recognition and the editor's model are faked): a local clip becomes version 1 in
// <out>/v1 with its files, then a revision makes version 2 in <out>/v2 from the stored transcript — no new
// transcription — and state.json follows. The synthetic task ID is above editlocal.TaskIDBase.
func TestIntegrationEditLocalRevision(t *testing.T) {
	t.Parallel()

	requireIntegrationFFmpeg(t)

	log := slog.New(slog.DiscardHandler)
	client := redistest.Connect(t)
	prefix := redistest.Prefix(t, client)

	files, err := media.NewStore(s3test.Connect(t),
		&config.Media{KeyPrefix: "incoming", ResultPrefix: "edits", UploadTimeout: time.Minute}, log)
	require.NoError(t, err)

	cfg := &config.Editor{
		Stream: prefix + "editor:jobs", JobTimeout: editWait, PollInterval: time.Second, QueueTimeout: time.Hour,
		WorkerWait: time.Minute,
	}

	board, err := editjob.NewBoard(client, cfg, log)
	require.NoError(t, err)

	transcriber, decider := &countingTranscriber{}, &scriptedDecider{}

	worker := NewWorker(board, realPipelineWith(t, files, transcriber), &config.MediaWorker{WorkDir: t.TempDir()}, log)
	require.NoError(t, worker.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, worker.Stop(context.Background())) })

	clip := filepath.Join(t.TempDir(), "source.mkv")
	makeSpeechClip(t, clip, verticalSize, 30, false)

	runner := editlocal.NewRunner(&editlocal.Params{
		Executor: editor.NewExecutor(board, files, decider, noTech{}, cfg, log), Board: board, Store: files,
		Sequence: editlocal.NewRedisSequence(client, prefix+editlocal.SequenceKey),
		Settings: editlocal.Settings{MaxFileSize: 1 << 30, MaxAttempts: 3, RetryBaseDelay: time.Second,
			RetryMaxDelay: time.Second, PollInterval: editPoll},
		Log: log,
	})

	ctx, cancel := context.WithTimeout(t.Context(), 2*editWait)
	defer cancel()

	out := t.TempDir()

	first, err := runner.First(ctx, &editlocal.FirstData{Path: clip, OutDir: out})
	require.NoError(t, err)
	assert.True(t, first.Done)
	assert.Greater(t, first.TaskID, editlocal.TaskIDBase)
	assert.Equal(t, int32(1), transcriber.calls.Load())
	assert.Empty(t, decider.Requests(), "nothing to decide in the first version")

	for _, name := range editlocal.VersionFiles {
		assert.FileExists(t, filepath.Join(out, "v1", name))
	}

	v1Duration := fileDuration(t, filepath.Join(out, "v1", editlocal.ReelFile))

	second, err := runner.Revise(ctx, &editlocal.ReviseData{OutDir: out, Revision: "убери «привет» в начале"})
	require.NoError(t, err)
	assert.Equal(t, 2, second.Version)
	assert.Equal(t, first.TaskID, second.TaskID)
	assert.Equal(t, int32(1), transcriber.calls.Load(), "the revision is not transcribed again")

	requests := decider.Requests()
	require.Len(t, requests, 1)
	assert.Equal(t, "убери «привет» в начале", requests[0].Revision)

	for _, name := range editlocal.VersionFiles {
		assert.FileExists(t, filepath.Join(out, "v2", name))
	}

	assert.Less(t, fileDuration(t, filepath.Join(out, "v2", editlocal.ReelFile)), v1Duration-0.3, "the first word is cut")

	description, err := os.ReadFile(filepath.Join(out, "v2", editlocal.DescriptionFile))
	require.NoError(t, err)
	assert.Contains(t, string(description), "Вырезал фрагмент «"+cutHello+"».")

	plan, err := os.ReadFile(filepath.Join(out, "v2", editlocal.PlanFile))
	require.NoError(t, err)
	assert.Contains(t, string(plan), `"edit"`, "the plan of version 2 keeps the edit")

	state, err := editlocal.LoadState(out)
	require.NoError(t, err)
	assert.Equal(t, 2, state.Version)
	assert.Equal(t, first.TaskID, state.TaskID)
	assert.Equal(t, []editlocal.Revision{{Version: 2, Text: "убери «привет» в начале"}}, state.Revisions)
}

// fileDuration probes the duration of a local video.
func fileDuration(t *testing.T, path string) float64 {
	t.Helper()

	out := run(t, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path)

	duration, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	require.NoError(t, err)

	return duration
}
