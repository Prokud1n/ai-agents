package teamchat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/providers/telegram"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

// notModifiedError is the Bot API answer to an edit that changes nothing (the buttons are already removed).
const notModifiedError = "message is not modified"

// Texts of a decided draft.
const (
	approvedText    = "Одобрено."
	rejectedText    = "Отклонено."
	revisionFormat  = "Принял правку, готовлю версию %d."
	versionHeader   = "Версия "
	failureFallback = "Не получилось выполнить задачу."
)

type draftSender interface {
	SendText(ctx context.Context, role gateway.Role, msg *gateway.OutgoingMessage) (int64, error)
	SendTextParts(ctx context.Context, role gateway.Role, msg *gateway.OutgoingMessage) ([]int64, error)
	EditButtons(ctx context.Context, role gateway.Role, messageID int64, buttons [][]gateway.Button) error
}

// Notifier is tasks.Notifier over the gateway: drafts, decisions and failures go to the topic of the task from
// the bot of its agent. Texts are plain: Sender sends them without a parse mode.
type Notifier struct {
	sender draftSender
	log    *slog.Logger
}

func NewNotifier(sender draftSender, log *slog.Logger) *Notifier {
	return &Notifier{sender: sender, log: log}
}

// SendDraft sends the draft as an answer to the task message, with the buttons «Одобрить / Правки / Отклонить».
// A long draft goes in several messages with the buttons on the last one; all their IDs are returned, so a
// reply to any part is a revision. If a part fails, the whole send fails: the parts sent before it come back in
// PartIDs with the error (a reply to them gets «reply to the latest draft»), and the next delivery attempt
// sends every part again.
func (n *Notifier) SendDraft(ctx context.Context, draft *tasks.Draft) (tasks.SentDraft, error) {
	ids, err := n.sender.SendTextParts(ctx, roleOf(draft.Agent), &gateway.OutgoingMessage{
		TopicID: draft.TopicID,
		ReplyTo: draft.ReplyToMessageID,
		Text:    draftText(draft),
		Buttons: buttonRows(draft.Buttons),
	})
	if err != nil {
		// The parts sent before the failure: the task service keeps them as old messages of the version.
		return tasks.SentDraft{PartIDs: ids}, fmt.Errorf("failed to send draft: %w", err)
	}

	if len(ids) == 0 {
		return tasks.SentDraft{}, nil
	}

	last := len(ids) - 1
	sent := tasks.SentDraft{MessageID: ids[last]}

	if last > 0 {
		sent.PartIDs = slices.Clone(ids[:last])
	}

	return sent, nil
}

// CloseDraft removes the buttons of the decided draft and answers it with the decision; a duplicate draft only
// loses its buttons. The decision is sent even if the buttons could not be removed.
func (n *Notifier) CloseDraft(ctx context.Context, data *tasks.CloseDraftData) error {
	role := roleOf(data.Agent)

	var errs []error

	if err := n.sender.EditButtons(ctx, role, data.MessageID, nil); err != nil && !notModified(err) {
		errs = append(errs, fmt.Errorf("failed to remove draft buttons: %w", err))
	}

	text := decisionText(data)
	if data.Duplicate || text == "" {
		return errors.Join(errs...)
	}

	if _, err := n.sender.SendText(ctx, role, &gateway.OutgoingMessage{
		TopicID: data.TopicID,
		ReplyTo: data.MessageID,
		Text:    text,
	}); err != nil {
		errs = append(errs, fmt.Errorf("failed to send draft decision: %w", err))
	}

	return errors.Join(errs...)
}

// NotifyFailure answers the task message with the reason for the trainer (never internal details).
func (n *Notifier) NotifyFailure(ctx context.Context, failure *tasks.Failure) error {
	text := strings.TrimSpace(failure.Message)
	if text == "" {
		text = failureFallback
	}

	if _, err := n.sender.SendText(ctx, roleOf(failure.Agent), &gateway.OutgoingMessage{
		TopicID: failure.TopicID,
		ReplyTo: failure.ReplyToMessageID,
		Text:    text,
	}); err != nil {
		return fmt.Errorf("failed to send task failure: %w", err)
	}

	n.log.InfoContext(ctx, "teamchat: told the trainer about a failed task", "task_id", failure.TaskID)

	return nil
}

// roleOf is the bot of the agent; an agent without its own bot writes from the marketer.
func roleOf(agent string) gateway.Role {
	role := gateway.Role(agent)
	if slices.Contains(gateway.Roles, role) {
		return role
	}

	return gateway.MarketerRole
}

// draftText: a revised version is headed with its number, a note (why a revision failed) goes above the draft.
func draftText(draft *tasks.Draft) string {
	parts := make([]string, 0, 3)

	if draft.Version > 1 {
		parts = append(parts, versionHeader+strconv.Itoa(draft.Version))
	}

	if note := strings.TrimSpace(draft.Note); note != "" {
		parts = append(parts, note)
	}

	parts = append(parts, draft.Text)

	return strings.Join(parts, paragraphBreak)
}

// buttonRows puts the draft buttons in one row.
func buttonRows(buttons []tasks.Button) [][]gateway.Button {
	if len(buttons) == 0 {
		return nil
	}

	row := make([]gateway.Button, 0, len(buttons))
	for _, b := range buttons {
		row = append(row, gateway.Button{Text: b.Text, Data: b.CallbackData})
	}

	return [][]gateway.Button{row}
}

func decisionText(data *tasks.CloseDraftData) string {
	switch data.Decision {
	case tasks.ApproveAction:
		return approvedText
	case tasks.RejectAction:
		return rejectedText
	case tasks.ReviseAction:
		return fmt.Sprintf(revisionFormat, data.Version+1)
	default:
		return ""
	}
}

func notModified(err error) bool {
	var apiErr *telegram.APIError

	return errors.As(err, &apiErr) && apiErr.Code == http.StatusBadRequest &&
		strings.Contains(apiErr.Description, notModifiedError)
}
