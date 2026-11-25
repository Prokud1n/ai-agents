package mediaworker

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	// talkPhrases of the synthetic talking head: two words each, 2.5 s apart; a phrase is 1.7 s of speech, 1.9 s in the
	// reel with its margins, so the cleaned cut is about 15 s of a 22 s source.
	talkPhrases    = 8
	talkStart      = 1.0
	talkStep       = 2.5
	talkWordLen    = 0.8
	talkWordGap    = 0.1
	talkDuration   = 22
	talkPhraseReel = 1.9
	// talkTarget: the recommended reel is 5–10 s, so the 15 s are shortened.
	talkTarget = 10 * time.Second
	// talkTolerance of a reel's length (editplan: ±15 %) plus the frame grid.
	talkTolerance = 0.15
)

// talkWords are the words of the phrases, all different, so no failed take is found.
var talkWords = [talkPhrases][2]string{
	{"планка", "укрепляет"}, {"держите", "локти"}, {"прямая", "линия"}, {"не", "прогибайтесь"},
	{"дышите", "спокойно"}, {"начните", "медленно"}, {"добавляйте", "время"}, {"сохраняйте", "видео"},
}

// talkTranscript is the transcript of the synthetic talking head: a segment per phrase.
func talkTranscript() *transcription.Result {
	result := &transcription.Result{Language: testLanguage, Duration: talkDuration}

	for p := range talkPhrases {
		start := talkStart + float64(p)*talkStep
		first := transcription.Word{Word: talkWords[p][0], Start: start, End: start + talkWordLen}
		second := transcription.Word{
			Word: talkWords[p][1], Start: first.End + talkWordGap, End: first.End + talkWordGap + talkWordLen,
		}

		result.Words = append(result.Words, first, second)
		result.Segments = append(result.Segments, transcription.Segment{
			Start: first.Start, End: second.End, Text: " " + first.Word + " " + second.Word + ".",
		})
		result.Text += first.Word + " " + second.Word + ". "
	}

	return result
}

// talkTranscriber is the speech recognition of the talking head that counts its calls.
type talkTranscriber struct {
	calls atomic.Int32
}

func (c *talkTranscriber) Transcribe(context.Context, transcription.TranscribeData) (*transcription.Result, error) {
	c.calls.Add(1)

	return talkTranscript(), nil
}

// selectingModel is the editor's model behind the real editplan.Decider: it answers submit_edit through the real
// tool, so the decision is checked by editplan.Apply. The first version keeps phrases 0 and 4–7 (about 9.5 s); the
// revision «сделай 6 секунд» also cuts phrases 4 and 5 (about 5.7 s).
type selectingModel struct {
	mu      sync.Mutex
	prompts []string
}

const (
	firstSelection  = `{"decision": "edit", "reason": "ролик про планку: хук и финал оставлены", "remove_words": [{"from": 2, "to": 7}]}`
	revisionSixSecs = `{"decision": "edit", "reason": "сделай 6 секунд", "target_seconds": 6, "remove_words": [{"from": 8, "to": 11}]}`
	sixSeconds      = 6.0
)

func (m *selectingModel) Run(ctx context.Context, data *agents.RunAgentData) (*agents.Result, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, data.Input)
	m.mu.Unlock()

	input := firstSelection
	if strings.Contains(data.Input, "<trainer_revision>") {
		input = revisionSixSecs
	}

	answer, err := editplan.NewSubmitTool().Execute(ctx, json.RawMessage(input))
	if err != nil {
		return nil, err
	}

	return &agents.Result{Text: answer}, nil
}

func (m *selectingModel) Prompts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]string(nil), m.prompts...)
}

// startSelectionEnv connects the editor of the app with the real decider (its model scripted) and the media worker
// with the real pipeline through a real Redis and S3, the source being the synthetic talking head, the target 10 s.
func startSelectionEnv(t *testing.T) (*editEnv, *talkTranscriber, *selectingModel) {
	t.Helper()

	requireIntegrationFFmpeg(t)

	log := slog.New(slog.DiscardHandler)
	client := redistest.Connect(t)

	files, err := media.NewStore(s3test.Connect(t),
		&config.Media{KeyPrefix: "incoming", ResultPrefix: "edits", UploadTimeout: time.Minute}, log)
	require.NoError(t, err)

	cfg := &config.Editor{
		Stream: redistest.Prefix(t, client) + "editor:jobs", JobTimeout: editWait, PollInterval: time.Second,
		QueueTimeout: time.Hour, WorkerWait: time.Minute, ReelsTargetDuration: talkTarget,
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
		ChatID: clipChat, MessageID: clipMsgID, FileUniqueID: "talk", MIMEType: "video/x-matroska",
	}, f)
	require.NoError(t, err)

	speech, model := &talkTranscriber{}, &selectingModel{}
	env := &editEnv{
		files: files, source: source,
		executor: editor.NewExecutor(board, files, editplan.NewDecider(model, log), noTech{}, cfg, log),
	}

	worker := NewWorker(board, realPipelineWith(t, files, speech), &config.MediaWorker{WorkDir: t.TempDir()}, log)
	require.NoError(t, worker.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, worker.Stop(context.Background())) })

	return env, speech, model
}

// TestIntegrationSelectionForLongTalk: a talking head of 15 s after the cleaning, no caption, no disputed repeats,
// with a target of 10 s: the worker stops after the plan, the model (through the real decider and its checks) keeps the
// best phrases, and the render is within the recommended 5–10 s; the speech is transcribed once. The revision
// «сделай 6 секунд» makes version 2 of about 6 s from the stored transcript, without the speech recognition.
func TestIntegrationSelectionForLongTalk(t *testing.T) {
	t.Parallel()

	env, speech, model := startSelectionEnv(t)
	first := env.data(43, "")

	v1 := env.run(t, first)
	assert.Equal(t, int32(1), speech.calls.Load(), "the render reads the stored transcript")

	prompts := model.Prompts()
	require.Len(t, prompts, 1, "the long talking head is given to the model without a caption")
	assert.Contains(t, prompts[0], "Сделай рилс 5–10 с")
	assert.Contains(t, prompts[0], "0–1 | 0:01.0–0:02.7 | в ролике")

	v1Duration := env.reelDuration(t, v1.MediaKey)
	assert.InDelta(t, 5*talkPhraseReel, v1Duration, 0.5, "five phrases of eight")
	assert.LessOrEqual(t, v1Duration, talkTarget.Seconds()*(1+talkTolerance))
	assert.Contains(t, v1.Text, "сократил под рилс")
	assert.Contains(t, v1.Text, "Не вошло: «держите локти прямая линия")

	files1, err := editjob.Files(env.files, env.source, 1)
	require.NoError(t, err)

	decisions := string(env.read(t, files1.Decisions))
	assert.Contains(t, decisions, "Отбор под рилс: ролик про планку")
	assert.Contains(t, decisions, "Не вошло в рилс: слова 2–7")
	assert.NotContains(t, decisions, "Решения монтажёра: 0")

	revision := *first
	revision.Version, revision.Revision = 2, "сделай 6 секунд"

	v2 := env.run(t, &revision)
	assert.Equal(t, int32(1), speech.calls.Load(), "the revision is not transcribed again")
	assert.Len(t, model.Prompts(), 2)
	assert.Contains(t, v2.Text, "Новая версия рилс")
	assert.Contains(t, v2.Text, "Не вошло: «дышите спокойно начните медленно».")

	v2Duration := env.reelDuration(t, v2.MediaKey)
	assert.InDelta(t, 3*talkPhraseReel, v2Duration, 0.5, "three phrases of eight")
	assert.InDelta(t, sixSeconds, v2Duration, sixSeconds*talkTolerance+0.2, "within the length asked for")
}
