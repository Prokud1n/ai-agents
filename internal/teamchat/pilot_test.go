package teamchat

import (
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/pilot"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

// greatFeedback is the text of the test feedback.
const greatFeedback = "Монтаж отличный"

// telegramLimit is the length of one Telegram message in UTF-16 code units.
const telegramLimit = 4096

// pilotCalls is what the handler did with the pilot store, the chat and «Техничка».
type pilotCalls struct {
	feedback []pilot.SaveFeedbackData
	texts    []sentText
	reports  []string
}

// newPilotHandler is a handler with the pilot fakes (new ones when nil).
func newPilotHandler(service *fakeService, sender *fakeSender, store *fakePilot, tech *fakeTech) *Handler {
	h := NewHandler(service, nil, sender, &fakeStore{}, slog.New(slog.DiscardHandler))
	h.pilot, h.tech = store, tech

	return h
}

func TestHelpText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		input         gateway.Topic
		expectedStart string
	}{
		{name: "planning", input: gateway.PlanningTopic, expectedStart: "Вы в «Планёрке»."},
		{name: "sources: the video and its revisions first", input: gateway.SourcesTopic, expectedStart: "Вы в «Исходниках»."},
		{name: "production", input: gateway.ProductionTopic, expectedStart: "Вы в «Продакшне»."},
		{name: "tech", input: gateway.TechTopic, expectedStart: "Это «Техничка»"},
		{name: "outside the topics: the general part only", input: gateway.OtherTopic, expectedStart: "Как работать с командой"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			text := helpText(tt.input)

			assert.True(t, strings.HasPrefix(text, tt.expectedStart), text)
			assert.True(t, strings.HasSuffix(text, helpGeneralText), "the general part follows")
			assert.LessOrEqual(t, len(utf16.Encode([]rune(text))), telegramLimit, "one Telegram message")
		})
	}
}

// TestHelpMentionsEveryMenuCommand: the trainer finds in /help every command of the menu.
func TestHelpMentionsEveryMenuCommand(t *testing.T) {
	t.Parallel()

	for _, c := range menuCommands {
		assert.Contains(t, helpGeneralText, "/"+string(c.Command), "the menu command is explained in /help")
		assert.LessOrEqual(t, len([]rune(c.Description)), 256, "Telegram limit of a description")
	}
}

func TestGreetingsFitOneMessage(t *testing.T) {
	t.Parallel()

	for _, text := range []string{planningGreeting, sourcesGreeting, productionGreeting} {
		assert.LessOrEqual(t, len(utf16.Encode([]rune(text))), telegramLimit)
		assert.Contains(t, text, "/help")
	}
}

func TestParseFeedback(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("ж", maxFeedbackLength+5)

	tests := []struct {
		name            string
		input           *gateway.Event
		expected        feedbackInput
		expectedRefusal string
	}{
		{name: "plain text", input: commandEvent(gateway.FeedbackCommand, "  Монтаж отличный "), expected: feedbackInput{text: greatFeedback}},
		{name: "empty: a hint", input: commandEvent(gateway.FeedbackCommand, "  "), expectedRefusal: emptyFeedbackText},
		{
			name:            "forwarded: someone else's words",
			input:           forwarded(commandEvent(gateway.FeedbackCommand, "чужой отзыв")),
			expectedRefusal: forwardedFeedbackText,
		},
		{
			name:     "long: cut to the limit",
			input:    commandEvent(gateway.FeedbackCommand, long),
			expected: feedbackInput{text: strings.Repeat("ж", maxFeedbackLength), truncated: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			input, refusal := parseFeedback(tt.input)

			assert.Equal(t, tt.expected, input)
			assert.Equal(t, tt.expectedRefusal, refusal)
		})
	}
}

func TestHandlerFeedback(t *testing.T) {
	t.Parallel()

	saved := func(taskID int64, version int, text string, truncated bool) []pilot.SaveFeedbackData {
		return []pilot.SaveFeedbackData{{
			AuthorID: testUserID, AuthorName: testUserName, Text: text, Truncated: truncated, TaskID: taskID,
			TaskVersion: version, Topic: string(gateway.PlanningTopic), TopicID: testTopicID, ChatID: testChatID,
			MessageID: testMessageID,
		}}
	}
	named := func(event *gateway.Event) *gateway.Event {
		event.From.FirstName = testUserName

		return event
	}

	tests := []struct {
		name     string
		service  *fakeService
		store    *fakePilot
		event    *gateway.Event
		expected pilotCalls
	}{
		{
			name:  "saved, copied to «Техничка», thanked",
			event: named(commandEvent(gateway.FeedbackCommand, greatFeedback)),
			expected: pilotCalls{
				feedback: saved(0, 0, greatFeedback, false),
				texts:    []sentText{reply(gateway.MarketerRole, feedbackThanksText)},
				reports:  []string{"Отзыв №1 от Анастасия, топик «Планёрка»:\nМонтаж отличный\nhttps://t.me/c/1000/7/100"},
			},
		},
		{
			name:  "a reply to a draft is linked to its task and version",
			event: named(replyTo(commandEvent(gateway.FeedbackCommand, "Субтитры мелкие"), testDraftID)),
			expected: pilotCalls{
				feedback: saved(testTaskID, 2, "Субтитры мелкие", false),
				texts:    []sentText{reply(gateway.MarketerRole, feedbackThanksText)},
				reports: []string{"Отзыв №1 от Анастасия, топик «Планёрка», к задаче 5 (версия 2):\nСубтитры мелкие\n" +
					"https://t.me/c/1000/7/100"},
			},
		},
		{
			name:    "a reply to another message is not linked",
			service: &fakeService{findErr: tasks.ErrNotFound},
			event:   named(replyTo(commandEvent(gateway.FeedbackCommand, "Всё понятно"), 55)),
			expected: pilotCalls{
				feedback: saved(0, 0, "Всё понятно", false),
				texts:    []sentText{reply(gateway.MarketerRole, feedbackThanksText)},
				reports:  []string{"Отзыв №1 от Анастасия, топик «Планёрка»:\nВсё понятно\nhttps://t.me/c/1000/7/100"},
			},
		},
		{
			name:  "long: saved cut, the trainer is told",
			event: named(commandEvent(gateway.FeedbackCommand, strings.Repeat("а", maxFeedbackLength+1))),
			expected: pilotCalls{
				feedback: saved(0, 0, strings.Repeat("а", maxFeedbackLength), true),
				texts:    []sentText{reply(gateway.MarketerRole, feedbackThanksText+feedbackTruncatedNote)},
				reports: []string{"Отзыв №1 от Анастасия, топик «Планёрка»:\n" + strings.Repeat("а", maxFeedbackLength) +
					"\n(обрезан до 2000 символов)\nhttps://t.me/c/1000/7/100"},
			},
		},
		{
			name:     "empty: a hint, nothing saved",
			event:    commandEvent(gateway.FeedbackCommand, ""),
			expected: pilotCalls{texts: []sentText{reply(gateway.MarketerRole, emptyFeedbackText)}},
		},
		{
			name:     "forwarded: not the trainer's feedback",
			event:    forwarded(commandEvent(gateway.FeedbackCommand, "чужой отзыв")),
			expected: pilotCalls{texts: []sentText{reply(gateway.MarketerRole, forwardedFeedbackText)}},
		},
		{
			name:  "redelivered: saved before, thanked again without a second copy",
			store: &fakePilot{exists: true},
			event: named(commandEvent(gateway.FeedbackCommand, greatFeedback)),
			expected: pilotCalls{
				feedback: saved(0, 0, greatFeedback, false),
				texts:    []sentText{reply(gateway.MarketerRole, feedbackThanksText)},
			},
		},
		{
			name:  "save failed",
			store: &fakePilot{saveErr: errDatabaseDown},
			event: named(commandEvent(gateway.FeedbackCommand, greatFeedback)),
			expected: pilotCalls{
				feedback: saved(0, 0, greatFeedback, false),
				texts:    []sentText{reply(gateway.MarketerRole, feedbackFailedText)},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			service, store := tt.service, tt.store
			if service == nil {
				service = &fakeService{}
			}

			if store == nil {
				store = &fakePilot{}
			}

			sender, tech := &fakeSender{}, &fakeTech{}
			h := newPilotHandler(service, sender, store, tech)

			require.NoError(t, h.HandleEvent(t.Context(), tt.event))
			assert.Equal(t, tt.expected, pilotCalls{feedback: store.feedback, texts: sender.texts, reports: tech.reports})
		})
	}
}

func TestGreetings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    *config.Telegram
		expected []greeting
	}{
		{name: "no topics", input: &config.Telegram{}, expected: []greeting{}},
		{
			name:  "every trainer's topic: «Планёрка» from the marketer, the rest from the editor; no «Техничка»",
			input: &config.Telegram{PlanningTopicID: 2, SourcesTopicID: 3, ProductionTopicID: 4, TechTopicID: 5},
			expected: []greeting{
				{topic: gateway.PlanningTopic, topicID: 2, role: gateway.MarketerRole, text: planningGreeting},
				{topic: gateway.SourcesTopic, topicID: 3, role: gateway.EditorRole, text: sourcesGreeting},
				{topic: gateway.ProductionTopic, topicID: 4, role: gateway.EditorRole, text: productionGreeting},
			},
		},
		{
			name:     "only the configured topics",
			input:    &config.Telegram{SourcesTopicID: 3},
			expected: []greeting{{topic: gateway.SourcesTopic, topicID: 3, role: gateway.EditorRole, text: sourcesGreeting}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, greetings(tt.input))
		})
	}
}

// onboardHandler is a handler for /onboard with two topics: «Планёрка» (2) and «Исходники» (3).
func onboardHandler(sender *fakeSender, store *fakePilot, tech *fakeTech, admins ...int64) *Handler {
	h := newPilotHandler(&fakeService{}, sender, store, tech)
	h.greetings = greetings(&config.Telegram{PlanningTopicID: 2, SourcesTopicID: 3})
	h.admins = adminSet(admins)

	return h
}

func TestHandlerOnboardPublishesThenEdits(t *testing.T) {
	t.Parallel()

	sender, store, tech := &fakeSender{nextID: 300}, &fakePilot{}, &fakeTech{}
	h := onboardHandler(sender, store, tech, testUserID)

	require.NoError(t, h.HandleEvent(t.Context(), commandEvent(gateway.OnboardCommand, "")))

	done := reply(gateway.MarketerRole, "Приветствия опубликованы и закреплены: «Планёрка», «Исходники».")
	assert.Equal(t, []sentText{
		{Role: gateway.MarketerRole, Msg: gateway.OutgoingMessage{TopicID: 2, Text: planningGreeting}},
		{Role: gateway.EditorRole, Msg: gateway.OutgoingMessage{TopicID: 3, Text: sourcesGreeting}},
		done,
	}, sender.texts)
	assert.Equal(t, []pinCall{{Role: gateway.MarketerRole, MessageID: 301}, {Role: gateway.MarketerRole, MessageID: 302}},
		sender.pins, "the marketer pins both")
	assert.Equal(t, map[string]pilot.OnboardingMessage{
		string(gateway.PlanningTopic): {
			ChatID: testChatID, Topic: string(gateway.PlanningTopic), TopicID: 2, Role: string(gateway.MarketerRole),
			MessageID: 301, Pinned: true,
		},
		string(gateway.SourcesTopic): {
			ChatID: testChatID, Topic: string(gateway.SourcesTopic), TopicID: 3, Role: string(gateway.EditorRole),
			MessageID: 302, Pinned: true,
		},
	}, store.greetings)

	// The second /onboard edits the same messages: no duplicates in the topics and no new pins (each pin is a service
	// message in the topic).
	sender.texts, sender.pins = nil, nil
	sender.editTextErr = gateway.ErrNotModified

	require.NoError(t, h.HandleEvent(t.Context(), commandEvent(gateway.OnboardCommand, "")))

	assert.Equal(t, []sentText{done}, sender.texts, "no new greeting")
	assert.Equal(t, []textEdit{
		{Role: gateway.MarketerRole, MessageID: 301, Text: planningGreeting},
		{Role: gateway.EditorRole, MessageID: 302, Text: sourcesGreeting},
	}, sender.textEdits)
	assert.Empty(t, sender.pins, "pinned already")
	assert.Empty(t, tech.reports)
}

// TestHandlerOnboardPinsAfterFailedPin: a greeting whose pin failed is edited and pinned by the next /onboard.
func TestHandlerOnboardPinsAfterFailedPin(t *testing.T) {
	t.Parallel()

	sender := &fakeSender{nextID: 300, pinErr: gateway.ErrNoRights}
	store := &fakePilot{}
	h := onboardHandler(sender, store, &fakeTech{})

	require.NoError(t, h.HandleEvent(t.Context(), commandEvent(gateway.OnboardCommand, "")))
	assert.False(t, store.greetings[string(gateway.PlanningTopic)].Pinned)

	sender.texts, sender.pins, sender.pinErr = nil, nil, nil

	require.NoError(t, h.HandleEvent(t.Context(), commandEvent(gateway.OnboardCommand, "")))

	assert.Len(t, sender.texts, 1, "only the answer: the greetings are edited")
	assert.Equal(t, []pinCall{{Role: gateway.MarketerRole, MessageID: 301}, {Role: gateway.MarketerRole, MessageID: 302}},
		sender.pins)
	assert.True(t, store.greetings[string(gateway.PlanningTopic)].Pinned)
}

func TestHandlerOnboardDeletedGreeting(t *testing.T) {
	t.Parallel()

	sender := &fakeSender{nextID: 400, editTextErr: gateway.ErrMessageNotFound}
	store := &fakePilot{greetings: map[string]pilot.OnboardingMessage{
		string(gateway.PlanningTopic): {
			ChatID: testChatID, Topic: string(gateway.PlanningTopic), TopicID: 2, Role: string(gateway.MarketerRole),
			MessageID: 10, Pinned: true,
		},
		// «Исходники» moved to another forum topic in the config: the greeting is sent anew.
		string(gateway.SourcesTopic): {
			ChatID: testChatID, Topic: string(gateway.SourcesTopic), TopicID: 99, Role: string(gateway.EditorRole),
			MessageID: 11, Pinned: true,
		},
	}}
	h := onboardHandler(sender, store, &fakeTech{})

	require.NoError(t, h.HandleEvent(t.Context(), commandEvent(gateway.OnboardCommand, "")))

	assert.Equal(t, []pinCall{
		{Role: gateway.MarketerRole, MessageID: 10, Unpin: true}, {Role: gateway.MarketerRole, MessageID: 401},
		{Role: gateway.MarketerRole, MessageID: 11, Unpin: true}, {Role: gateway.MarketerRole, MessageID: 402},
	}, sender.pins, "the new greetings replace the old pins")
	assert.Equal(t, []textEdit{{Role: gateway.MarketerRole, MessageID: 10, Text: planningGreeting}}, sender.textEdits)
	assert.Equal(t, int64(402), store.greetings[string(gateway.SourcesTopic)].MessageID)
	assert.Equal(t, int64(3), store.greetings[string(gateway.SourcesTopic)].TopicID)
}

func TestHandlerOnboardRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		sender          *fakeSender
		admins          []int64
		event           *gateway.Event
		expectedText    string
		expectedReports []string
	}{
		{
			name:         "not an admin",
			admins:       []int64{testUserID + 1},
			event:        commandEvent(gateway.OnboardCommand, ""),
			expectedText: onboardNotAllowedText,
		},
		{
			name:         "forwarded",
			event:        forwarded(commandEvent(gateway.OnboardCommand, "")),
			expectedText: onboardForwardedText,
		},
		{
			name:   "no right to pin: told to the admin and to «Техничка»",
			sender: &fakeSender{pinErr: gateway.ErrNoRights},
			event:  commandEvent(gateway.OnboardCommand, ""),
			expectedText: "Не получилось закрепить в «Планёрка», «Исходники»: у бота Маркетолога нет права закреплять " +
				"сообщения. Дайте ему это право в настройках администраторов группы и повторите /onboard.",
			expectedReports: []string{"Онбординг: нет права закреплять сообщения в «Планёрка», «Исходники» (нужно у " +
				"бота Маркетолога: «Закреплять сообщения»)."},
		},
		{
			name:         "sending failed",
			sender:       &fakeSender{sendErr: errTelegramDown},
			event:        commandEvent(gateway.OnboardCommand, ""),
			expectedText: "Не получилось обновить приветствие в «Планёрка», «Исходники». Повторите /onboard чуть позже.",
			expectedReports: []string{"Онбординг: не удалось обновить приветствие в «Планёрка», «Исходники», " +
				"подробности в логе app."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sender, tech := tt.sender, &fakeTech{}
			if sender == nil {
				sender = &fakeSender{}
			}

			h := onboardHandler(sender, &fakePilot{}, tech, tt.admins...)

			err := h.HandleEvent(t.Context(), tt.event)
			if tt.sender != nil && tt.sender.sendErr != nil {
				require.ErrorIs(t, err, errTelegramDown, "the answer itself cannot be sent either")
			} else {
				require.NoError(t, err)
			}

			require.NotEmpty(t, sender.texts)
			assert.Equal(t, reply(gateway.MarketerRole, tt.expectedText), sender.texts[len(sender.texts)-1])
			assert.Equal(t, tt.expectedReports, tech.reports)
		})
	}
}

func TestHandlerHelp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		event    *gateway.Event
		expected []sentText
	}{
		{
			name:     "help in «Планёрка», from the marketer even when addressed to another bot",
			event:    withTarget(commandEvent(gateway.HelpCommand, ""), gateway.EditorRole),
			expected: []sentText{reply(gateway.MarketerRole, helpText(gateway.PlanningTopic))},
		},
		{
			name:     "start is help",
			event:    inTopic(commandEvent(gateway.StartCommand, ""), gateway.SourcesTopic),
			expected: []sentText{reply(gateway.MarketerRole, helpText(gateway.SourcesTopic))},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls, err := handle(t, nil, nil, tt.event)

			require.NoError(t, err)
			assert.Equal(t, tt.expected, calls.texts)
		})
	}
}

func TestHandlerStatusWithMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		store    *fakePilot
		expected string
	}{
		{
			name: "the metrics follow the status",
			store: &fakePilot{metrics: pilot.Metrics{Feedback: 2, Agents: []pilot.AgentMetrics{{
				Agent: editorAgent, Created: 4, Approved: 2, ApprovedFirst: 1, Revisions: 2, Rejected: 1,
				FirstDrafts: 3, MedianFirstDraft: 20 * time.Minute, MaxFirstDraft: 75 * time.Minute,
			}}}},
			expected: "Задачи:\n— в очереди: 1\n\nРасход LLM за 7 дней: $0.00, вызовов: 0\n\n" +
				"Итоги пилота за 7 дней (задачи, созданные за это время):\n" +
				"— Монтажёр: задач 4, одобрено 2 (с первой версии 1 — 50%, правок до одобрения в среднем 1), " +
				"отклонено 1, сбоев 0. Первый черновик: медиана 20 мин, максимум 1 ч 15 мин.\nОтзывов: 2.",
		},
		{
			name:     "metrics failed: the status still answers",
			store:    &fakePilot{metErr: errDatabaseDown},
			expected: "Задачи:\n— в очереди: 1\n\nРасход LLM за 7 дней: $0.00, вызовов: 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			service := &fakeService{report: tasks.StatusReport{Tasks: []tasks.StatusCount{{Status: tasks.QueuedStatus, Count: 1}}}}
			sender := &fakeSender{}
			h := newPilotHandler(service, sender, tt.store, &fakeTech{})
			h.now = func() time.Time { return testNow }

			require.NoError(t, h.HandleEvent(t.Context(), commandEvent(gateway.StatusCommand, "")))
			assert.Equal(t, []sentText{reply(gateway.MarketerRole, tt.expected)}, sender.texts)
			assert.Equal(t, []time.Time{testNow.AddDate(0, 0, -7)}, tt.store.since)
		})
	}
}

func TestFormatAgentMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    pilot.AgentMetrics
		expected string
	}{
		{
			name:     "nothing approved: no breakdown; the draft time only for the editor",
			input:    pilot.AgentMetrics{Agent: string(gateway.CopywriterRole), Created: 2, Rejected: 1, FirstDrafts: 2, MedianFirstDraft: time.Minute},
			expected: "Копирайтер: задач 2, одобрено 0, отклонено 1, сбоев 0.",
		},
		{
			name:     "average revisions with a decimal",
			input:    pilot.AgentMetrics{Agent: string(gateway.MarketerRole), Created: 2, Approved: 2, Revisions: 3},
			expected: "Маркетолог: задач 2, одобрено 2 (с первой версии 0 — 0%, правок до одобрения в среднем 1,5), отклонено 0, сбоев 0.",
		},
		{
			name:  "editor drafts faster than a minute",
			input: pilot.AgentMetrics{Agent: editorAgent, Created: 1, Approved: 1, ApprovedFirst: 1, FirstDrafts: 1, MedianFirstDraft: 30 * time.Second, MaxFirstDraft: 2 * time.Hour},
			expected: "Монтажёр: задач 1, одобрено 1 (с первой версии 1 — 100%, правок до одобрения в среднем 0), " +
				"отклонено 0, сбоев 0. Первый черновик: медиана меньше минуты, максимум 2 ч.",
		},
		{name: "unknown agent by its name", input: pilot.AgentMetrics{Agent: "seo", Created: 1}, expected: "seo: задач 1, одобрено 0, отклонено 0, сбоев 0."},
		{
			name:     "idea cards apart, with failures",
			input:    pilot.AgentMetrics{Agent: pilot.IdeasGroup, Created: 3, Failed: 2},
			expected: "Идеи (/idea): задач 3, одобрено 0, отклонено 0, сбоев 2.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, formatAgentMetrics(&tt.input))
		})
	}
}

// TestFormatWeeklyReportFitsOneMessage: the longest week — every group with big numbers and the feedback read at most,
// each at the cut length — is one Telegram message; the feedback that does not fit is counted.
func TestFormatWeeklyReportFitsOneMessage(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 11, 16, 7, 0, 0, 0, time.UTC)
	agents := make([]pilot.AgentMetrics, 0, len(agentTitles))

	for agent := range agentTitles {
		agents = append(agents, pilot.AgentMetrics{
			Agent: agent, Created: 9999, Approved: 9999, ApprovedFirst: 9999, Revisions: 99999, Rejected: 9999,
			Failed: 9999, FirstDrafts: 9999, MedianFirstDraft: 999 * time.Hour, MaxFirstDraft: 999 * time.Hour,
		})
	}

	feedback := make([]pilot.Feedback, 0, weeklyFeedbackLimit)
	for range weeklyFeedbackLimit {
		feedback = append(feedback, pilot.Feedback{
			AuthorName: strings.Repeat("Ж", 64), Text: strings.Repeat("ё", 2000), TaskID: 99999, TaskVersion: 99,
			CreatedAt: since,
		})
	}

	metrics := pilot.Metrics{Since: since, Until: since.AddDate(0, 0, 7), Agents: agents, Feedback: 999}
	text := formatWeeklyReport(&metrics, feedback, 999, moscow)

	assert.LessOrEqual(t, utf16Len(text), maxMessageLength)
	assert.Contains(t, text, "\n…и ещё ")
}

func TestUnknownAdmins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		admins   []int64
		allowed  []int64
		expected []int64
	}{
		{name: "no admins", allowed: []int64{1}},
		{name: "admins are allowed", admins: []int64{1}, allowed: []int64{1, 2}},
		{name: "an admin the gateway ignores", admins: []int64{1, 3}, allowed: []int64{1, 2}, expected: []int64{3}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, unknownAdmins(tt.admins, tt.allowed))
		})
	}
}

func TestFormatWeeklyReport(t *testing.T) {
	t.Parallel()

	since := time.Date(2026, 11, 16, 7, 0, 0, 0, time.UTC) // 10:00 in Moscow
	metrics := pilot.Metrics{Since: since, Until: since.AddDate(0, 0, 7), Feedback: 3}
	feedback := []pilot.Feedback{
		{AuthorName: testUserName, Text: greatFeedback, CreatedAt: since.Add(time.Hour)},
		{AuthorName: testUserName, Text: strings.Repeat("б", weeklyFeedbackLength+1), TaskID: 12, TaskVersion: 2, CreatedAt: since.Add(48 * time.Hour)},
	}

	tests := []struct {
		name     string
		feedback []pilot.Feedback
		total    int
		expected string
	}{
		{
			name: "no feedback", total: 0,
			expected: "Сводка пилота за неделю 16.11–22.11 (задачи, созданные за неделю):\n— задач не было.\nОтзывов: 3." +
				"\n\nОтзывов за неделю не было.",
		},
		{
			name: "feedback cut, the rest counted", feedback: feedback, total: 3,
			expected: "Сводка пилота за неделю 16.11–22.11 (задачи, созданные за неделю):\n— задач не было.\nОтзывов: 3." +
				"\n\nОтзывы тренера:\n— 16.11, Анастасия: Монтаж отличный\n— 18.11, Анастасия, задача 12 (версия 2): " +
				strings.Repeat("б", weeklyFeedbackLength) + "…\n…и ещё 1.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, formatWeeklyReport(&metrics, tt.feedback, tt.total, moscow))
		})
	}
}
