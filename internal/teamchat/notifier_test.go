package teamchat

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/providers/telegram"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const draftBody = "Хук: три ошибки в планке"

func draftButtons(version int) [][]gateway.Button {
	buttons := tasks.DraftButtons(testTaskID, version)

	return [][]gateway.Button{{
		{Text: buttons[0].Text, Data: buttons[0].CallbackData},
		{Text: buttons[1].Text, Data: buttons[1].CallbackData},
		{Text: buttons[2].Text, Data: buttons[2].CallbackData},
	}}
}

func TestNotifierSendDraft(t *testing.T) {
	t.Parallel()

	longText := strings.Repeat("а", partLimit+1)

	tests := []struct {
		name         string
		draft        tasks.Draft
		expected     sentText
		expectedSent tasks.SentDraft
	}{
		{
			name: "first version from the agent's bot with buttons",
			draft: tasks.Draft{
				TaskID: testTaskID, Version: 1, Agent: string(gateway.CopywriterRole), ChatID: testChatID,
				TopicID: testTopicID, ReplyToMessageID: testMessageID, Text: draftBody,
				Buttons: tasks.DraftButtons(testTaskID, 1),
			},
			expected: sentText{Role: gateway.CopywriterRole, Msg: gateway.OutgoingMessage{
				TopicID: testTopicID, ReplyTo: testMessageID, Text: draftBody, Buttons: draftButtons(1),
			}},
			expectedSent: tasks.SentDraft{MessageID: testDraftID},
		},
		{
			name: "long draft: the earlier parts are returned too",
			draft: tasks.Draft{
				TaskID: testTaskID, Version: 1, Agent: string(gateway.CopywriterRole), ChatID: testChatID,
				TopicID: testTopicID, ReplyToMessageID: testMessageID, Text: longText,
				Buttons: tasks.DraftButtons(testTaskID, 1),
			},
			expected: sentText{Role: gateway.CopywriterRole, Msg: gateway.OutgoingMessage{
				TopicID: testTopicID, ReplyTo: testMessageID, Text: longText, Buttons: draftButtons(1),
			}},
			expectedSent: tasks.SentDraft{MessageID: testDraftID, PartIDs: []int64{testDraftID - 1}},
		},
		{
			name: "revised version with a note",
			draft: tasks.Draft{
				TaskID: testTaskID, Version: 3, Agent: string(gateway.MarketerRole), ChatID: testChatID,
				TopicID: testTopicID, ReplyToMessageID: testMessageID, Text: draftBody,
				Note: "Не получилось применить правку.", Buttons: tasks.DraftButtons(testTaskID, 3),
			},
			expected: sentText{Role: gateway.MarketerRole, Msg: gateway.OutgoingMessage{
				TopicID: testTopicID, ReplyTo: testMessageID,
				Text: "Версия 3\n\nНе получилось применить правку.\n\n" + draftBody, Buttons: draftButtons(3),
			}},
			expectedSent: tasks.SentDraft{MessageID: testDraftID},
		},
		{
			name: "agent without a bot writes from the marketer",
			draft: tasks.Draft{
				TaskID: testTaskID, Version: 1, Agent: "scout", ChatID: testChatID, ReplyToMessageID: testMessageID,
				Text: draftBody,
			},
			expected: sentText{Role: gateway.MarketerRole, Msg: gateway.OutgoingMessage{
				ReplyTo: testMessageID, Text: draftBody,
			}},
			expectedSent: tasks.SentDraft{MessageID: testDraftID},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sender := &fakeSender{}

			sent, err := NewNotifier(sender, slog.New(slog.DiscardHandler)).SendDraft(t.Context(), &tt.draft)

			require.NoError(t, err)
			assert.Equal(t, tt.expectedSent, sent)
			assert.Equal(t, []sentText{tt.expected}, sender.texts)
		})
	}
}

// TestNotifierSendDraftPartFails: the parts sent before the failure come back with the error.
func TestNotifierSendDraftPartFails(t *testing.T) {
	t.Parallel()

	sender := &fakeSender{partsErr: errTelegramDown}

	sent, err := NewNotifier(sender, slog.New(slog.DiscardHandler)).SendDraft(t.Context(), &tasks.Draft{
		TaskID: testTaskID, Version: 1, Agent: string(gateway.CopywriterRole), Text: draftBody,
	})

	require.ErrorIs(t, err, errTelegramDown)
	assert.Equal(t, tasks.SentDraft{PartIDs: []int64{testDraftID - 1}}, sent)
}

func TestNotifierCloseDraft(t *testing.T) {
	t.Parallel()

	removed := []buttonsEdit{{Role: gateway.CopywriterRole, MessageID: testDraftID}}

	decision := func(text string) []sentText {
		return []sentText{{Role: gateway.CopywriterRole, Msg: gateway.OutgoingMessage{
			TopicID: testTopicID, ReplyTo: testDraftID, Text: text,
		}}}
	}

	notModifiedErr := &telegram.APIError{Code: http.StatusBadRequest, Description: "Bad Request: message is not modified"}

	tests := []struct {
		name          string
		decision      tasks.Action
		duplicate     bool
		editErr       error
		expectedTexts []sentText
		expectedErr   error
	}{
		{name: "approved", decision: tasks.ApproveAction, expectedTexts: decision(approvedText)},
		{name: "rejected", decision: tasks.RejectAction, expectedTexts: decision(rejectedText)},
		{name: "sent for revision", decision: tasks.ReviseAction, expectedTexts: decision("Принял правку, готовлю версию 3.")},
		{name: "duplicate only loses its buttons", duplicate: true},
		{
			name: "buttons already removed", decision: tasks.ApproveAction, editErr: notModifiedErr,
			expectedTexts: decision(approvedText),
		},
		{
			name: "buttons not removed: the decision is still sent", decision: tasks.RejectAction,
			editErr: errTelegramDown, expectedTexts: decision(rejectedText), expectedErr: errTelegramDown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sender := &fakeSender{editErr: tt.editErr}

			err := NewNotifier(sender, slog.New(slog.DiscardHandler)).CloseDraft(t.Context(), &tasks.CloseDraftData{
				TaskID: testTaskID, Version: 2, Agent: string(gateway.CopywriterRole), ChatID: testChatID,
				TopicID: testTopicID, MessageID: testDraftID, Decision: tt.decision, Duplicate: tt.duplicate,
			})

			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr)
			} else {
				require.NoError(t, err)
			}

			assert.Equal(t, removed, sender.edits)
			assert.Equal(t, tt.expectedTexts, sender.texts)
		})
	}
}

func TestNotifierNotifyFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		message  string
		expected string
	}{
		{name: "message for the trainer", message: "Не получилось выполнить задачу. Таймаут.", expected: "Не получилось выполнить задачу. Таймаут."},
		{name: "empty message", message: " ", expected: failureFallback},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sender := &fakeSender{}

			err := NewNotifier(sender, slog.New(slog.DiscardHandler)).NotifyFailure(t.Context(), &tasks.Failure{
				TaskID: testTaskID, Agent: string(gateway.EditorRole), ChatID: testChatID, TopicID: testTopicID,
				ReplyToMessageID: testMessageID, Message: tt.message,
			})

			require.NoError(t, err)
			assert.Equal(t, []sentText{reply(gateway.EditorRole, tt.expected)}, sender.texts)
		})
	}
}

// TestNotifierLongDraft sends a draft above the Telegram limit through the real gateway.Sender: it is split,
// the buttons go with the last part, and the IDs of all parts are returned (a reply to any is a revision).
func TestNotifierLongDraft(t *testing.T) {
	t.Parallel()

	api := newFakeBotAPI(t, testChatID, testBotUsers())
	sender := testGatewaySender(t, api)

	text := strings.Repeat("Присед с паузой внизу. ", 300) // about 6900 characters: two messages

	sent, err := NewNotifier(sender, slog.New(slog.DiscardHandler)).SendDraft(t.Context(), &tasks.Draft{
		TaskID: testTaskID, Version: 1, Agent: string(gateway.CopywriterRole), ChatID: testChatID,
		TopicID: testTopicID, ReplyToMessageID: testMessageID, Text: text, Buttons: tasks.DraftButtons(testTaskID, 1),
	})
	require.NoError(t, err)

	calls := api.callsOf("sendMessage")
	require.Len(t, calls, 2)
	assert.Equal(t, tasks.SentDraft{MessageID: calls[1].result, PartIDs: []int64{calls[0].result}}, sent)

	var first, last telegram.SendMessageParams

	require.NoError(t, json.Unmarshal(calls[0].body, &first))
	require.NoError(t, json.Unmarshal(calls[1].body, &last))

	assert.Equal(t, testBotToken(gateway.CopywriterRole), calls[0].token)
	assert.Equal(t, testTopicID, first.MessageThreadID)
	require.NotNil(t, first.ReplyParameters)
	assert.Equal(t, testMessageID, first.ReplyParameters.MessageID)
	assert.Nil(t, first.ReplyMarkup)

	require.NotNil(t, last.ReplyMarkup)
	assert.Len(t, last.ReplyMarkup.InlineKeyboard[0], 3)
	assert.Nil(t, last.ReplyParameters)
	assert.Equal(t, text, first.Text+last.Text)
}

func TestNewNotifierFollowsGateway(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.DiscardHandler)

	assert.IsType(t, &tasks.LogNotifier{}, newNotifier(&config.Telegram{}, nil, nil, log), "no tokens: drafts go to the log")
	assert.IsType(t, &Notifier{}, newNotifier(&config.Telegram{MarketerToken: "1:x"}, nil, nil, log))
}
