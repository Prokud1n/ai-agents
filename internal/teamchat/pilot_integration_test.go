package teamchat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/providers/telegram"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	feedbackIdeaMsgID   = int64(20)
	feedbackMsgID       = int64(21)
	feedbackReplyMsgID  = int64(22)
	feedbackStatusMsgID = int64(23)
	onboardMsgID        = int64(24)
	onboardAgainMsgID   = int64(25)
	testProductionTopic = int64(30)
	testTechTopic       = int64(31)
)

func withPilotTopics(cfg *config.Telegram) {
	cfg.ProductionTopicID = testProductionTopic
	cfg.TechTopicID = testTechTopic
	cfg.AdminUserIDs = []int64{testUserID}
}

// TestIntegrationFeedback: /feedback is saved and copied to «Техничка»; a /feedback that replies to a draft is linked to
// its task; /status shows the metrics with the feedback count.
func TestIntegrationFeedback(t *testing.T) {
	t.Parallel()

	env := startIntegrationEnv(t, withPilotTopics)
	api, groupID, marketer := env.api, env.groupID, env.marketer

	api.queue(marketer, telegram.Update{UpdateID: 1, Message: topicMessage(groupID, feedbackIdeaMsgID, "Идея: рилс про планку")})

	draft := waitDraft(t, api, 1)
	cb, err := tasks.ParseCallback(draft.params.ReplyMarkup.InlineKeyboard[0][0].CallbackData)
	require.NoError(t, err)
	waitDraftStored(t, env, cb.TaskID, 1)

	api.queue(marketer, telegram.Update{UpdateID: 2, Message: commandMessage(groupID, feedbackMsgID, "/feedback Всё понятно")})

	require.Eventually(t, func() bool {
		return countText(api, feedbackThanksText) == 1
	}, integrationWait, integrationTick, "the feedback is thanked")

	onDraft := commandMessage(groupID, feedbackReplyMsgID, "/feedback Хук слабый")
	onDraft.ReplyToMessage = &telegram.Message{
		MessageID: draft.call.result, From: &env.marketerBot, Chat: onDraft.Chat,
		IsTopicMessage: true, MessageThreadID: testTopicID,
	}
	api.queue(marketer, telegram.Update{UpdateID: 3, Message: onDraft})

	require.Eventually(t, func() bool {
		return countText(api, feedbackThanksText) == 2
	}, integrationWait, integrationTick, "the feedback on the draft is thanked")

	list, total, err := env.pilot.ListFeedback(context.Background(), draftTime(t, env, cb.TaskID).AddDate(0, 0, -1),
		draftTime(t, env, cb.TaskID).AddDate(0, 0, 1), 10)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	assert.Equal(t, "Всё понятно", list[0].Text)
	assert.Zero(t, list[0].TaskID)
	assert.Equal(t, "Хук слабый", list[1].Text)
	assert.Equal(t, cb.TaskID, list[1].TaskID, "a reply to the draft is linked to its task")
	assert.Equal(t, 1, list[1].TaskVersion)
	assert.Equal(t, testUserID, list[1].AuthorID)
	assert.Equal(t, string(gateway.PlanningTopic), list[1].Topic)

	techCopies := techTexts(t, api)
	require.Len(t, techCopies, 2)
	assert.Equal(t, marketer, techCopies[1].token, "the copy comes from the marketer's bot")
	assert.True(t, strings.HasPrefix(techCopies[1].text, fmt.Sprintf("Отзыв №%d от %s, топик «Планёрка», к задаче %d (версия 1):\nХук слабый",
		list[1].ID, testUserName, cb.TaskID)), techCopies[1].text)

	api.queue(marketer, telegram.Update{UpdateID: 4, Message: commandMessage(groupID, feedbackStatusMsgID, "/status")})

	require.Eventually(t, func() bool {
		return containsText(api, pilotStatusHeader+"\n— Маркетолог: задач 1, одобрено 0, отклонено 0, сбоев 0.\nОтзывов: 2.")
	}, integrationWait, integrationTick, "/status shows the metrics of the pilot")
}

// TestIntegrationOnboarding: the marketer's bot gets the command menu of the group, the other bots lose theirs; /onboard
// sends the greetings from the bots of the topics and pins them; a repeated /onboard edits them without duplicates.
func TestIntegrationOnboarding(t *testing.T) {
	t.Parallel()

	env := startIntegrationEnv(t, withPilotTopics)
	api, groupID, marketer := env.api, env.groupID, env.marketer

	require.Eventually(t, func() bool {
		return len(api.callsOf("setMyCommands")) == 1 && len(api.callsOf("deleteMyCommands")) == 3*(len(gateway.Roles)-1)
	}, integrationWait, integrationTick, "the menu is set up on start")

	menu := api.callsOf("setMyCommands")[0]
	assert.Equal(t, marketer, menu.token)

	var menuParams telegram.SetMyCommandsParams
	require.NoError(t, json.Unmarshal(menu.body, &menuParams))
	assert.Equal(t, &telegram.BotCommandScope{Type: telegram.ChatCommandScope, ChatID: groupID}, menuParams.Scope)
	require.Len(t, menuParams.Commands, len(menuCommands))
	assert.Equal(t, "help", menuParams.Commands[0].Command)

	for _, c := range api.callsOf("deleteMyCommands") {
		assert.NotEqual(t, marketer, c.token, "the marketer's menu is not removed")
	}

	done := fmt.Sprintf(onboardDoneText, "«Планёрка», «Исходники», «Продакшн»")

	api.queue(marketer, telegram.Update{UpdateID: 1, Message: commandMessage(groupID, onboardMsgID, "/onboard")})

	require.Eventually(t, func() bool {
		return countText(api, done) == 1
	}, integrationWait, integrationTick, "the greetings are published")

	expected := map[string]struct {
		token string
		topic int64
	}{
		planningGreeting:   {token: marketer, topic: testTopicID},
		sourcesGreeting:    {token: env.editor, topic: testSourcesTopicID},
		productionGreeting: {token: env.editor, topic: testProductionTopic},
	}

	for _, c := range api.callsOf("sendMessage") {
		var params telegram.SendMessageParams
		require.NoError(t, json.Unmarshal(c.body, &params))

		if want, ok := expected[params.Text]; ok {
			assert.Equal(t, want.token, c.token, "the greeting comes from the bot of its topic")
			assert.Equal(t, want.topic, params.MessageThreadID)
		}
	}

	pins := api.callsOf("pinChatMessage")
	require.Len(t, pins, 3)

	for _, pin := range pins {
		assert.Equal(t, marketer, pin.token, "the marketer pins")
	}

	api.queue(marketer, telegram.Update{UpdateID: 2, Message: commandMessage(groupID, onboardAgainMsgID, "/onboard")})

	require.Eventually(t, func() bool {
		return countText(api, done) == 2
	}, integrationWait, integrationTick, "the repeated /onboard is answered")

	for text := range expected {
		assert.Equal(t, 1, countText(api, text), "no duplicate greeting")
	}

	assert.Len(t, api.callsOf("editMessageText"), 3, "the greetings are edited")
	assert.Len(t, api.callsOf("pinChatMessage"), 3, "and not pinned again: no service messages in the topics")
	assert.Empty(t, api.callsOf("unpinChatMessage"))
}

type techText struct {
	token string
	text  string
}

// techTexts are the messages sent to «Техничка».
func techTexts(t *testing.T, api *fakeBotAPI) []techText {
	t.Helper()

	var texts []techText

	for _, c := range api.callsOf("sendMessage") {
		var params telegram.SendMessageParams
		require.NoError(t, json.Unmarshal(c.body, &params))

		if params.MessageThreadID == testTechTopic {
			texts = append(texts, techText{token: c.token, text: params.Text})
		}
	}

	return texts
}

func containsText(api *fakeBotAPI, text string) bool {
	for _, c := range api.callsOf("sendMessage") {
		var params telegram.SendMessageParams
		if json.Unmarshal(c.body, &params) == nil && strings.Contains(params.Text, text) {
			return true
		}
	}

	return false
}

// draftTime is the creation time of the task: the feedback is looked for around it.
func draftTime(t *testing.T, env *integrationEnv, taskID int64) time.Time {
	t.Helper()

	task, err := env.service.Get(context.Background(), taskID)
	require.NoError(t, err)

	return task.CreatedAt
}
