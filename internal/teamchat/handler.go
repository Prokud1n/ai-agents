// Package teamchat connects the Telegram gateway with the tasks. Handler turns events of the team group into
// tasks and decisions on drafts; Notifier sends drafts, decisions and failures back to the group from the bot
// of the agent that made the draft. Agent names are the gateway roles (marketer, copywriter, editor, designer).
package teamchat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

// takenReaction marks a message that became a task: the trainer sees it was taken, and the topic gets no
// extra message before the draft.
const takenReaction = "👀"

// Task kinds of the team commands; a plain message has no kind.
const (
	PlanTaskKind = "plan"
	IdeaTaskKind = "idea"
)

type taskService interface {
	Create(ctx context.Context, data *tasks.CreateTaskData) (tasks.Task, bool, error)
	Approve(ctx context.Context, data *tasks.ApproveTaskData) (tasks.Task, error)
	Reject(ctx context.Context, data *tasks.RejectTaskData) (tasks.Task, error)
	RequestRevisionByReply(ctx context.Context, data *tasks.RequestRevisionByReplyData) (tasks.Task, error)
	StatusReport(ctx context.Context, since time.Time) (tasks.StatusReport, error)
}

type chatSender interface {
	SendText(ctx context.Context, role gateway.Role, msg *gateway.OutgoingMessage) (int64, error)
	AnswerCallback(ctx context.Context, role gateway.Role, callbackID, text string) error
	React(ctx context.Context, role gateway.Role, messageID int64, emoji string) error
}

// Handler is the gateway.Handler of the team: it only records work (a task, a decision, a revision) and
// answers; agents run in the task workers. It is idempotent: a task is keyed by the source message, and
// a repeated decision is refused by the task service.
//
//   - a text message creates a task for the target role (the topic of the message gets the draft);
//     a forwarded text goes to the agent as data, not as an instruction;
//   - a reply to the latest draft is a revision of it; a reply to any other message is a new task;
//   - draft buttons: «Одобрить» and «Отклонить» decide, «Правки» asks to reply to the draft with the revision;
//   - /status, /plan, /idea, /pause are the marketer's;
//   - voice messages, videos and photos are not processed yet: the trainer gets an honest answer.
//
// Errors of the task service reach the trainer as tasks.UserMessage; details go to the log only.
type Handler struct {
	tasks  taskService
	sender chatSender
	log    *slog.Logger
	now    func() time.Time
}

func NewHandler(service taskService, sender chatSender, log *slog.Logger) *Handler {
	return &Handler{tasks: service, sender: sender, log: log, now: time.Now}
}

func (h *Handler) HandleEvent(ctx context.Context, event *gateway.Event) error {
	switch event.Type {
	case gateway.CallbackEventType:
		return h.handleCallback(ctx, event)
	case gateway.CommandEventType:
		return h.handleCommand(ctx, event)
	default:
		return h.handleMessage(ctx, event)
	}
}

func (h *Handler) handleMessage(ctx context.Context, event *gateway.Event) error {
	if event.Media != nil {
		return h.reply(ctx, event.Target, event, mediaText(event.Media.Type, event.Text))
	}

	if event.ReplyToMessageID != 0 {
		handled, err := h.revise(ctx, event)
		if handled || err != nil {
			return err
		}
	}

	return h.createTask(ctx, event, &taskRequest{agent: event.Target, input: messageInput(event)})
}

// revise takes a reply as a revision of the draft it answers. It reports false when the replied message is
// not a draft: then the reply is a new task.
func (h *Handler) revise(ctx context.Context, event *gateway.Event) (bool, error) {
	_, err := h.tasks.RequestRevisionByReply(ctx, &tasks.RequestRevisionByReplyData{
		ChatID:           event.ChatID,
		ReplyToMessageID: event.ReplyToMessageID,
		UserID:           event.From.ID,
		Text:             revisionText(event),
	})

	switch {
	case err == nil:
		// The draft loses its buttons and gets «Принял правку» from the notifier: no answer here.
		return true, nil
	case errors.Is(err, tasks.ErrNotFound):
		return false, nil
	default:
		return true, h.replyError(ctx, event.Target, event, "request revision", err)
	}
}

type taskRequest struct {
	agent gateway.Role
	kind  string
	input string
}

func (h *Handler) createTask(ctx context.Context, event *gateway.Event, req *taskRequest) error {
	task, created, err := h.tasks.Create(ctx, &tasks.CreateTaskData{
		Agent:    string(req.agent),
		Kind:     req.kind,
		Input:    req.input,
		Source:   tasks.Source{ChatID: event.ChatID, TopicID: event.TopicID, MessageID: event.MessageID},
		AuthorID: event.From.ID,
	})
	if err != nil {
		return h.replyError(ctx, req.agent, event, "create task", err)
	}

	if !created {
		h.log.InfoContext(ctx, "teamchat: task for the message already exists", "task_id", task.ID, "event", event)

		return nil
	}

	// The bot that received the message has surely seen it; a failed reaction does not matter.
	if err := h.sender.React(ctx, event.ReceivedBy, event.MessageID, takenReaction); err != nil {
		h.log.WarnContext(ctx, "teamchat: failed to react to the task message", "task_id", task.ID, "error", err)
	}

	return nil
}

// handleCommand: team commands are the marketer's, even when addressed to another bot.
func (h *Handler) handleCommand(ctx context.Context, event *gateway.Event) error {
	switch event.Command {
	case gateway.StatusCommand:
		return h.status(ctx, event)
	case gateway.PlanCommand:
		return h.createTask(ctx, event, &taskRequest{
			agent: gateway.MarketerRole, kind: PlanTaskKind, input: commandInput(event, planTask),
		})
	case gateway.IdeaCommand:
		if strings.TrimSpace(event.Text) == "" {
			return h.reply(ctx, gateway.MarketerRole, event, emptyIdeaText)
		}

		return h.createTask(ctx, event, &taskRequest{
			agent: gateway.MarketerRole, kind: IdeaTaskKind, input: commandInput(event, ideaTask),
		})
	case gateway.PauseCommand:
		return h.reply(ctx, gateway.MarketerRole, event, pauseText)
	default:
		return h.reply(ctx, gateway.MarketerRole, event, unknownCommandText)
	}
}

func (h *Handler) status(ctx context.Context, event *gateway.Event) error {
	report, err := h.tasks.StatusReport(ctx, h.now().Add(-statusPeriod))
	if err != nil {
		return h.replyError(ctx, gateway.MarketerRole, event, "status report", err)
	}

	return h.reply(ctx, gateway.MarketerRole, event, formatStatus(&report))
}

// handleCallback applies a draft button. The answer goes from the bot that received the press (the owner of
// the button); Telegram takes one answer per press.
func (h *Handler) handleCallback(ctx context.Context, event *gateway.Event) error {
	cb, err := tasks.ParseCallback(event.Callback.Data)
	if err != nil {
		// The error repeats the callback data: only the event (without its content) is logged.
		h.log.InfoContext(ctx, "teamchat: callback is not a task button", "event", event)

		return h.answer(ctx, event, tasks.UserMessage(err))
	}

	text, err := h.decide(ctx, event, cb)
	if err != nil {
		h.logFailure(ctx, "decide on draft", event, err)

		text = tasks.UserMessage(err)
	}

	return h.answer(ctx, event, text)
}

// decide applies the pressed action and returns the answer to the press.
func (h *Handler) decide(ctx context.Context, event *gateway.Event, cb tasks.Callback) (string, error) {
	switch cb.Action {
	case tasks.ApproveAction:
		_, err := h.tasks.Approve(ctx, &tasks.ApproveTaskData{TaskID: cb.TaskID, Version: cb.Version, UserID: event.From.ID})

		return approvedAnswer, err
	case tasks.RejectAction:
		_, err := h.tasks.Reject(ctx, &tasks.RejectTaskData{TaskID: cb.TaskID, Version: cb.Version, UserID: event.From.ID})

		return rejectedAnswer, err
	default: // tasks.ReviseAction: the revision comes as a reply to the draft
		return reviseHint, nil
	}
}

// reply answers the event's message in its topic.
func (h *Handler) reply(ctx context.Context, role gateway.Role, event *gateway.Event, text string) error {
	if _, err := h.sender.SendText(ctx, role, &gateway.OutgoingMessage{
		TopicID: event.TopicID,
		ReplyTo: event.MessageID,
		Text:    text,
	}); err != nil {
		return fmt.Errorf("failed to reply to the team chat: %w", err)
	}

	return nil
}

// replyError logs the error and answers the trainer with its class: internal details stay in the log.
func (h *Handler) replyError(ctx context.Context, role gateway.Role, event *gateway.Event, action string, err error) error {
	h.logFailure(ctx, action, event, err)

	return h.reply(ctx, role, event, tasks.UserMessage(err))
}

func (h *Handler) answer(ctx context.Context, event *gateway.Event, text string) error {
	if err := h.sender.AnswerCallback(ctx, event.ReceivedBy, event.Callback.ID, text); err != nil {
		return fmt.Errorf("failed to answer the button press: %w", err)
	}

	return nil
}

// logFailure: a refused action (stale draft, repeated press, validation) is expected, anything else is an error.
func (h *Handler) logFailure(ctx context.Context, action string, event *gateway.Event, err error) {
	if refused(err) {
		h.log.InfoContext(ctx, "teamchat: action refused", "action", action, "event", event, "error", err)

		return
	}

	h.log.ErrorContext(ctx, "teamchat: action failed", "action", action, "event", event, "error", err)
}

func refused(err error) bool {
	return errors.Is(err, tasks.ErrValidation) || errors.Is(err, tasks.ErrConflict) ||
		errors.Is(err, tasks.ErrStaleVersion) || errors.Is(err, tasks.ErrNotFound)
}
