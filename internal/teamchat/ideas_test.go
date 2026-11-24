package teamchat

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/ideas"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const testIdeaID = int64(7)

var testIdeaDate = time.Date(2026, 11, 24, 21, 30, 0, 0, time.UTC)

type ideaCalls struct {
	handler handlerCalls
	saved   []int64
	closed  []closeCall
	limits  []int
}

// handleIdeas runs one event through a handler with the backlog fake.
func handleIdeas(t *testing.T, service *fakeService, backlog *fakeIdeas, event *gateway.Event) ideaCalls {
	t.Helper()

	if service == nil {
		service = &fakeService{}
	}

	sender := &fakeSender{}
	h := NewHandler(service, sender, &fakeStore{}, slog.New(slog.DiscardHandler))
	h.ideas = backlog

	require.NoError(t, h.HandleEvent(t.Context(), event))
	require.NoError(t, h.WaitBackground(t.Context()))

	return ideaCalls{
		handler: handlerCalls{
			created: service.created, approved: service.approved, rejected: service.rejected,
			texts: sender.texts, answers: sender.answers, reactions: sender.reactions,
		},
		saved:  backlog.saved,
		closed: backlog.closed,
		limits: backlog.limits,
	}
}

func testIdea(id int64, title string) ideas.Idea {
	return ideas.Idea{ID: id, Card: ideas.Card{Title: title}, Status: ideas.NewStatus, CreatedAt: testIdeaDate}
}

func TestHandlerApproveIdea(t *testing.T) {
	t.Parallel()

	approve := tasks.EncodeCallback(tasks.Callback{TaskID: testTaskID, Version: 2, Action: tasks.ApproveAction})
	approved := []tasks.ApproveTaskData{{TaskID: testTaskID, Version: 2, UserID: testUserID}}
	answer := func(text string) []callbackAnswer {
		return []callbackAnswer{{Role: gateway.MarketerRole, ID: testCallbackID, Text: text}}
	}

	tests := []struct {
		name     string
		service  *fakeService
		backlog  *fakeIdeas
		expected ideaCalls
	}{
		{
			name:    "approved card goes to the backlog, its number is told",
			service: &fakeService{draftKind: IdeaTaskKind},
			backlog: &fakeIdeas{idea: testIdea(testIdeaID, "Зарядка")},
			expected: ideaCalls{
				handler: handlerCalls{
					approved: approved,
					// The number is told in the topic, as an answer to the draft message of the button.
					texts: []sentText{{Role: gateway.MarketerRole, Msg: gateway.OutgoingMessage{
						TopicID: testTopicID, ReplyTo: testDraftID,
						Text: "Идея №7 в бэклоге. Список — /ideas, отметить сделанной — /idea_done 7, убрать — /idea_drop 7.",
					}}},
					answers: answer("Идея №7 в бэклоге"),
				},
				saved: []int64{testTaskID},
			},
		},
		{
			name:    "repeated press on an approved card: no second idea, no second message",
			service: &fakeService{draftKind: IdeaTaskKind, draftStatus: tasks.ApprovedStatus, approveErr: tasks.ErrConflict},
			backlog: &fakeIdeas{idea: testIdea(testIdeaID, "Зарядка"), existed: true},
			expected: ideaCalls{
				handler: handlerCalls{approved: approved, answers: answer("Идея №7 в бэклоге")},
				saved:   []int64{testTaskID},
			},
		},
		{
			name:    "rejected card cannot be approved",
			service: &fakeService{draftKind: IdeaTaskKind, draftStatus: tasks.RejectedStatus, approveErr: tasks.ErrConflict},
			backlog: &fakeIdeas{},
			expected: ideaCalls{handler: handlerCalls{
				approved: approved, answers: answer("Задача уже обработана или сейчас в работе."),
			}},
		},
		{
			name:    "old version is not saved",
			service: &fakeService{draftKind: IdeaTaskKind, approveErr: tasks.ErrStaleVersion},
			backlog: &fakeIdeas{},
			expected: ideaCalls{handler: handlerCalls{
				approved: approved, answers: answer("Это устаревшая версия черновика: действуйте по последней."),
			}},
		},
		{
			name:    "save failed after the approval",
			service: &fakeService{draftKind: IdeaTaskKind},
			backlog: &fakeIdeas{saveErr: errDatabaseDown},
			expected: ideaCalls{
				handler: handlerCalls{approved: approved, answers: answer(ideaSaveFailedAnswer)},
				saved:   []int64{testTaskID},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			calls := handleIdeas(t, tt.service, tt.backlog, callbackEvent(gateway.MarketerRole, approve))
			assert.Equal(t, tt.expected, calls)
		})
	}
}

func TestHandlerIdeaCommands(t *testing.T) {
	t.Parallel()

	closedDone := ideas.Idea{ID: 3, Status: ideas.DoneStatus}
	closedDropped := ideas.Idea{ID: 3, Status: ideas.DroppedStatus}

	tests := []struct {
		name     string
		backlog  *fakeIdeas
		event    *gateway.Event
		expected ideaCalls
	}{
		{
			name:    "ideas: the backlog",
			backlog: &fakeIdeas{list: []ideas.Idea{testIdea(2, "Сон и восстановление")}, total: 1},
			event:   withTarget(commandEvent(gateway.IdeasCommand, ""), gateway.CopywriterRole),
			expected: ideaCalls{
				handler: handlerCalls{texts: []sentText{reply(gateway.MarketerRole, ideasHeader+
					"\n№2 — Сон и восстановление, 24.11\n\n"+ideasFooter)}},
				limits: []int{ideasListLimit},
			},
		},
		{
			name:    "ideas: the backlog cannot be read",
			backlog: &fakeIdeas{listErr: errDatabaseDown},
			event:   commandEvent(gateway.IdeasCommand, ""),
			expected: ideaCalls{
				handler: handlerCalls{texts: []sentText{reply(gateway.MarketerRole, ideasListFailedText)}},
				limits:  []int{ideasListLimit},
			},
		},
		{
			name:    "idea done",
			backlog: &fakeIdeas{idea: testIdea(0, "Зарядка")},
			event:   commandEvent(gateway.IdeaDoneCommand, "5"),
			expected: ideaCalls{
				handler: handlerCalls{texts: []sentText{reply(gateway.MarketerRole, "Идея №5 «Зарядка» отмечена сделанной.")}},
				closed:  []closeCall{{ID: 5, Status: ideas.DoneStatus}},
			},
		},
		{
			name:    "idea drop with a number sign",
			backlog: &fakeIdeas{idea: testIdea(0, "Зарядка")},
			event:   commandEvent(gateway.IdeaDropCommand, "№5"),
			expected: ideaCalls{
				handler: handlerCalls{texts: []sentText{reply(gateway.MarketerRole, "Идея №5 «Зарядка» убрана из бэклога.")}},
				closed:  []closeCall{{ID: 5, Status: ideas.DroppedStatus}},
			},
		},
		{
			name:    "no number",
			backlog: &fakeIdeas{},
			event:   commandEvent(gateway.IdeaDoneCommand, ""),
			expected: ideaCalls{handler: handlerCalls{texts: []sentText{reply(gateway.MarketerRole,
				"Укажите номер идеи: /idea_done 5. Номера — в /ideas.")}}},
		},
		{
			name:    "not a number",
			backlog: &fakeIdeas{},
			event:   commandEvent(gateway.IdeaDropCommand, "зарядка"),
			expected: ideaCalls{handler: handlerCalls{texts: []sentText{reply(gateway.MarketerRole,
				"Укажите номер идеи: /idea_drop 5. Номера — в /ideas.")}}},
		},
		{
			name:    "unknown number",
			backlog: &fakeIdeas{closeErr: ideas.ErrNotFound},
			event:   commandEvent(gateway.IdeaDoneCommand, "42"),
			expected: ideaCalls{
				handler: handlerCalls{texts: []sentText{reply(gateway.MarketerRole, "Идеи №42 нет. Номера — в /ideas.")}},
				closed:  []closeCall{{ID: 42, Status: ideas.DoneStatus}},
			},
		},
		{
			name:    "already done",
			backlog: &fakeIdeas{closeErr: ideas.ErrClosed, closeIdea: &closedDone},
			event:   commandEvent(gateway.IdeaDropCommand, "3"),
			expected: ideaCalls{
				handler: handlerCalls{texts: []sentText{reply(gateway.MarketerRole, "Идея №3 уже отмечена сделанной.")}},
				closed:  []closeCall{{ID: 3, Status: ideas.DroppedStatus}},
			},
		},
		{
			name:    "already dropped",
			backlog: &fakeIdeas{closeErr: ideas.ErrClosed, closeIdea: &closedDropped},
			event:   commandEvent(gateway.IdeaDoneCommand, "3"),
			expected: ideaCalls{
				handler: handlerCalls{texts: []sentText{reply(gateway.MarketerRole, "Идея №3 уже убрана из бэклога.")}},
				closed:  []closeCall{{ID: 3, Status: ideas.DoneStatus}},
			},
		},
		{
			name:    "backlog cannot be changed",
			backlog: &fakeIdeas{closeErr: errDatabaseDown},
			event:   commandEvent(gateway.IdeaDoneCommand, "3"),
			expected: ideaCalls{
				handler: handlerCalls{texts: []sentText{reply(gateway.MarketerRole, ideaCloseFailedText)}},
				closed:  []closeCall{{ID: 3, Status: ideas.DoneStatus}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, handleIdeas(t, nil, tt.backlog, tt.event))
		})
	}
}

func TestFormatIdeas(t *testing.T) {
	t.Parallel()

	list := make([]ideas.Idea, 0, 2)
	list = append(list, testIdea(9, "Планка без ошибок"), testIdea(8, "Завтрак до тренировки"))

	tests := []struct {
		name     string
		list     []ideas.Idea
		total    int
		expected string
	}{
		{name: "empty", expected: ideasEmptyText},
		{
			name: "all shown", list: list, total: 2,
			expected: ideasHeader + "\n№9 — Планка без ошибок, 24.11\n№8 — Завтрак до тренировки, 24.11\n\n" + ideasFooter,
		},
		{
			name: "more than the limit", list: list, total: 5,
			expected: ideasHeader + "\n№9 — Планка без ошибок, 24.11\n№8 — Завтрак до тренировки, 24.11\n…и ещё 3.\n\n" +
				ideasFooter,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, formatIdeas(tt.list, tt.total))
		})
	}
}

func TestParseIdeaNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected int64
		ok       bool
	}{
		{name: "number", input: "5", expected: 5, ok: true},
		{name: "spaces", input: "  12 ", expected: 12, ok: true},
		{name: "number sign", input: "№7", expected: 7, ok: true},
		{name: "hash", input: "#7", expected: 7, ok: true},
		{name: "empty", input: ""},
		{name: "zero", input: "0"},
		{name: "negative", input: "-3"},
		{name: "two numbers", input: "3 4"},
		{name: "text", input: "зарядка"},
		{name: "too large", input: strings.Repeat("9", 30)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			id, ok := parseIdeaNumber(tt.input)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.expected, id)
		})
	}
}
