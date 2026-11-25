package mediaworker

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editor"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/redistest"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/s3test"
	"github.com/Prokud1n/ai-agents/internal/tasks"
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	// editWait: an edit of the 6-second clip takes seconds; the worker polls the stream every 2 s.
	editWait  = 2 * time.Minute
	editPoll  = 200 * time.Millisecond
	maxReel   = 64 << 20
	cutHello  = "Привет"
	clipChat  = -100123
	clipMsgID = 42
)

// countingTranscriber is the speech recognition of the clip that counts its calls (Whisper bills every call).
type countingTranscriber struct {
	calls atomic.Int32
}

func (c *countingTranscriber) Transcribe(context.Context, transcription.TranscribeData) (*transcription.Result, error) {
	c.calls.Add(1)

	return clipTranscript(), nil
}

// scriptedDecider is the editor's model: it cuts the first word («Привет») and records the requests.
type scriptedDecider struct {
	mu       sync.Mutex
	requests []editplan.Request
}

func (d *scriptedDecider) Decide(_ context.Context, req *editplan.Request) (*editplan.Outcome, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.requests = append(d.requests, *req)

	return &editplan.Outcome{
		Kind:    editplan.EditOutcome,
		Edit:    roughcut.Edit{Remove: []roughcut.WordRange{{From: 0, To: 0}}, Notes: []string{"первое слово убрано"}},
		Changes: []string{"Вырезал фрагмент «" + cutHello + "»."},
	}, nil
}

func (d *scriptedDecider) Requests() []editplan.Request {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]editplan.Request(nil), d.requests...)
}

type noTech struct{}

func (noTech) Report(context.Context, string, string) error { return nil }

type editEnv struct {
	files       *media.Store
	transcriber *countingTranscriber
	decider     *scriptedDecider
	executor    *editor.Executor
	source      string
}

// startEditEnv connects the editor of the app (Executor) and the media worker with the real pipeline (ffmpeg, rough
// cut, subtitles; the speech recognition is faked and counted) through a real Redis and S3, as in the compose stack.
func startEditEnv(t *testing.T) *editEnv {
	t.Helper()

	requireIntegrationFFmpeg(t)

	log := slog.New(slog.DiscardHandler)
	client := redistest.Connect(t)

	files, err := media.NewStore(s3test.Connect(t),
		&config.Media{KeyPrefix: testKeyPrefix, ResultPrefix: testResultPrefix, UploadTimeout: time.Minute}, log)
	require.NoError(t, err)

	cfg := &config.Editor{
		Stream: redistest.Prefix(t, client) + "editor:jobs", JobTimeout: editWait, PollInterval: time.Second,
		QueueTimeout: time.Hour, WorkerWait: time.Minute,
	}

	board, err := editjob.NewBoard(client, cfg, log)
	require.NoError(t, err)

	clip := filepath.Join(t.TempDir(), "source.mkv")
	makeSpeechClip(t, clip, verticalSize, 30, false)

	f, err := os.Open(clip)
	require.NoError(t, err)

	t.Cleanup(func() { _ = f.Close() })

	source, err := files.Save(t.Context(), &media.File{
		ChatID: clipChat, MessageID: clipMsgID, FileUniqueID: "uniq", MIMEType: matroskaType,
	}, f)
	require.NoError(t, err)

	env := &editEnv{
		files: files, transcriber: &countingTranscriber{}, decider: &scriptedDecider{}, source: source,
	}
	env.executor = editor.NewExecutor(board, files, env.decider, noTech{}, cfg, log)

	worker := NewWorker(board, realPipelineWith(t, files, env.transcriber), &config.MediaWorker{WorkDir: t.TempDir()}, log)
	require.NoError(t, worker.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, worker.Stop(context.Background())) })

	return env
}

func (env *editEnv) data(taskID int64, caption string) *tasks.ExecuteData {
	return &tasks.ExecuteData{
		TaskID: taskID, Agent: editor.AgentName, Kind: "reels-edit", Input: caption, Media: []string{env.source},
		Version: 1,
	}
}

// run runs the task like the task workers do until it is no longer deferred.
func (env *editEnv) run(t *testing.T, data *tasks.ExecuteData) tasks.Output {
	t.Helper()

	deadline := time.Now().Add(editWait)

	for {
		out, err := env.executor.Execute(t.Context(), data)

		var deferred *tasks.DeferredError
		if !errors.As(err, &deferred) {
			require.NoError(t, err)

			return out
		}

		require.True(t, time.Now().Before(deadline), "the edit did not finish in %s", editWait)
		time.Sleep(editPoll)
	}
}

func (env *editEnv) read(t *testing.T, key string) []byte {
	t.Helper()

	data, err := env.files.Read(t.Context(), key, maxReel)
	require.NoError(t, err)

	return data
}

// reelDuration probes the duration of a stored reel.
func (env *editEnv) reelDuration(t *testing.T, key string) float64 {
	t.Helper()

	path := filepath.Join(t.TempDir(), "reel.mp4")
	require.NoError(t, os.WriteFile(path, env.read(t, key), 0o600))

	out := run(t, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path)

	duration, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	require.NoError(t, err)

	return duration
}

// TestIntegrationRevisionWithoutTranscription: the first version is transcribed once and rendered at once (no caption,
// no disputed repeats: no model call). The trainer's revision makes version 2 from the stored transcript — the speech
// recognition is not called again — with the editor's edit, under the keys of version 2; the reel of version 1 stays
// as it was.
func TestIntegrationRevisionWithoutTranscription(t *testing.T) {
	t.Parallel()

	env := startEditEnv(t)
	first := env.data(41, "")

	v1 := env.run(t, first)
	assert.Equal(t, "edits/-100123/42/uniq-v1-reel.mp4", v1.MediaKey)
	assert.Equal(t, int32(1), env.transcriber.calls.Load())
	assert.Empty(t, env.decider.Requests(), "nothing to decide in the first version")

	v1Reel := env.read(t, v1.MediaKey)
	v1Duration := env.reelDuration(t, v1.MediaKey)

	revision := *first
	revision.Version, revision.Revision = 2, "убери «привет» в начале"

	v2 := env.run(t, &revision)
	assert.Equal(t, "edits/-100123/42/uniq-v2-reel.mp4", v2.MediaKey, "a new key for the new version")
	assert.Equal(t, int32(1), env.transcriber.calls.Load(), "the revision is not transcribed again")
	assert.Contains(t, v2.Text, "Новая версия рилс")
	assert.Contains(t, v2.Text, "Вырезал фрагмент «"+cutHello+"».")

	requests := env.decider.Requests()
	require.Len(t, requests, 1)
	assert.Equal(t, "убери «привет» в начале", requests[0].Revision)
	assert.Len(t, requests[0].Transcript.Words, len(clipWords), "the stored transcript of the source")
	assert.InDelta(t, v1Duration, requests[0].Plan.Duration, 0.1, "the plan of version 1")

	assert.Equal(t, v1Reel, env.read(t, v1.MediaKey), "version 1 is not overwritten")
	assert.Less(t, env.reelDuration(t, v2.MediaKey), v1Duration-0.3, "the first word is cut")

	files, err := editjob.Files(env.files, env.source, 2)
	require.NoError(t, err)
	assert.Contains(t, string(env.read(t, files.Plan)), `"edit"`, "the plan of version 2 keeps the edit")
	assert.Contains(t, string(env.read(t, files.Decisions)), "первое слово убрано", "the decision in the log")
}

// TestIntegrationCaptionDecidedBeforeRender: with the trainer's caption the worker stops after the plan, the editor
// decides, the job is replaced by its render, and the worker renders it from the stored transcript: one transcription,
// one draft with the caption applied.
func TestIntegrationCaptionDecidedBeforeRender(t *testing.T) {
	t.Parallel()

	env := startEditEnv(t)

	v1 := env.run(t, env.data(42, "без «привет» в начале"))
	assert.Equal(t, "edits/-100123/42/uniq-v1-reel.mp4", v1.MediaKey)
	assert.Equal(t, int32(1), env.transcriber.calls.Load(), "the render reads the stored transcript")
	assert.Contains(t, v1.Text, "Вырезал фрагмент «"+cutHello+"».")
	assert.Contains(t, v1.Text, "Ваша подпись к исходнику")

	requests := env.decider.Requests()
	require.Len(t, requests, 1)
	assert.Equal(t, "без «привет» в начале", requests[0].Caption)
	assert.Equal(t, 1, requests[0].Version)

	files, err := editjob.Files(env.files, env.source, 1)
	require.NoError(t, err)
	assert.Contains(t, string(env.read(t, files.Plan)), `"edit"`, "the reel is rendered with the decision")
}
