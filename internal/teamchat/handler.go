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
	"sync"
	"time"

	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	// takenReaction marks a message that became a task: the trainer sees it was taken, and the topic gets no
	// extra message before the draft.
	takenReaction = "👀"
	// reactionTimeout: the reaction is set in the background and is not worth retrying for long.
	reactionTimeout = 3 * time.Second
)

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
	FindByDraftMessage(ctx context.Context, chatID, messageID int64) (tasks.Task, tasks.Version, error)
	GetVersion(ctx context.Context, taskID int64, version int) (tasks.Version, error)
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
//     a forwarded text goes to the agent as data, not as an instruction; the editor and the designer are not
//     connected yet and answer with a fixed text, without an agent run;
//   - a reply to any part of the latest draft is a revision of it (once per reply message); a reply while the
//     new version is being made, to an old or a decided draft gets an explanation; a reply to any other
//     message is a new task;
//   - draft buttons: «Одобрить» and «Отклонить» decide, «Правки» asks to reply to the draft with the revision;
//     a button counts only on the draft message of its task and version;
//   - /status, /plan, /idea, /pause are the marketer's; any other command gets the list of commands;
//   - voice messages, videos and photos are not processed yet: the trainer gets an honest answer, and the
//     gateway does not download the file (WantsMedia).
//
// Errors of the task service reach the trainer as tasks.UserMessage; details go to the log only.
type Handler struct {
	tasks  taskService
	sender chatSender
	log    *slog.Logger
	now    func() time.Time
	// background tracks reactions being set; see WaitBackground.
	background sync.WaitGroup
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

// WantsMedia: files are not processed yet, so the gateway answers at once instead of downloading them.
func (*Handler) WantsMedia(*gateway.Event) bool {
	return false
}

// WaitBackground waits for reactions being set, at most until ctx is done.
func (h *Handler) WaitBackground(ctx context.Context) error {
	done := make(chan struct{})

	go func() {
		h.background.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("failed to wait for team chat background work: %w", ctx.Err())
	}
}

func (h *Handler) handleMessage(ctx context.Context, event *gateway.Event) error {
	if event.Media != nil {
		return h.reply(ctx, event.Target, event, mediaText(event.Media.Type, event.Text))
	}

	// A command (a bot_command entity at the start, not just a slash: «/2 подхода убрать» is a revision) that
	// the gateway did not pass as a team command; before the stubs, so /help in «Исходники» gets the list.
	if event.IsCommand && !event.Forwarded {
		return h.reply(ctx, gateway.MarketerRole, event, unknownCommandText)
	}

	if text, ok := stubTexts[event.Target]; ok {
		return h.reply(ctx, event.Target, event, text)
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
		MessageID:        event.MessageID,
		TopicID:          event.TopicID,
		Agent:            string(event.ReplyToRole),
		UserID:           event.From.ID,
		Text:             revisionText(event),
	})

	switch {
	case err == nil:
		// The draft loses its buttons and gets «Принял правку» from the notifier; a redelivered reply changes
		// nothing. No answer here.
		return true, nil
	case errors.Is(err, tasks.ErrNotFound):
		return false, nil
	case errors.Is(err, tasks.ErrConflict):
		return true, h.replyConflict(ctx, event, err)
	default:
		return true, h.replyError(ctx, event.Target, event, "request revision", err)
	}
}

// replyConflict explains why a reply to the latest draft is not a revision: by the status of its task.
func (h *Handler) replyConflict(ctx context.Context, event *gateway.Event, conflict error) error {
	h.logFailure(ctx, "request revision", event, conflict)

	text := tasks.UserMessage(conflict)

	task, _, err := h.tasks.FindByDraftMessage(ctx, event.ChatID, event.ReplyToMessageID)
	if err != nil {
		h.log.WarnContext(ctx, "teamchat: failed to find the task of the replied draft", "event", event, "error", err)
	} else if explained, ok := revisionRefusedTexts[task.Status]; ok {
		text = explained
	}

	return h.reply(ctx, event.Target, event, text)
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

	h.markTaken(ctx, event, task.ID)

	return nil
}

// markTaken sets the reaction in the background with a short timeout, so it does not hold a handler slot.
// The bot that received the message has surely seen it; a failed reaction (e.g. the group restricts
// reactions) is only a warning.
func (h *Handler) markTaken(ctx context.Context, event *gateway.Event, taskID int64) {
	bgCtx := context.WithoutCancel(ctx)
	role, messageID := event.ReceivedBy, event.MessageID

	h.background.Go(func() {
		ctx, cancel := context.WithTimeout(bgCtx, reactionTimeout)
		defer cancel()

		if err := h.sender.React(ctx, role, messageID, takenReaction); err != nil {
			h.log.WarnContext(ctx, "teamchat: failed to react to the task message", "task_id", taskID, "error", err)
		}
	})
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
// the button) right after the decision is stored; the task service closes the draft in the background.
// Telegram takes one answer per press.
func (h *Handler) handleCallback(ctx context.Context, event *gateway.Event) error {
	cb, err := tasks.ParseCallback(event.Callback.Data)
	if err != nil {
		// The error repeats the callback data: only the event (without its content) is logged.
		h.log.InfoContext(ctx, "teamchat: callback is not a task button", "event", event)

		return h.answer(ctx, event, tasks.UserMessage(err))
	}

	if err := h.checkButton(ctx, event, cb); err != nil {
		h.logFailure(ctx, "check button", event, err)

		if errors.Is(err, errDraftSaving) {
			return h.answer(ctx, event, draftSavingAnswer)
		}

		return h.answer(ctx, event, tasks.UserMessage(err))
	}

	text, err := h.decide(ctx, event, cb)
	if err != nil {
		h.logFailure(ctx, "decide on draft", event, err)

		text = tasks.UserMessage(err)
	}

	return h.answer(ctx, event, text)
}

// checkButton: a button counts only on the draft message of its task and version, so callback data put on
// another message cannot decide a task. A press that comes before the draft's messages are stored (they are
// stored right after Telegram answers) is errDraftSaving.
func (h *Handler) checkButton(ctx context.Context, event *gateway.Event, cb tasks.Callback) error {
	task, v, err := h.tasks.FindByDraftMessage(ctx, event.ChatID, event.MessageID)

	switch {
	case errors.Is(err, tasks.ErrNotFound) && h.draftNotStored(ctx, cb):
		return fmt.Errorf("draft of task %d version %d: %w", cb.TaskID, cb.Version, errDraftSaving)
	case errors.Is(err, tasks.ErrNotFound) || errors.Is(err, tasks.ErrStaleVersion):
		return fmt.Errorf("message %d is not the draft of the button: %w", event.MessageID, tasks.ErrInvalidCallback)
	case err != nil:
		return fmt.Errorf("failed to find the draft of the button: %w", err)
	case task.ID != cb.TaskID || v.Version != cb.Version:
		return fmt.Errorf("button of task %d version %d on the draft of task %d version %d: %w",
			cb.TaskID, cb.Version, task.ID, v.Version, tasks.ErrInvalidCallback)
	default:
		return nil
	}
}

// draftNotStored reports whether the version of the button exists and its draft messages are not stored yet.
func (h *Handler) draftNotStored(ctx context.Context, cb tasks.Callback) bool {
	v, err := h.tasks.GetVersion(ctx, cb.TaskID, cb.Version)

	return err == nil && v.DraftMessageID == nil
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

// errDraftSaving: a button pressed before the draft's messages are stored.
var errDraftSaving = errors.New("the draft is still being stored")

func refused(err error) bool {
	return errors.Is(err, tasks.ErrValidation) || errors.Is(err, tasks.ErrConflict) ||
		errors.Is(err, tasks.ErrStaleVersion) || errors.Is(err, tasks.ErrNotFound) ||
		errors.Is(err, tasks.ErrDraftNotReady) || errors.Is(err, errDraftSaving)
}
