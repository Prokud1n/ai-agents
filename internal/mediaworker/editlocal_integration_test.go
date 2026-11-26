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
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/llmusage"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
	"github.com/Prokud1n/ai-agents/internal/redistest"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/s3test"
)

const (
	localRevision = "убери «привет» в начале"
	localCaption  = "сделай без «привет» в начале"
	localStream   = "editor:jobs"
)

// localDecider is the editor's model as the executor takes it.
type localDecider interface {
	Decide(ctx context.Context, req *editplan.Request) (*editplan.Outcome, error)
}

// localEdit is make edit wired to the media worker with the real pipeline through a real Redis and S3.
type localEdit struct {
	runner      *editlocal.Runner
	transcriber *countingTranscriber
	decider     *scriptedDecider
}

// startLocalEdit runs make edit with the speech of the 6-second clip and the scripted model.
func startLocalEdit(t *testing.T) *localEdit {
	t.Helper()

	env := &localEdit{transcriber: &countingTranscriber{}, decider: &scriptedDecider{}}
	env.runner = newLocalRunner(t, env.transcriber, env.decider, false)

	return env
}

// newLocalRunner is the runner of make edit with the app's executor (hook titles on or off) and the media worker.
func newLocalRunner(t *testing.T, speech transcriber, decider localDecider, hook bool) *editlocal.Runner {
	t.Helper()

	client := redistest.Connect(t)

	return newLocalRunnerOn(t, client, redistest.Prefix(t, client), speech, decider, hook)
}

// newLocalRunnerOn is newLocalRunner on the given Redis and key prefix (the stream is prefix + localStream).
func newLocalRunnerOn(
	t *testing.T,
	client *redis.Client,
	prefix string,
	speech transcriber,
	decider localDecider,
	hook bool,
) *editlocal.Runner {
	t.Helper()

	requireIntegrationFFmpeg(t)

	log := slog.New(slog.DiscardHandler)

	files, err := media.NewStore(s3test.Connect(t),
		&config.Media{KeyPrefix: testKeyPrefix, ResultPrefix: testResultPrefix, UploadTimeout: time.Minute}, log)
	require.NoError(t, err)

	cfg := &config.Editor{
		Stream: prefix + localStream, JobTimeout: editWait, PollInterval: time.Second, QueueTimeout: time.Hour,
		WorkerWait: time.Minute, HookTitle: hook,
	}

	board, err := editjob.NewBoard(client, cfg, log)
	require.NoError(t, err)

	worker := NewWorker(board, realPipelineWith(t, files, speech), &config.MediaWorker{WorkDir: t.TempDir()}, log)
	require.NoError(t, worker.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, worker.Stop(context.Background())) })

	return editlocal.NewRunner(&editlocal.Params{
		Executor: editor.NewExecutor(editlocal.NewLeaseBoard(board), files, decider, noTech{}, cfg, log), Board: board,
		Store:    files,
		Sequence: editlocal.NewRedisSequence(client, prefix+editlocal.SequenceKey),
		Settings: editlocal.Settings{MaxFileSize: 1 << 30, MaxAttempts: 3, RetryBaseDelay: time.Second,
			RetryMaxDelay: time.Second, PollInterval: editPoll},
		Log: log,
	})
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

// freePrices is LLM_PRICES where every model is free.
type freePrices struct{}

func (freePrices) Cost(string, llmusage.Tokens) (int64, bool) { return 0, true }

// TestIntegrationEditLocalHook: with EDITOR_HOOK_TITLE (on by default) the first version of a short talking head goes
// through the model for the hook title — the real decider behind editlocal.UsageMeter, as cmd/edit-local builds it —
// and the revision «убери заголовок» removes it from the stored transcript.
func TestIntegrationEditLocalHook(t *testing.T) {
	t.Parallel()

	speech, model := &talkTranscriber{}, &hookModel{}
	meter := editlocal.NewUsageMeter(model, freePrices{})
	runner := newLocalRunner(t, speech, editplan.NewDecider(meter, slog.New(slog.DiscardHandler)), true)

	words := talkTranscript().Words
	spans := make([]roughcut.Interval, 0, len(words))

	for _, w := range words {
		spans = append(spans, roughcut.Interval{Start: w.Start, End: w.End})
	}

	clip := filepath.Join(t.TempDir(), "talk.mkv")
	makeToneClip(t, clip, talkDuration, spans)

	ctx, cancel := context.WithTimeout(t.Context(), 2*editWait)
	defer cancel()

	out := t.TempDir()

	first, err := runner.First(ctx, &editlocal.FirstData{Path: clip, OutDir: out})
	require.NoError(t, err)
	require.Len(t, model.Prompts(), 1, "the first version goes through the model for the hook")
	assert.Contains(t, first.Output.Text, "Заголовок: «"+hookText+"».")
	assert.Positive(t, meter.Snapshot().ModelTime)

	second, err := runner.Revise(ctx, &editlocal.ReviseData{OutDir: out, Revision: hookRevision})
	require.NoError(t, err)
	require.Len(t, model.Prompts(), 2)
	assert.Contains(t, second.Output.Text, "Убрал заголовок.")
	assert.Equal(t, int32(1), speech.calls.Load(), "the revision is not transcribed again")

	for _, name := range editlocal.VersionFiles {
		assert.FileExists(t, filepath.Join(out, editlocal.VersionDir(2), name))
	}
}
