package teamchat

import (
	"context"
	"encoding/json"
	"fmt"
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
	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"
	"go.uber.org/fx/fxtest"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/knowledge"
	"github.com/Prokud1n/ai-agents/internal/llmusage"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/pgtest"
	"github.com/Prokud1n/ai-agents/internal/providers/embeddings"
	"github.com/Prokud1n/ai-agents/internal/providers/redis"
	"github.com/Prokud1n/ai-agents/internal/providers/telegram"
	"github.com/Prokud1n/ai-agents/internal/redistest"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	integrationWait  = 20 * time.Second
	integrationTick  = 20 * time.Millisecond
	firstVersionLine = "Хук: три ошибки в планке, которые крадут результат. "
	secondVersion    = "Три ошибки в планке. Коротко."
	revisionRequest  = "Короче, без вступления"
	ideaMessageID    = int64(10)
	revisionMsgID    = int64(11)
	videoMessageID   = int64(12)
	voiceMessageID   = int64(13)
	videoFileID      = "video-file-id"
	neverWait        = 300 * time.Millisecond
)

// firstVersion is above the Telegram limit: the draft is sent in two parts.
var firstVersion = strings.Repeat(firstVersionLine, 100)

// scriptedExecutor answers runs in order and remembers the prompts.
type scriptedExecutor struct {
	mu      sync.Mutex
	answers []string
	prompts []string
}

func (e *scriptedExecutor) Execute(_ context.Context, data *tasks.ExecuteData) (tasks.Output, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.prompts = append(e.prompts, data.Prompt)
	answer := e.answers[0]

	if len(e.answers) > 1 {
		e.answers = e.answers[1:]
	}

	return tasks.Output{Text: answer}, nil
}

func (e *scriptedExecutor) Prompts() []string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return append([]string(nil), e.prompts...)
}

// TestIntegrationGatewayTaskCycle runs the application's gateway, tasks and teamchat modules against a fake
// Bot API with a scripted agent: an idea in «Планёрка» → a draft with buttons → a revision by a reply →
// version 2 → «Одобрить» → the task is approved and the buttons are removed.
func TestIntegrationGatewayTaskCycle(t *testing.T) {
	t.Parallel()

	env := startIntegrationEnv(t)
	api, groupID, marketer, marketerBot := env.api, env.groupID, env.marketer, env.marketerBot

	// An idea in «Планёрка» becomes the marketer's task; the draft answers it with buttons.
	api.queue(marketer, telegram.Update{UpdateID: 1, Message: topicMessage(groupID, ideaMessageID, "Идея: рилс про планку")})

	first := waitDraft(t, api, 1)
	assert.Equal(t, marketer, first.call.token, "the draft comes from the bot of the agent")
	assert.Equal(t, testTopicID, first.params.MessageThreadID)

	// The long draft: the first part answers the idea, the last one has the buttons.
	firstPart := sentMessage(t, api, first.call.result-1)
	require.NotNil(t, firstPart.ReplyParameters)
	assert.Equal(t, ideaMessageID, firstPart.ReplyParameters.MessageID)
	assert.Nil(t, firstPart.ReplyMarkup)
	assert.Equal(t, firstVersion, firstPart.Text+first.params.Text)

	cb, err := tasks.ParseCallback(first.params.ReplyMarkup.InlineKeyboard[0][0].CallbackData)
	require.NoError(t, err)
	assert.Equal(t, 1, cb.Version)
	waitDraftStored(t, env, cb.TaskID, 1)

	require.Eventually(t, func() bool {
		reactions := api.callsOf("setMessageReaction")

		return len(reactions) == 1 && strings.Contains(string(reactions[0].body), `"message_id":10`)
	}, integrationWait, integrationTick, "the idea is marked as taken")

	// A reply to the first part of the draft is a revision: the old draft loses its buttons, version 2 comes
	// with new ones.
	revision := topicMessage(groupID, revisionMsgID, revisionRequest)
	revision.ReplyToMessage = &telegram.Message{
		MessageID: first.call.result - 1, From: &marketerBot, Chat: revision.Chat,
		IsTopicMessage: true, MessageThreadID: testTopicID,
	}
	api.queue(marketer, telegram.Update{UpdateID: 2, Message: revision})

	second := waitDraft(t, api, 2)
	assert.Equal(t, "Версия 2\n\n"+secondVersion, second.params.Text)
	assert.Equal(t, tasks.DraftButtons(cb.TaskID, 2)[0].CallbackData, second.params.ReplyMarkup.InlineKeyboard[0][0].CallbackData)
	assert.Contains(t, env.executor.Prompts()[1], revisionRequest)
	waitButtonsRemoved(t, api, first.call.result)
	waitDraftStored(t, env, cb.TaskID, 2)

	// «Одобрить» under version 2.
	api.queue(marketer, telegram.Update{UpdateID: 3, CallbackQuery: &telegram.CallbackQuery{
		ID:   env.callbackID,
		From: telegram.User{ID: testUserID, FirstName: "Анастасия"},
		Message: &telegram.Message{
			MessageID: second.call.result, From: &marketerBot,
			Chat:           telegram.Chat{ID: groupID, Type: supergroupType, IsForum: true},
			IsTopicMessage: true, MessageThreadID: testTopicID,
		},
		Data: second.params.ReplyMarkup.InlineKeyboard[0][0].CallbackData,
	}})

	require.Eventually(t, func() bool {
		task, err := env.service.Get(context.Background(), cb.TaskID)

		return err == nil && task.Status == tasks.ApprovedStatus
	}, integrationWait, integrationTick)

	waitButtonsRemoved(t, api, second.call.result)

	expectedAnswer := `{"callback_query_id":"` + env.callbackID + `","text":"` + approvedAnswer + `"}`

	require.Eventually(t, func() bool {
		answers := api.callsOf("answerCallbackQuery")

		return len(answers) == 1 && string(answers[0].body) == expectedAnswer
	}, integrationWait, integrationTick, "the press is answered")

	require.Eventually(t, func() bool {
		return hasText(api, fmt.Sprintf(revisionFormat, 2)) && hasText(api, approvedText)
	}, integrationWait, integrationTick, "the decisions are told in the topic")
}

// TestIntegrationVideoIntake runs a video through the real gateway against the fake Bot API: a video in
// «Исходники» is downloaded (getFile, a file in TELEGRAM_FILES_DIR), stored, becomes the editor's task and is
// answered by the editor's bot; the same message delivered again is skipped by the gateway's mark in Redis (one copy,
// one task, one answer); a voice message is answered without a download.
func TestIntegrationVideoIntake(t *testing.T) {
	t.Parallel()

	env := startIntegrationEnv(t)
	api := env.api

	path := filepath.Join(env.filesDir, testVideoName)
	require.NoError(t, os.WriteFile(path, []byte(testVideoContent), 0o600))
	api.addFile(telegram.File{
		FileID: videoFileID, FileUniqueID: testFileUniqueID, FileSize: int64(len(testVideoContent)), FilePath: path,
	})

	video := topicMessage(env.groupID, videoMessageID, "")
	video.MessageThreadID = testSourcesTopicID
	video.Video = &telegram.Video{
		FileID: videoFileID, FileUniqueID: testFileUniqueID, MIMEType: "video/mp4",
		FileSize: int64(len(testVideoContent)), Duration: 3,
	}

	// The same message twice: a redelivery (e.g. the offset was not saved before a restart).
	api.queue(env.marketer, telegram.Update{UpdateID: 1, Message: video}, telegram.Update{UpdateID: 2, Message: video})

	require.Eventually(t, func() bool {
		return hasText(api, videoAcceptedHereText)
	}, integrationWait, integrationTick, "the video is stored and answered")

	voice := topicMessage(env.groupID, voiceMessageID, "")
	voice.Voice = &telegram.Voice{FileID: "voice-file-id", FileUniqueID: "voice-uniq", MIMEType: "audio/ogg", Duration: 5}
	api.queue(env.marketer, telegram.Update{UpdateID: 3, Message: voice})

	require.Eventually(t, func() bool {
		return hasText(api, voiceText)
	}, integrationWait, integrationTick, "the voice message is answered")

	assert.Never(t, func() bool {
		return countText(api, videoAcceptedHereText) > 1 || len(env.store.Files()) > 1
	}, neverWait, integrationTick, "a redelivered video is stored and answered once")

	assert.Len(t, api.callsOf("getFile"), 1, "only the video is downloaded")

	// Create finds the task of the same source message: one task for the redelivered video.
	task, created, err := env.service.Create(t.Context(), &tasks.CreateTaskData{
		Agent: string(gateway.EditorRole), Input: "lookup", Source: tasks.Source{ChatID: env.groupID, MessageID: videoMessageID},
	})
	require.NoError(t, err)
	assert.False(t, created, "the video already has its task")
	assert.Equal(t, string(gateway.EditorRole), task.Agent)
	assert.Equal(t, ReelsEditTaskKind, task.Kind)
	assert.Equal(t, []string{"incoming/" + strconv.FormatInt(env.groupID, 10) + "/" +
		strconv.FormatInt(videoMessageID, 10) + "/" + testFileUniqueID + ".mp4"}, task.Media)
	assert.Equal(t, [][]byte{[]byte(testVideoContent)}, env.store.Saved())
	assert.Equal(t, []media.File{{
		ChatID: env.groupID, MessageID: videoMessageID, FileUniqueID: testFileUniqueID, MIMEType: "video/mp4",
	}}, env.store.Files())

	for _, c := range api.callsOf("sendMessage") {
		var params telegram.SendMessageParams
		require.NoError(t, json.Unmarshal(c.body, &params))

		if params.Text == videoAcceptedHereText {
			assert.Equal(t, env.editor, c.token, "the editor's bot answers the video")
			assert.Equal(t, testSourcesTopicID, params.MessageThreadID)
		}
	}
}

// TestIntegrationStopOrder: on stop the gateway (no new events) and the workers stop before the handler and
// the task service wait for their background work, so nothing adds background work while it is waited for.
func TestIntegrationStopOrder(t *testing.T) {
	t.Parallel()

	env := startIntegrationEnv(t)
	env.stop()

	gatewayStop := env.stops.index("(*gateway).stop")
	poolStop := env.stops.index("(*Pool).Stop")
	handlerWait := env.stops.index("(*Handler).WaitBackground")
	serviceWait := env.stops.index("(*Service).WaitBackground")

	for name, i := range map[string]int{
		"gateway": gatewayStop, "pool": poolStop, "handler": handlerWait, "service": serviceWait,
	} {
		require.NotEqual(t, -1, i, "the %s stop hook did not run: %v", name, env.stops.hooks)
	}

	assert.Less(t, gatewayStop, handlerWait, "the gateway stops before the handler waits")
	assert.Less(t, gatewayStop, serviceWait, "the gateway stops before the service waits")
	assert.Less(t, poolStop, serviceWait, "the workers stop before the service waits")
}

// integrationEnv is the started gateway, tasks and teamchat modules against the fake Bot API.
type integrationEnv struct {
	api         *fakeBotAPI
	service     *tasks.Service
	executor    *scriptedExecutor
	store       *fakeStore
	filesDir    string // TELEGRAM_FILES_DIR of the gateway
	editor      string // the editor bot's token
	groupID     int64
	callbackID  string
	marketer    string // the marketer bot's token
	marketerBot telegram.User
	stops       *stopRecorder
	stop        func()
}

// stopRecorder is an fx logger that records the OnStop hooks in the order fx runs them.
type stopRecorder struct {
	mu    sync.Mutex
	hooks []string
}

func (r *stopRecorder) LogEvent(event fxevent.Event) {
	if e, ok := event.(*fxevent.OnStopExecuting); ok {
		r.mu.Lock()
		defer r.mu.Unlock()

		r.hooks = append(r.hooks, e.FunctionName)
	}
}

// index is the position of the first hook whose function name contains name; -1 if none ran.
func (r *stopRecorder) index(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i, hook := range r.hooks {
		if strings.Contains(hook, name) {
			return i
		}
	}

	return -1
}

func startIntegrationEnv(t *testing.T) *integrationEnv {
	t.Helper()

	db := pgtest.Connect(t)
	rdb := redistest.Connect(t)
	prefix := redistest.Prefix(t, rdb)

	// The gateway keeps its state in Redis under fixed keys (bot ID, chat ID): unique IDs keep runs apart.
	base := (time.Now().UnixNano() % 1_000_000_000) * 10
	env := &integrationEnv{
		groupID:    -1_000_000_000_000 - base,
		callbackID: "cb-" + strconv.FormatInt(base, 10),
		marketer:   botToken(gateway.MarketerRole, base),
		editor:     botToken(gateway.EditorRole, base),
		executor:   &scriptedExecutor{answers: []string{firstVersion, secondVersion}},
		store:      &fakeStore{},
		stops:      &stopRecorder{},
	}
	env.marketerBot = botUsers(base)[env.marketer]
	env.api = newFakeBotAPI(t, env.groupID, botUsers(base))
	cleanGatewayKeys(t, rdb, base, env.groupID, env.callbackID)

	gatewayCfg := gatewayConfig(t, env.api, base)
	env.filesDir = gatewayCfg.FilesDir

	app := fxtest.New(t,
		fx.Supply(
			gatewayCfg,
			integrationTasksConfig(prefix),
			&config.LLM{}, &config.OpenAI{}, &config.Embeddings{},
			slog.New(slog.DiscardHandler),
			db, rdb,
		),
		embeddings.Module,
		knowledge.Module,
		llmusage.Module,
		gateway.Module,
		tasks.Module,
		Module,
		fx.Decorate(func() tasks.Executor { return env.executor }),
		// Videos go to a fake store: S3 of the compose stack is not reachable from the tests.
		fx.Decorate(func() mediaStore { return env.store }),
		fx.Populate(&env.service),
		fx.WithLogger(func() fxevent.Logger { return env.stops }),
	)
	app.RequireStart()

	var once sync.Once

	env.stop = func() { once.Do(app.RequireStop) }
	t.Cleanup(env.stop)

	return env
}

type sentDraft struct {
	call   botCall
	params telegram.SendMessageParams
}

// waitDraft waits for the n-th message with buttons.
func waitDraft(t *testing.T, api *fakeBotAPI, n int) sentDraft {
	t.Helper()

	var drafts []sentDraft

	require.Eventually(t, func() bool {
		drafts = drafts[:0]

		for _, c := range api.callsOf("sendMessage") {
			var params telegram.SendMessageParams
			if json.Unmarshal(c.body, &params) == nil && params.ReplyMarkup != nil {
				drafts = append(drafts, sentDraft{call: c, params: params})
			}
		}

		return len(drafts) >= n
	}, integrationWait, integrationTick, "draft %d is not sent", n)

	return drafts[n-1]
}

// waitDraftStored waits until the messages of the draft version are stored: the worker stores them right
// after Telegram answers, and only then a reply or a button press finds the draft.
func waitDraftStored(t *testing.T, env *integrationEnv, taskID int64, version int) {
	t.Helper()

	require.Eventually(t, func() bool {
		v, err := env.service.GetVersion(context.Background(), taskID, version)

		return err == nil && v.DraftMessageID != nil
	}, integrationWait, integrationTick, "draft %d of task %d is not stored", version, taskID)
}

// sentMessage returns the sendMessage call that got the message ID.
func sentMessage(t *testing.T, api *fakeBotAPI, messageID int64) telegram.SendMessageParams {
	t.Helper()

	for _, c := range api.callsOf("sendMessage") {
		if c.result != messageID {
			continue
		}

		var params telegram.SendMessageParams
		require.NoError(t, json.Unmarshal(c.body, &params))

		return params
	}

	require.FailNow(t, "message is not sent", "message %d", messageID)

	return telegram.SendMessageParams{}
}

func waitButtonsRemoved(t *testing.T, api *fakeBotAPI, messageID int64) {
	t.Helper()

	require.Eventually(t, func() bool {
		for _, c := range api.callsOf("editMessageReplyMarkup") {
			var params telegram.EditMessageReplyMarkupParams
			if json.Unmarshal(c.body, &params) == nil && params.MessageID == messageID && params.ReplyMarkup == nil {
				return true
			}
		}

		return false
	}, integrationWait, integrationTick, "buttons of message %d are not removed", messageID)
}

func hasText(api *fakeBotAPI, text string) bool {
	for _, c := range api.callsOf("sendMessage") {
		var params telegram.SendMessageParams
		if json.Unmarshal(c.body, &params) == nil && params.Text == text {
			return true
		}
	}

	return false
}

func countText(api *fakeBotAPI, text string) int {
	n := 0

	for _, c := range api.callsOf("sendMessage") {
		var params telegram.SendMessageParams
		if json.Unmarshal(c.body, &params) == nil && params.Text == text {
			n++
		}
	}

	return n
}

func topicMessage(groupID, messageID int64, text string) *telegram.Message {
	return &telegram.Message{
		MessageID:       messageID,
		From:            &telegram.User{ID: testUserID, FirstName: "Анастасия"},
		Date:            time.Now().Unix(),
		Chat:            telegram.Chat{ID: groupID, Type: supergroupType, IsForum: true},
		IsTopicMessage:  true,
		MessageThreadID: testTopicID,
		Text:            text,
	}
}

func integrationTasksConfig(prefix string) *config.Tasks {
	return &config.Tasks{
		Workers:          1,
		MaxAttempts:      2,
		RetryBaseDelay:   100 * time.Millisecond,
		RetryMaxDelay:    time.Second,
		Timeout:          30 * time.Second,
		ShutdownGrace:    2 * time.Second,
		SweepInterval:    time.Second,
		DeliveryAttempts: 3,
		Stream:           prefix + "tasks",
	}
}

// cleanGatewayKeys removes the gateway's Redis keys of the test: offsets of its bots, marks of its events.
func cleanGatewayKeys(t *testing.T, rdb *redis.Client, base, groupID int64, callbackID string) {
	t.Helper()

	t.Cleanup(func() {
		keys := []string{"telegram:seen:callback:" + callbackID}

		for i := range gateway.Roles {
			keys = append(keys, "telegram:offset:"+strconv.FormatInt(base+int64(i)+1, 10))
		}

		for _, id := range []int64{ideaMessageID, revisionMsgID, videoMessageID, voiceMessageID} {
			keys = append(keys, "telegram:seen:message:"+strconv.FormatInt(groupID, 10)+":"+strconv.FormatInt(id, 10))
		}

		if err := rdb.Conn().Del(context.Background(), keys...).Err(); err != nil {
			t.Errorf("failed to delete gateway keys: %v", err)
		}
	})
}
