package mediaworker

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/agents"
	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/editjob"
	"github.com/Prokud1n/ai-agents/internal/editor"
	"github.com/Prokud1n/ai-agents/internal/editplan"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/redistest"
	"github.com/Prokud1n/ai-agents/internal/roughcut"
	"github.com/Prokud1n/ai-agents/internal/s3test"
	"github.com/Prokud1n/ai-agents/internal/subtitles"
)

const (
	// matroskaType of the synthetic sources saved to S3.
	matroskaType = "video/x-matroska"
	hookText     = "Планка без ошибок"
	proposeHook  = `{"decision": "edit", "reason": "ролик про планку", "hook_title": "` + hookText + `"}`
	removeHook   = `{"decision": "edit", "reason": "убери заголовок", "hook_title": ""}`
	hookRevision = "убери заголовок"
	// Frames compared: with the hook (and the subtitles under it), and long after it, when both versions are the same.
	withHookAt  = 1.0
	afterHookAt = 5.0
	// hookDiff: the mean difference of the hook's band with the hook versus without it, in gray levels; sameDiff: the
	// same band of the two versions after the hook (only encoding noise).
	hookDiff = 5.0
	sameDiff = 4.0
	// hookBandLines: two lines of the hook with the line spacing, in font sizes.
	hookBandLines = 2.5
)

// hookModel is the editor's model behind the real decider: it proposes the hook title of the first version and removes
// it by the revision.
type hookModel struct {
	mu      sync.Mutex
	prompts []string
}

func (m *hookModel) Run(ctx context.Context, data *agents.RunAgentData) (*agents.Result, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, data.Input)
	m.mu.Unlock()

	input := proposeHook
	if strings.Contains(data.Input, "<trainer_revision>") {
		input = removeHook
	}

	answer, err := editplan.NewSubmitTool().Execute(ctx, json.RawMessage(input))
	if err != nil {
		return nil, err
	}

	return &agents.Result{Text: answer}, nil
}

func (m *hookModel) Prompts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]string(nil), m.prompts...)
}

// startHookEnv is the editor of the app with the hook title on (no selection: the talking head is short enough) and
// the real media worker, through a real Redis and S3, the source being the synthetic talking head.
func startHookEnv(t *testing.T) (*editEnv, *talkTranscriber, *hookModel) {
	t.Helper()

	requireIntegrationFFmpeg(t)

	log := slog.New(slog.DiscardHandler)
	client := redistest.Connect(t)

	files, err := media.NewStore(s3test.Connect(t),
		&config.Media{KeyPrefix: testKeyPrefix, ResultPrefix: testResultPrefix, UploadTimeout: time.Minute}, log)
	require.NoError(t, err)

	cfg := &config.Editor{
		Stream: redistest.Prefix(t, client) + "editor:jobs", JobTimeout: editWait, PollInterval: time.Second,
		QueueTimeout: time.Hour, WorkerWait: time.Minute, HookTitle: true,
	}

	board, err := editjob.NewBoard(client, cfg, log)
	require.NoError(t, err)

	words := talkTranscript().Words
	spans := make([]roughcut.Interval, 0, len(words))

	for _, w := range words {
		spans = append(spans, roughcut.Interval{Start: w.Start, End: w.End})
	}

	clip := filepath.Join(t.TempDir(), "talk.mkv")
	makeToneClip(t, clip, talkDuration, spans)

	f, err := os.Open(clip)
	require.NoError(t, err)

	t.Cleanup(func() { _ = f.Close() })

	source, err := files.Save(t.Context(), &media.File{
		ChatID: clipChat, MessageID: clipMsgID, FileUniqueID: "hook", MIMEType: matroskaType,
	}, f)
	require.NoError(t, err)

	speech, model := &talkTranscriber{}, &hookModel{}
	env := &editEnv{
		files: files, source: source,
		executor: editor.NewExecutor(board, files, editplan.NewDecider(model, log), noTech{}, cfg, log),
	}

	worker := NewWorker(board, realPipelineWith(t, files, speech), &config.MediaWorker{WorkDir: t.TempDir()}, log)
	require.NoError(t, worker.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, worker.Stop(context.Background())) })

	return env, speech, model
}

// grayFrame decodes the frame of the stored reel at the second as 8-bit gray pixels, row by row.
func (env *editEnv) grayFrame(t *testing.T, key string, at float64) []byte {
	t.Helper()

	return reelFrame(t, env.read(t, key), at)
}

// reelFrame decodes the frame of the reel at the second as 8-bit gray pixels, row by row.
func reelFrame(t *testing.T, reel []byte, at float64) []byte {
	t.Helper()

	path := filepath.Join(t.TempDir(), "reel.mp4")
	require.NoError(t, os.WriteFile(path, reel, 0o600))

	frame := run(t, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "error", "-ss",
		strconv.FormatFloat(at, 'f', 2, 64), "-i", path, "-frames:v", "1", "-f", "rawvideo", "-pix_fmt", "gray", "-")
	require.Len(t, frame, subtitles.FrameWidth*subtitles.FrameHeight)

	return frame
}

// meanDiff is the mean absolute difference of the rows [from, to) of two frames.
func meanDiff(a, b []byte, from, to int) float64 {
	total := 0

	for i := from * subtitles.FrameWidth; i < to*subtitles.FrameWidth; i++ {
		total += max(int(a[i])-int(b[i]), int(b[i])-int(a[i]))
	}

	return float64(total) / float64((to-from)*subtitles.FrameWidth)
}

// TestIntegrationHookTitle: a short talking head without a caption or disputed repeats stops after the plan only for
// the hook title; the model proposes it (through the real decider and its checks), libass draws it over the first
// seconds with the subtitles under it, the description and the decisions log name it. The revision «убери заголовок»
// makes version 2 without it from the stored transcript — the speech recognition is not called again — and the reels
// differ only while the hook was shown.
func TestIntegrationHookTitle(t *testing.T) {
	t.Parallel()

	env, speech, model := startHookEnv(t)
	first := env.data(44, "")

	v1 := env.run(t, first)
	assert.Equal(t, int32(1), speech.calls.Load(), "the render reads the stored transcript")

	prompts := model.Prompts()
	require.Len(t, prompts, 1, "the short talking head is given to the model for the hook")
	assert.Contains(t, prompts[0], "предложи заголовок-хук")
	assert.Contains(t, v1.Text, "Заголовок: «"+hookText+"».")

	files1, err := editjob.Files(env.files, env.source, 1)
	require.NoError(t, err)
	assert.Contains(t, string(env.read(t, files1.Decisions)), "Заголовок-хук: «"+hookText+"»")

	revision := *first
	revision.Version, revision.Revision = 2, hookRevision

	v2 := env.run(t, &revision)
	assert.Equal(t, int32(1), speech.calls.Load(), "the revision is not transcribed again")
	require.Len(t, model.Prompts(), 2)
	assert.Contains(t, model.Prompts()[1], "Заголовок-хук сейчас: «"+hookText+"»")
	assert.Contains(t, v2.Text, "Убрал заголовок.")

	files2, err := editjob.Files(env.files, env.source, 2)
	require.NoError(t, err)
	assert.NotContains(t, string(env.read(t, files2.Decisions)), "Заголовок-хук")
	assert.InDelta(t, env.reelDuration(t, v1.MediaKey), env.reelDuration(t, v2.MediaKey), 0.05, "the cut is the same")

	checkHookDrawn(t, env.read(t, v1.MediaKey), env.read(t, v2.MediaKey))
}

// checkHookDrawn compares the reels with and without the hook in the band of the hook (two lines of its size from the
// subtitles' margin; without the hook only a subtitle line is there): different while the hook is shown, the same
// after it.
func checkHookDrawn(t *testing.T, hooked, plain []byte) {
	t.Helper()

	style := subtitles.DefaultStyle()
	from := style.Layout.MarginTop
	to := from + int(hookBandLines*float64(style.Hook.Size))

	assert.Greater(t, meanDiff(reelFrame(t, hooked, withHookAt), reelFrame(t, plain, withHookAt), from, to), hookDiff,
		"the hook is drawn in the reel with it only")
	assert.Less(t, meanDiff(reelFrame(t, hooked, afterHookAt), reelFrame(t, plain, afterHookAt), from, to), sameDiff,
		"after the hook the reels are the same")
}

// TestIntegrationHookRender: the media worker alone (no Redis or S3, the ffmpeg, libass and fonts of the host or of the
// media image) renders a decided version with the hook title and one without it from the stored transcript: the plan
// keeps the hook, libass draws it over the first seconds and nothing of it is left after them.
func TestIntegrationHookRender(t *testing.T) {
	t.Parallel()
	requireIntegrationFFmpeg(t)

	words := talkTranscript().Words
	spans := make([]roughcut.Interval, 0, len(words))

	for _, w := range words {
		spans = append(spans, roughcut.Interval{Start: w.Start, End: w.End})
	}

	source := filepath.Join(t.TempDir(), "talk.mkv")
	makeToneClip(t, source, talkDuration, spans)

	store := newLocalStore(map[string]string{testSourceKey: source})
	speech := &fakeTranscriber{result: talkTranscript()}

	first, err := editjob.Files(store, testSourceKey, 1)
	require.NoError(t, err)

	stored, err := json.Marshal(talkTranscript())
	require.NoError(t, err)
	require.NoError(t, store.PutResult(t.Context(), first.Transcript, bytes.NewReader(stored), editjob.JSONContentType))

	p := realPipelineWith(t, store, speech)
	render := func(version int, hook string) (reel, plan []byte) {
		job := testJob()
		job.ID, job.Version, job.Stage = editjob.JobID(job.TaskID, version), version, editjob.RenderStage
		job.Settings = &roughcut.Settings{MaxPause: 0.4, PadBefore: 0.08, PadAfter: 0.12, RetakeSimilarity: 0.7}
		job.Edit = &roughcut.Edit{Hook: hook}

		result, err := p.Run(t.Context(), job)
		require.NoError(t, err)
		require.Equal(t, editjob.DoneStatus, result.Status)

		files, err := editjob.Files(store, testSourceKey, version)
		require.NoError(t, err)

		return store.results[files.Reel].data, store.results[files.Plan].data
	}

	hooked, plan := render(1, hookText)
	plain, _ := render(2, "")

	assert.Zero(t, speech.calls, "a render reads the stored transcript")
	assert.Contains(t, string(plan), `"hook":"`+hookText+`"`)
	checkHookDrawn(t, hooked, plain)
}
