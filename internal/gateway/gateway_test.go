package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx/fxtest"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/telegram"
)

const (
	eventTimeout  = 5 * time.Second
	sourceCaption = "исходник"
)

var errHandlerFailed = errors.New("handler failed")

type recordingHandler struct {
	events chan *Event
	// panicOn makes the handler panic on the message with this ID, failOn return an error.
	panicOn int64
	failOn  int64
}

func newRecordingHandler() *recordingHandler {
	return &recordingHandler{events: make(chan *Event, 10)}
}

func (h *recordingHandler) HandleEvent(_ context.Context, event *Event) error {
	if h.panicOn != 0 && event.MessageID == h.panicOn {
		panic("handler bug reading /var/lib/telegram-bot-api/" + marketerAuth + "/videos/file_0.mp4")
	}

	if h.failOn != 0 && event.MessageID == h.failOn {
		h.events <- event

		return errHandlerFailed
	}

	h.events <- event

	return nil
}

func (h *recordingHandler) next(t *testing.T) *Event {
	t.Helper()

	select {
	case event := <-h.events:
		return event
	case <-time.After(eventTimeout):
		require.FailNow(t, "no event")

		return nil
	}
}

func startGateway(
	t *testing.T,
	cfg *config.Telegram,
	store stateStore,
	handler Handler,
	log *slog.Logger,
) (*fxtest.Lifecycle, *Status) {
	t.Helper()

	lc := fxtest.NewLifecycle(t)
	status := NewStatus()
	newGateway(lc, cfg, testBots(t, cfg), store, handler, status, log)
	lc.RequireStart()

	return lc, status
}

func sourcesMessage(updateID, messageID int64, text string) telegram.Update {
	update := groupMessage(text, inTopic(sourcesTopic))
	update.UpdateID = updateID
	update.Message.MessageID = messageID

	return *update
}

func TestGatewayEndToEnd(t *testing.T) {
	t.Parallel()

	api, baseURL := newFakeBotAPI(t)

	filesDir := t.TempDir()
	videoPath := filepath.Join(filesDir, marketerAuth, "videos", "file_0.mp4")
	require.NoError(t, os.MkdirAll(filepath.Dir(videoPath), 0o750))
	require.NoError(t, os.WriteFile(videoPath, []byte("0123456789"), 0o600))
	api.addFile(telegram.File{FileID: "vid", FileSize: 10, FilePath: videoPath})

	stranger := sourcesMessage(2, 201, "чужой")
	stranger.Message.From = &telegram.User{ID: strangerID}

	video := sourcesMessage(3, 202, "")
	video.Message.Caption = sourceCaption
	video.Message.Video = &telegram.Video{FileID: "vid", Duration: 95, MIMEType: "video/mp4", FileSize: 10}

	api.queue(marketerAuth,
		sourcesMessage(1, 200, "Смонтируй рилс"),
		stranger,
		video,
		sourcesMessage(4, 200, "Смонтируй рилс"), // the same message again: skipped
	)
	api.queue(editorAuth, telegram.Update{UpdateID: 10, CallbackQuery: &telegram.CallbackQuery{
		ID: "cb-1", From: telegram.User{ID: testUserID}, Data: "approve:1",
		Message: &telegram.Message{MessageID: 300, Chat: telegram.Chat{ID: testGroupID, Type: supergroupType}},
	}})

	store := newMemoryStore()
	handler := newRecordingHandler()

	var logs bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	lc, _ := startGateway(t, testConfig(baseURL, filesDir), store, handler, log)

	events := []*Event{handler.next(t), handler.next(t), handler.next(t)}
	sort.Slice(events, func(i, j int) bool { return events[i].UpdateID < events[j].UpdateID })

	assert.Eventually(t, func() bool { return store.offset(1) == 5 && store.offset(3) == 11 }, eventTimeout, 10*time.Millisecond)

	lc.RequireStop()

	text, media, callback := events[0], events[1], events[2]

	assert.Equal(t, MessageEventType, text.Type)
	assert.Equal(t, EditorRole, text.Target)
	assert.Equal(t, MarketerRole, text.ReceivedBy)
	assert.Equal(t, "Смонтируй рилс", text.Text)

	require.NotNil(t, media.Media)
	require.True(t, media.Media.Downloaded())
	assert.Equal(t, int64(10), media.Media.Size)
	assert.Equal(t, "0123456789", readMedia(t, media.Media))
	assert.Equal(t, 95*time.Second, media.Media.Duration)
	assert.Equal(t, sourceCaption, media.Text)

	assert.Equal(t, CallbackEventType, callback.Type)
	assert.Equal(t, EditorRole, callback.ReceivedBy)
	assert.Equal(t, "approve:1", callback.Callback.Data)

	select {
	case extra := <-handler.events:
		assert.Fail(t, "unexpected event", "update %d", extra.UpdateID)
	default:
	}

	for _, call := range api.callsOf("getUpdates") {
		var params telegram.GetUpdatesParams
		require.NoError(t, json.Unmarshal(call.body, &params))

		expected := []string{telegram.CallbackQueryUpdate}
		if call.token == marketerAuth {
			expected = []string{telegram.MessageUpdate, telegram.CallbackQueryUpdate}
		}

		assert.Equal(t, expected, params.AllowedUpdates, "bot %s", call.token[:1])
	}

	assert.NotContains(t, logs.String(), "SECRET", "tokens and file paths must not be logged")
	assert.NotContains(t, logs.String(), "Смонтируй", "message texts must not be logged")
}

func TestGatewayResumesFromStoredOffset(t *testing.T) {
	t.Parallel()

	api, baseURL := newFakeBotAPI(t)
	store := newMemoryStore()
	require.NoError(t, store.SaveOffset(t.Context(), 1, 42))

	lc, _ := startGateway(t, testConfig(baseURL, t.TempDir()), store, newRecordingHandler(), slog.New(slog.DiscardHandler))

	assert.Eventually(t, func() bool { return len(api.callsOf("getUpdates")) >= len(Roles) }, eventTimeout, 10*time.Millisecond)
	lc.RequireStop()

	var params telegram.GetUpdatesParams

	for _, call := range api.callsOf("getUpdates") {
		if call.token == marketerAuth {
			require.NoError(t, json.Unmarshal(call.body, &params))

			break
		}
	}

	assert.Equal(t, int64(42), params.Offset)
}

func TestGatewaySurvivesHandlerPanic(t *testing.T) {
	t.Parallel()

	api, baseURL := newFakeBotAPI(t)
	api.queue(marketerAuth, sourcesMessage(1, 500, "первое"), sourcesMessage(2, 501, "второе"))

	handler := newRecordingHandler()
	handler.panicOn = 500

	var logs bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&logs, nil))
	lc, _ := startGateway(t, testConfig(baseURL, t.TempDir()), newMemoryStore(), handler, log)

	event := handler.next(t)

	lc.RequireStop()

	assert.Equal(t, int64(501), event.MessageID)
	assert.Contains(t, logs.String(), "handler panicked")
	assert.NotContains(t, logs.String(), "SECRET", "the panic text is sanitized")
}

func TestGatewayDisabled(t *testing.T) {
	t.Parallel()

	api, baseURL := newFakeBotAPI(t)
	cfg := testConfig(baseURL, t.TempDir())
	cfg.MarketerToken, cfg.CopywriterToken, cfg.EditorToken, cfg.DesignerToken = "", "", "", ""

	lc, _ := startGateway(t, cfg, newMemoryStore(), newRecordingHandler(), slog.New(slog.DiscardHandler))
	lc.RequireStop()

	assert.Empty(t, api.callsOf("getMe"))

	_, err := NewSender(testBots(t, cfg), cfg).SendText(t.Context(), MarketerRole, &OutgoingMessage{Text: "проверка"})
	require.ErrorIs(t, err, ErrDisabled)
}

func TestGatewayDownloadFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		file *telegram.File // nil: getFile fails
	}{
		{name: "getFile fails", file: nil},
		{name: "public api relative path", file: &telegram.File{FileID: "f", FilePath: "videos/file_0.mp4"}},
		{name: "path outside the files directory", file: &telegram.File{FileID: "f", FilePath: "/etc/passwd"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, baseURL := newFakeBotAPI(t)
			if tt.file != nil {
				api.addFile(*tt.file)
			}

			msg := sourcesMessage(1, 600, "")
			msg.Message.Voice = &telegram.Voice{FileID: "f", Duration: 3, FileSize: 10}
			api.queue(marketerAuth, msg)

			handler := newRecordingHandler()
			lc, _ := startGateway(t, testConfig(baseURL, t.TempDir()), newMemoryStore(), handler, slog.New(slog.DiscardHandler))

			event := handler.next(t)

			lc.RequireStop()

			require.NotNil(t, event.Media)
			assert.False(t, event.Media.Downloaded())
			assert.False(t, event.Media.TooLarge)
		})
	}
}

// noMediaHandler does not process files: the gateway must not download them.
type noMediaHandler struct {
	*recordingHandler
}

func (noMediaHandler) WantsMedia(*Event) bool {
	return false
}

func TestGatewaySkipsDownloadForHandlerWithoutMedia(t *testing.T) {
	t.Parallel()

	api, baseURL := newFakeBotAPI(t)
	api.addFile(telegram.File{FileID: "f", FileSize: 10, FilePath: "/var/lib/telegram-bot-api/voice/file_0.oga"})

	msg := sourcesMessage(1, 600, "")
	msg.Message.Voice = &telegram.Voice{FileID: "f", Duration: 3, FileSize: 10}
	api.queue(marketerAuth, msg)

	handler := noMediaHandler{recordingHandler: newRecordingHandler()}
	lc, _ := startGateway(t, testConfig(baseURL, t.TempDir()), newMemoryStore(), handler, slog.New(slog.DiscardHandler))

	event := handler.next(t)

	lc.RequireStop()

	require.NotNil(t, event.Media)
	assert.Equal(t, VoiceMediaType, event.Media.Type)
	assert.False(t, event.Media.Downloaded())
	assert.Empty(t, api.callsOf("getFile"))
}

func TestValidateConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(cfg *config.Telegram)
		wantErr bool
	}{
		{name: "valid", mutate: func(*config.Telegram) {}},
		{name: "missing token", mutate: func(cfg *config.Telegram) { cfg.DesignerToken = "" }, wantErr: true},
		{name: "malformed token", mutate: func(cfg *config.Telegram) { cfg.EditorToken = "nonsense" }, wantErr: true},
		{name: "same token twice", mutate: func(cfg *config.Telegram) { cfg.EditorToken = cfg.CopywriterToken }, wantErr: true},
		{name: "no group", mutate: func(cfg *config.Telegram) { cfg.GroupID = 0 }, wantErr: true},
		{name: "empty allowlist", mutate: func(cfg *config.Telegram) { cfg.AllowedUserIDs = nil }, wantErr: true},
		{name: "same topic twice", mutate: func(cfg *config.Telegram) { cfg.TechTopicID = cfg.SourcesTopicID }, wantErr: true},
		{name: "no workers", mutate: func(cfg *config.Telegram) { cfg.Workers = 0 }, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := testConfig("http://telegram-bot-api:8081", "/var/lib/telegram-bot-api")
			tt.mutate(cfg)

			err := validateConfig(cfg)
			if !tt.wantErr {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, ErrInvalidConfig)
			assert.NotContains(t, err.Error(), "SECRET")
		})
	}
}

func TestEventLogValueHidesContent(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&logs, nil))
	event := &Event{
		Type: MessageEventType, Text: "личный текст",
		Media:    &Media{Type: VideoMediaType, dir: "/var/lib/telegram-bot-api", rel: "1:SECRET/videos/a.mp4", Size: 5},
		Callback: &Callback{ID: "cb-log", Data: "approve:1"},
	}

	log.Info("event", "event", event)

	assert.NotContains(t, logs.String(), "личный")
	assert.NotContains(t, logs.String(), "SECRET")
	assert.NotContains(t, logs.String(), "approve")
	assert.Contains(t, logs.String(), `"media_type":"video"`)
}

func readMedia(t *testing.T, media *Media) string {
	t.Helper()

	reader, err := media.Open()
	require.NoError(t, err)

	defer reader.Close()

	data, err := io.ReadAll(reader)
	require.NoError(t, err)

	return string(data)
}

func TestGatewayCallbackDoesNotWaitForDownload(t *testing.T) {
	t.Parallel()

	api, baseURL := newFakeBotAPI(t)
	gate := make(chan struct{})
	api.fileGate = gate

	video := sourcesMessage(1, 700, "")
	video.Message.Video = &telegram.Video{FileID: "slow", FileSize: 10}
	api.queue(marketerAuth, video)
	api.queue(editorAuth, telegram.Update{UpdateID: 20, CallbackQuery: &telegram.CallbackQuery{
		ID: "cb-fast", From: telegram.User{ID: testUserID}, Data: "approve:2",
		Message: &telegram.Message{MessageID: 301, Chat: telegram.Chat{ID: testGroupID, Type: supergroupType}},
	}})

	cfg := testConfig(baseURL, t.TempDir())
	cfg.Workers = 1

	handler := newRecordingHandler()
	lc, _ := startGateway(t, cfg, newMemoryStore(), handler, slog.New(slog.DiscardHandler))

	first := handler.next(t)
	assert.Equal(t, CallbackEventType, first.Type, "the callback is handled while the video is downloading")

	close(gate)

	second := handler.next(t)
	assert.Equal(t, int64(700), second.MessageID)

	lc.RequireStop()
}

func TestGatewayUnmarksFailedEvents(t *testing.T) {
	t.Parallel()

	api, baseURL := newFakeBotAPI(t)
	api.queue(editorAuth, telegram.Update{UpdateID: 30, CallbackQuery: &telegram.CallbackQuery{
		ID: "cb-fail", From: telegram.User{ID: testUserID}, Data: "approve:3",
		Message: &telegram.Message{MessageID: 800, Chat: telegram.Chat{ID: testGroupID, Type: supergroupType}},
	}})

	store := newMemoryStore()
	handler := newRecordingHandler()
	handler.failOn = 800

	lc, _ := startGateway(t, testConfig(baseURL, t.TempDir()), store, handler, slog.New(slog.DiscardHandler))

	handler.next(t)
	assert.Eventually(t, func() bool { return len(api.callsOf("answerCallbackQuery")) == 1 }, eventTimeout, 10*time.Millisecond)
	lc.RequireStop()

	assert.Equal(t, []string{"callback:cb-fail"}, store.unmarkedKeys())

	answer := api.callsOf("answerCallbackQuery")[0]
	assert.Equal(t, editorAuth, answer.token)
	assert.JSONEq(t, `{"callback_query_id":"cb-fail"}`, string(answer.body), "answered without text")
}

func TestGatewayAnswersStrangerCallback(t *testing.T) {
	t.Parallel()

	api, baseURL := newFakeBotAPI(t)
	api.queue(designerAuth, telegram.Update{UpdateID: 40, CallbackQuery: &telegram.CallbackQuery{
		ID: "cb-stranger", From: telegram.User{ID: strangerID}, Data: "approve:4",
		Message: &telegram.Message{MessageID: 900, Chat: telegram.Chat{ID: testGroupID, Type: supergroupType}},
	}})

	handler := newRecordingHandler()
	lc, _ := startGateway(t, testConfig(baseURL, t.TempDir()), newMemoryStore(), handler, slog.New(slog.DiscardHandler))

	assert.Eventually(t, func() bool { return len(api.callsOf("answerCallbackQuery")) == 1 }, eventTimeout, 10*time.Millisecond)
	lc.RequireStop()

	answer := api.callsOf("answerCallbackQuery")[0]
	assert.Equal(t, designerAuth, answer.token)
	assert.JSONEq(t, `{"callback_query_id":"cb-stranger"}`, string(answer.body))
	assert.Empty(t, handler.events)
}

func TestGatewayStopsBotWithRejectedToken(t *testing.T) {
	t.Parallel()

	const rejected = "9:rejected-SECRET"

	api, baseURL := newFakeBotAPI(t)
	api.queue(marketerAuth, sourcesMessage(1, 1000, "работаем"))

	cfg := testConfig(baseURL, t.TempDir())
	cfg.DesignerToken = rejected

	var logs bytes.Buffer

	handler := newRecordingHandler()
	lc, status := startGateway(t, cfg, newMemoryStore(), handler, slog.New(slog.NewJSONHandler(&logs, nil)))

	event := handler.next(t)
	assert.Equal(t, int64(1000), event.MessageID, "other bots keep working")
	assert.Eventually(t, func() bool { return status.Ping(t.Context()) != nil }, eventTimeout, 10*time.Millisecond)

	time.Sleep(50 * time.Millisecond)
	lc.RequireStop()

	require.ErrorIs(t, status.Ping(t.Context()), ErrBotsBroken)

	rejectedCalls := 0

	for _, call := range api.callsOf("getMe") {
		if call.token == rejected {
			rejectedCalls++
		}
	}

	assert.Equal(t, 1, rejectedCalls, "a rejected token is not retried")
	assert.Contains(t, logs.String(), "TELEGRAM_DESIGNER_TOKEN")
	assert.NotContains(t, logs.String(), "SECRET")
}

func TestGatewayMarketerCallbackDoesNotWaitForVideos(t *testing.T) {
	t.Parallel()

	api, baseURL := newFakeBotAPI(t)
	gate := make(chan struct{})
	api.fileGate = gate

	first := sourcesMessage(1, 710, "")
	first.Message.Video = &telegram.Video{FileID: "slow-1", FileSize: 10}
	second := sourcesMessage(2, 711, "")
	second.Message.Video = &telegram.Video{FileID: "slow-2", FileSize: 10}
	callback := telegram.Update{UpdateID: 3, CallbackQuery: &telegram.CallbackQuery{
		ID: "cb-marketer", From: telegram.User{ID: testUserID}, Data: "approve:8",
		Message: &telegram.Message{MessageID: 302, Chat: telegram.Chat{ID: testGroupID, Type: supergroupType}},
	}}
	api.queue(marketerAuth, first, second, callback)

	cfg := testConfig(baseURL, t.TempDir())
	cfg.Workers = 1

	handler := newRecordingHandler()
	lc, _ := startGateway(t, cfg, newMemoryStore(), handler, slog.New(slog.DiscardHandler))

	event := handler.next(t)
	assert.Equal(t, CallbackEventType, event.Type, "the callback is handled while both videos wait")
	assert.Equal(t, MarketerRole, event.ReceivedBy)

	close(gate)

	assert.Equal(t, MessageEventType, handler.next(t).Type)
	assert.Equal(t, MessageEventType, handler.next(t).Type)

	lc.RequireStop()
}

func TestGatewayFinishesWaitingEventsOnStop(t *testing.T) {
	t.Parallel()

	api, baseURL := newFakeBotAPI(t)
	gate := make(chan struct{}) // the first video holds the only worker
	api.fileGate = gate

	first := sourcesMessage(1, 720, "")
	first.Message.Video = &telegram.Video{FileID: "stuck-1", FileSize: 10}
	second := sourcesMessage(2, 721, "")
	second.Message.Video = &telegram.Video{FileID: "stuck-2", FileSize: 10}
	api.queue(marketerAuth, first, second)

	cfg := testConfig(baseURL, t.TempDir())
	cfg.Workers = 1

	store := newMemoryStore()
	handler := newRecordingHandler()
	lc, _ := startGateway(t, cfg, store, handler, slog.New(slog.DiscardHandler))

	assert.Eventually(t, func() bool { return store.offset(1) == 3 }, eventTimeout, 10*time.Millisecond)

	stopped := make(chan error, 1)

	go func() { stopped <- lc.Stop(t.Context()) }()

	time.Sleep(50 * time.Millisecond) // polling is stopped, one video waits for the slot
	close(gate)

	require.NoError(t, <-stopped)

	ids := []int64{handler.next(t).MessageID, handler.next(t).MessageID}
	assert.ElementsMatch(t, []int64{720, 721}, ids, "the queue is handled within the stop timeout")
	assert.Empty(t, store.unmarkedKeys())
}

func TestPrepareOutbox(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	file := filepath.Join(base, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	link := filepath.Join(base, "link")
	require.NoError(t, os.Symlink(t.TempDir(), link))

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "created", input: filepath.Join(base, "outbox", "nested")},
		{name: "symlink", input: link, wantErr: true},
		{name: "file", input: file, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := prepareOutbox(tt.input)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrInvalidConfig)

				return
			}

			require.NoError(t, err)

			info, err := os.Lstat(tt.input)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
		})
	}
}
