package mediaworker

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
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
	"github.com/Prokud1n/ai-agents/internal/transcription"
)

const (
	// The synthetic talking head with the preparation for the shooting: a phrase of three words every 2.5 s.
	cleanupStart    = 1.0
	cleanupStep     = 2.5
	cleanupWordLen  = 0.5
	cleanupWordGap  = 0.1
	cleanupDuration = 17
	// The model's answers: the cleaning cuts the countdown and the slate (words 0–5); the caption «добавь музыку» is
	// declined.
	cleanupAnswer = `{"decision": "edit", "reason": "отсчёт и слова про дубль", "remove_words": [{"from": 0, "to": 5}]}`
	musicAnswer   = `{"decision": "cannot", "reason": "музыка", "declined": ["music"]}`
	musicCaption  = "добавь музыку"
	musicText     = "Музыку добавьте в Instagram при публикации — я её не накладываю."
)

// cleanupPhrases: a countdown and a slate before the content.
var cleanupPhrases = [][]string{
	{"раз", "два", "три"}, {"вот", "последний", "дубль"}, {"планка", "укрепляет", "спину"},
	{"держите", "локти", "ровно"}, {"дышите", "очень", "спокойно"}, {"сохраняйте", "наше", "видео"},
}

// cleanupTranscript is the transcript of the synthetic talking head: a segment per phrase.
func cleanupTranscript() *transcription.Result {
	result := &transcription.Result{Language: testLanguage, Duration: cleanupDuration}

	for p, phrase := range cleanupPhrases {
		start := cleanupStart + float64(p)*cleanupStep
		words := make([]transcription.Word, 0, len(phrase))

		for i, text := range phrase {
			at := start + float64(i)*(cleanupWordLen+cleanupWordGap)
			words = append(words, transcription.Word{Word: text, Start: at, End: at + cleanupWordLen})
		}

		text := strings.Join(phrase, " ") + "."
		result.Words = append(result.Words, words...)
		result.Segments = append(result.Segments, transcription.Segment{
			Start: words[0].Start, End: words[len(words)-1].End, Text: " " + text,
		})
		result.Text += text + " "
	}

	return result
}

// cleanupModel is the editor's model behind the real decider: it cleans a first version without a caption and declines
// the caption that asks for music.
type cleanupModel struct {
	mu      sync.Mutex
	prompts []string
}

func (m *cleanupModel) Run(ctx context.Context, data *agents.RunAgentData) (*agents.Result, error) {
	m.mu.Lock()
	m.prompts = append(m.prompts, data.Input)
	m.mu.Unlock()

	input := cleanupAnswer
	if strings.Contains(data.Input, "<trainer_caption>") {
		input = musicAnswer
	}

	answer, err := editplan.NewSubmitTool().Execute(ctx, json.RawMessage(input))
	if err != nil {
		return nil, err
	}

	return &agents.Result{Text: answer}, nil
}

func (m *cleanupModel) Prompts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]string(nil), m.prompts...)
}

// startCleanupEnv is the editor of the app with the hook title on (the default: the worker stops every first version
// for the model) and the real media worker, through a real Redis and S3, the source being the synthetic talking head
// with the preparation for the shooting.
func startCleanupEnv(t *testing.T, model *cleanupModel) *editEnv {
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

	words := cleanupTranscript().Words
	spans := make([]roughcut.Interval, 0, len(words))

	for _, w := range words {
		spans = append(spans, roughcut.Interval{Start: w.Start, End: w.End})
	}

	clip := filepath.Join(t.TempDir(), "cleanup.mkv")
	makeToneClip(t, clip, cleanupDuration, spans)

	f, err := os.Open(clip)
	require.NoError(t, err)

	t.Cleanup(func() { _ = f.Close() })

	source, err := files.Save(t.Context(), &media.File{
		ChatID: clipChat, MessageID: clipMsgID, FileUniqueID: "cleanup", MIMEType: matroskaType,
	}, f)
	require.NoError(t, err)

	env := &editEnv{
		files: files, source: source,
		executor: editor.NewExecutor(board, files, editplan.NewDecider(model, log), noTech{}, cfg, log),
	}

	speech := &fakeTranscriber{result: cleanupTranscript()}
	worker := NewWorker(board, realPipelineWith(t, files, speech), &config.MediaWorker{WorkDir: t.TempDir()}, log)
	require.NoError(t, worker.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, worker.Stop(context.Background())) })

	return env
}

// TestIntegrationCleanupFirstVersion: a talking head without a caption is given to the model with the hints about the
// shooting; the model (through the real decider and its checks) cuts the countdown and the slate, the reel is shorter
// by them, and the description says the failed takes and the preparation were removed, in one line.
func TestIntegrationCleanupFirstVersion(t *testing.T) {
	t.Parallel()

	model := &cleanupModel{}
	env := startCleanupEnv(t, model)

	v1 := env.run(t, env.data(46, ""))

	prompts := model.Prompts()
	require.Len(t, prompts, 1)
	assert.Contains(t, prompts[0], "почисти ролик (раздел «Чистка дублей и подготовки»)")
	assert.Contains(t, prompts[0], "Похоже на слова про съёмку")
	assert.Contains(t, prompts[0], "«раз два три»")
	assert.Contains(t, prompts[0], "«вот последний дубль»")

	assert.Contains(t, v1.Text, "(паузы, оговорки и неудачные дубли)")
	assert.Contains(t, v1.Text, editplan.CleanupLinePrefix+": 1 фрагмент (")
	assert.NotContains(t, v1.Text, "Вырезал фрагмент", "the cleaning is one line")

	// Four phrases of 1.7 s with their margins are left of six.
	assert.InDelta(t, 4*(3*cleanupWordLen+2*cleanupWordGap+0.2), env.reelDuration(t, v1.MediaKey), 0.6)

	files, err := editjob.Files(env.files, env.source, 1)
	require.NoError(t, err)

	decisions := string(env.read(t, files.Decisions))
	assert.Contains(t, decisions, "Чистка первой версии: отсчёт и слова про дубль")
	assert.Contains(t, decisions, "Чистка: вырезаны слова 0–5 «раз два три вот последний дубль»")
}

// TestIntegrationCaptionDeclined: the caption «добавь музыку» is declined by its kind: the automatic cut is rendered
// and the description gives the prepared text about the music, not the model's words.
func TestIntegrationCaptionDeclined(t *testing.T) {
	t.Parallel()

	model := &cleanupModel{}
	env := startCleanupEnv(t, model)

	v1 := env.run(t, env.data(47, musicCaption))

	require.Len(t, model.Prompts(), 1)
	assert.Contains(t, model.Prompts()[0], "<trainer_caption>\n"+musicCaption+"\n</trainer_caption>")
	assert.Contains(t, v1.Text, "Подпись как указание к монтажу не применил. "+musicText)
	assert.NotContains(t, v1.Text, "музыка\n", "the model's reason is not shown")
	assert.Contains(t, v1.Text, "Ваша подпись к исходнику: «"+musicCaption+"»")
}
