package gateway

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/providers/telegram"
)

const approveText = "Одобрить"

func newTestSender(t *testing.T) (*Sender, *fakeBotAPI) {
	t.Helper()

	api, baseURL := newFakeBotAPI(t)
	cfg := testConfig(baseURL, t.TempDir())

	return NewSender(testBots(t, cfg), cfg), api
}

func TestSenderSendText(t *testing.T) {
	t.Parallel()

	sender, api := newTestSender(t)

	id, err := sender.SendText(t.Context(), CopywriterRole, &OutgoingMessage{
		TopicID: productionTopic,
		ReplyTo: 55,
		Text:    "Сценарий готов",
		Buttons: [][]Button{{{Text: approveText, Data: "approve:7"}, {Text: "Правки", Data: "edit:7"}}},
	})

	require.NoError(t, err)
	assert.Positive(t, id)

	calls := api.callsOf("sendMessage")
	require.Len(t, calls, 1)
	assert.Equal(t, copywriterAuth, calls[0].token, "sent on behalf of the role")

	var params telegram.SendMessageParams
	require.NoError(t, json.Unmarshal(calls[0].body, &params))
	assert.Equal(t, telegram.SendMessageParams{
		ChatID:          testGroupID,
		MessageThreadID: productionTopic,
		Text:            "Сценарий готов",
		ReplyParameters: &telegram.ReplyParameters{MessageID: 55, AllowSendingWithoutReply: true},
		ReplyMarkup: &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{{
			{Text: approveText, CallbackData: "approve:7"},
			{Text: "Правки", CallbackData: "edit:7"},
		}}},
	}, params)
}

func TestSenderEditsAndCallbacks(t *testing.T) {
	t.Parallel()

	sender, api := newTestSender(t)

	require.NoError(t, sender.EditText(t.Context(), MarketerRole, 10, "План v2", nil))
	require.NoError(t, sender.EditButtons(t.Context(), DesignerRole, 11, [][]Button{{{Text: "Ок", Data: "keep"}}}))
	require.NoError(t, sender.AnswerCallback(t.Context(), EditorRole, "cb-1", "Принято"))
	require.NoError(t, sender.React(t.Context(), MarketerRole, 12, "👀"))
	require.ErrorIs(t, sender.React(t.Context(), MarketerRole, 0, "👀"), ErrValidation)

	edit := api.callsOf("editMessageText")
	require.Len(t, edit, 1)
	assert.Equal(t, marketerAuth, edit[0].token)
	assert.JSONEq(t, `{"chat_id":-1001000,"message_id":10,"text":"План v2"}`, string(edit[0].body))

	buttons := api.callsOf("editMessageReplyMarkup")
	require.Len(t, buttons, 1)
	assert.Equal(t, designerAuth, buttons[0].token)
	assert.JSONEq(t,
		`{"chat_id":-1001000,"message_id":11,"reply_markup":{"inline_keyboard":[[{"text":"Ок","callback_data":"keep"}]]}}`,
		string(buttons[0].body))

	answer := api.callsOf("answerCallbackQuery")
	require.Len(t, answer, 1)
	assert.Equal(t, editorAuth, answer[0].token)
	assert.JSONEq(t, `{"callback_query_id":"cb-1","text":"Принято"}`, string(answer[0].body))

	reactions := api.callsOf("setMessageReaction")
	require.Len(t, reactions, 1, "an invalid reaction is not sent")
	assert.Equal(t, marketerAuth, reactions[0].token)
	assert.JSONEq(t, `{"chat_id":-1001000,"message_id":12,"reaction":[{"type":"emoji","emoji":"👀"}]}`,
		string(reactions[0].body))
}

// outboxFile writes a file into the sender's outbox directory.
func outboxFile(t *testing.T, sender *Sender, name, data string) string {
	t.Helper()

	path := filepath.Join(sender.uploadRoots[0], name)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))

	return path
}

func TestSenderSendMedia(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		input          MediaType
		expectedMethod string
	}{
		{name: "video", input: VideoMediaType, expectedMethod: "sendVideo"},
		{name: "photo", input: PhotoMediaType, expectedMethod: "sendPhoto"},
		{name: "document", input: DocumentMediaType, expectedMethod: "sendDocument"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sender, api := newTestSender(t)
			path := outboxFile(t, sender, "cover.png", "png")

			id, err := sender.SendMedia(t.Context(), DesignerRole, &OutgoingMedia{
				Type: tt.input, Path: path, TopicID: productionTopic, Caption: "Обложка",
			})

			require.NoError(t, err)
			assert.Positive(t, id)

			calls := api.callsOf(tt.expectedMethod)
			require.Len(t, calls, 1)
			assert.Equal(t, designerAuth, calls[0].token)
			assert.Contains(t, string(calls[0].body), "Обложка")
		})
	}
}

func TestSenderValidation(t *testing.T) {
	t.Parallel()

	sender, _ := newTestSender(t)

	tests := []struct {
		name     string
		send     func() error
		expected error
	}{
		{
			name: "unknown role",
			send: func() error {
				_, err := sender.SendText(t.Context(), Role("coach"), &OutgoingMessage{Text: "тест"})

				return err
			},
			expected: ErrUnknownRole,
		},
		{
			name: "button without data",
			send: func() error {
				_, err := sender.SendText(t.Context(), MarketerRole, &OutgoingMessage{
					Text: "hi", Buttons: [][]Button{{{Text: "Без данных"}}},
				})

				return err
			},
			expected: ErrValidation,
		},
		{
			name: "callback data above 64 bytes",
			send: func() error {
				return sender.EditButtons(t.Context(), MarketerRole, 1, [][]Button{{{Text: "Длинная", Data: strings.Repeat("x", 65)}}})
			},
			expected: ErrValidation,
		},
		{
			name: "blank text",
			send: func() error {
				_, err := sender.SendText(t.Context(), MarketerRole, &OutgoingMessage{Text: " \n\t"})

				return err
			},
			expected: ErrValidation,
		},
		{
			name: "voice cannot be sent",
			send: func() error {
				_, err := sender.SendMedia(t.Context(), MarketerRole, &OutgoingMedia{Type: VoiceMediaType, Path: "a.ogg"})

				return err
			},
			expected: ErrValidation,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, tt.send(), tt.expected)
		})
	}
}

func TestAckText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *Event
		expected string
	}{
		{
			name:     "message",
			input:    &Event{Type: MessageEventType, Text: "секрет"},
			expected: "Принял. Задача появится, когда подключим очередь задач.",
		},
		{
			name:     "command",
			input:    &Event{Type: CommandEventType, Command: PlanCommand},
			expected: "Команда /plan принята. Задача появится, когда подключим очередь задач.",
		},
		{
			name:     "downloaded video",
			input:    &Event{Type: MessageEventType, Media: &Media{Type: VideoMediaType, rel: "x.mp4", Size: 3 << 20}},
			expected: "Принял файл (video, 3.0 МБ). Задача появится, когда подключим очередь задач.",
		},
		{
			name:     "too large",
			input:    &Event{Type: MessageEventType, Media: &Media{Type: VideoMediaType, Size: 3 << 30, TooLarge: true}},
			expected: "Файл (video, 3072.0 МБ) больше лимита, скачать его не могу.",
		},
		{
			name:     "not downloaded",
			input:    &Event{Type: MessageEventType, Media: &Media{Type: VoiceMediaType}},
			expected: "Не смог скачать файл (voice), попробуй отправить ещё раз.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, ackText(tt.input))
		})
	}
}

func TestAckHandlerRepliesFromTargetBot(t *testing.T) {
	t.Parallel()

	sender, api := newTestSender(t)
	handler := NewAckHandler(sender, slog.New(slog.DiscardHandler))

	err := handler.HandleEvent(t.Context(), &Event{
		Type: MessageEventType, ReceivedBy: MarketerRole, Target: EditorRole,
		ChatID: testGroupID, TopicID: sourcesTopic, MessageID: 200, Text: "Смонтируй",
	})
	require.NoError(t, err)

	err = handler.HandleEvent(t.Context(), &Event{
		Type: CallbackEventType, ReceivedBy: DesignerRole, Target: DesignerRole,
		Callback: &Callback{ID: "cb-9", Data: "approve:9"},
	})
	require.NoError(t, err)

	sent := api.callsOf("sendMessage")
	require.Len(t, sent, 1)
	assert.Equal(t, editorAuth, sent[0].token)

	var params telegram.SendMessageParams
	require.NoError(t, json.Unmarshal(sent[0].body, &params))
	assert.Equal(t, sourcesTopic, params.MessageThreadID)
	assert.Equal(t, int64(200), params.ReplyParameters.MessageID)
	assert.NotContains(t, params.Text, "Смонтируй")

	answers := api.callsOf("answerCallbackQuery")
	require.Len(t, answers, 1)
	assert.Equal(t, designerAuth, answers[0].token)
}

func TestSenderSendMediaOnlyFromAllowedDirs(t *testing.T) {
	t.Parallel()

	sender, api := newTestSender(t)
	outbox := sender.uploadRoots[0]
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("secret"), 0o600))
	outboxFile(t, sender, "keep.txt", "ok")
	require.NoError(t, os.Symlink(secret, filepath.Join(outbox, "link.txt")))
	require.NoError(t, os.Symlink(outside, filepath.Join(outbox, "escape")))

	escape, err := filepath.Rel(outbox, secret)
	require.NoError(t, err)

	fifo := filepath.Join(outbox, "pipe")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))

	// Files of the Bot API server are not uploaded: their paths hold the token and never leave the gateway.
	inFiles := filepath.Join(filepath.Dir(outbox), "video.mp4")
	require.NoError(t, os.WriteFile(inFiles, []byte("v"), 0o600))

	tests := []struct {
		name  string
		input string
	}{
		{name: "dot-dot", input: outbox + "/" + escape},
		{name: "outside path", input: secret},
		{name: "relative path", input: "secret.txt"},
		{name: "file symlink outside", input: filepath.Join(outbox, "link.txt")},
		{name: "directory symlink outside", input: filepath.Join(outbox, "escape", "secret.txt")},
		{name: "proc environ", input: "/proc/self/environ"},
		{name: "fifo would block", input: fifo},
		{name: "bot api files directory", input: inFiles},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := sender.SendMedia(t.Context(), DesignerRole, &OutgoingMedia{Type: DocumentMediaType, Path: tt.input})

			require.Error(t, err)
		})
	}

	t.Cleanup(func() {
		assert.Empty(t, api.callsOf("sendDocument"), "nothing outside the allowed directories is uploaded")
	})
}

func TestSplitText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		limit    int
		expected []string
	}{
		{name: "short", input: "коротко", limit: 10, expected: []string{"коротко"}},
		{name: "at a line break", input: "один два\nтри четыре", limit: 12, expected: []string{"один два\n", "три четыре"}},
		{name: "at a space", input: "один два три", limit: 9, expected: []string{"один два ", "три"}},
		{name: "hard cut", input: "абвгдежзик", limit: 4, expected: []string{"абвг", "дежз", "ик"}},
		{name: "emoji count as two units", input: "😀😀😀", limit: 4, expected: []string{"😀😀", "😀"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, splitText(tt.input, tt.limit))
		})
	}
}

func TestSenderSplitsLongText(t *testing.T) {
	t.Parallel()

	sender, api := newTestSender(t)
	text := strings.Repeat("слово ", 1000) // 6000 characters

	id, err := sender.SendText(t.Context(), MarketerRole, &OutgoingMessage{
		Text: text, ReplyTo: 5, Buttons: [][]Button{{{Text: approveText, Data: "approve:5"}}},
	})
	require.NoError(t, err)

	calls := api.callsOf("sendMessage")
	require.Len(t, calls, 2)

	var first, last telegram.SendMessageParams
	require.NoError(t, json.Unmarshal(calls[0].body, &first))
	require.NoError(t, json.Unmarshal(calls[1].body, &last))

	assert.Equal(t, text, first.Text+last.Text)
	assert.LessOrEqual(t, utf16Len(first.Text), maxMessageLength)
	assert.NotNil(t, first.ReplyParameters)
	assert.Nil(t, first.ReplyMarkup)
	assert.Nil(t, last.ReplyParameters)
	assert.NotNil(t, last.ReplyMarkup)
	assert.Equal(t, api.lastMessageID(), id, "the ID of the message with the buttons")

	ids, err := sender.SendTextParts(t.Context(), MarketerRole, &OutgoingMessage{Text: text})
	require.NoError(t, err)
	assert.Equal(t, []int64{api.lastMessageID() - 1, api.lastMessageID()}, ids, "the IDs of all parts in order")
}

// TestSenderSendTextPartsFailure: when a part fails, the IDs of the parts already sent come with the error.
func TestSenderSendTextPartsFailure(t *testing.T) {
	t.Parallel()

	sender, api := newTestSender(t)
	api.failSendMessage(2)

	ids, err := sender.SendTextParts(t.Context(), MarketerRole, &OutgoingMessage{Text: strings.Repeat("слово ", 1000)})

	require.Error(t, err)
	assert.Equal(t, []int64{api.lastMessageID()}, ids, "the first part was sent")
}

func TestSenderMovesLongCaptionToText(t *testing.T) {
	t.Parallel()

	sender, api := newTestSender(t)
	path := outboxFile(t, sender, "draft.mp4", "video")
	caption := strings.Repeat("а", maxCaptionLength+1)

	id, err := sender.SendMedia(t.Context(), EditorRole, &OutgoingMedia{
		Type: VideoMediaType, Path: path, Caption: caption, Buttons: [][]Button{{{Text: approveText, Data: "approve:6"}}},
	})
	require.NoError(t, err)

	videos := api.callsOf("sendVideo")
	require.Len(t, videos, 1)
	assert.NotContains(t, string(videos[0].body), "reply_markup")
	assert.NotContains(t, string(videos[0].body), "name=\"caption\"")

	texts := api.callsOf("sendMessage")
	require.Len(t, texts, 1)

	var params telegram.SendMessageParams
	require.NoError(t, json.Unmarshal(texts[0].body, &params))
	assert.Equal(t, caption, params.Text)
	assert.NotNil(t, params.ReplyParameters, "the caption answers the video")
	assert.NotNil(t, params.ReplyMarkup)
	assert.Equal(t, api.lastMessageID(), id)
}
