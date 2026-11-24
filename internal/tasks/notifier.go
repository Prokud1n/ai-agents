package tasks

import (
	"context"
	"log/slog"
)

// Notifier delivers drafts and failures to the trainer. The Telegram gateway implements it (each agent
// writes from its own bot: teamchat.Notifier); LogNotifier stands in for it when the gateway is off.
type Notifier interface {
	// SendDraft sends the draft with buttons from the agent's bot and returns its messages (none if the
	// notifier has no messages). It fails as a whole: when a part of a long draft is not sent, the error comes
	// with the parts already sent in PartIDs (they become old messages of the version), and the next delivery
	// attempt sends all parts again.
	SendDraft(ctx context.Context, draft *Draft) (SentDraft, error)
	// CloseDraft marks a decided draft: removes its buttons and shows the decision.
	CloseDraft(ctx context.Context, data *CloseDraftData) error
	// NotifyFailure tells the trainer that the task failed.
	NotifyFailure(ctx context.Context, failure *Failure) error
}

// Draft is a version to send for approval to the topic of the source message.
type Draft struct {
	TaskID  int64
	Version int
	Agent   string // the agent's bot sends the draft
	// Kind is the task kind: the notifier may label the buttons of a kind its own way (an idea goes «В бэклог»).
	Kind    string
	ChatID  int64
	TopicID int64
	// ReplyToMessageID is the source message of the task.
	ReplyToMessageID int64
	Text             string
	// MediaKey is the S3 key of the video of a video draft (Text is then its description); empty for a text draft.
	MediaKey string
	// Note is shown above the draft when not empty, e.g. why a revision could not be applied.
	Note    string
	Buttons []Button
}

// SentDraft is the messages of a sent draft.
type SentDraft struct {
	// MessageID is the message with the buttons (the last part of a long draft); 0 if the notifier has no
	// messages.
	MessageID int64
	// PartIDs are the earlier parts of a long draft: a reply to any of them is a revision too.
	PartIDs []int64
}

// CloseDraftData is a decided draft message.
type CloseDraftData struct {
	TaskID  int64
	Version int
	Agent   string
	// Kind is the task kind, as in Draft.
	Kind   string
	ChatID int64
	// TopicID is the topic of the draft (the task's source topic), for a message about the decision.
	TopicID   int64
	MessageID int64
	// Decision is the pressed action: approve, revise or reject; empty for a duplicate.
	Decision Action
	// Duplicate: the message repeats a draft already sent by another sender; remove its buttons.
	Duplicate bool
	// Video: the draft is a video (it may have been sent to another topic than the task's source).
	Video bool
}

// Failure is a task that failed; Message is for the trainer and never has internal error details.
type Failure struct {
	TaskID           int64
	Agent            string
	ChatID           int64
	TopicID          int64
	ReplyToMessageID int64
	Message          string
}

// LogNotifier only logs: the app runs without Telegram.
type LogNotifier struct {
	log *slog.Logger
}

func NewLogNotifier(log *slog.Logger) *LogNotifier {
	return &LogNotifier{log: log}
}

func (n *LogNotifier) SendDraft(ctx context.Context, draft *Draft) (SentDraft, error) {
	n.log.InfoContext(ctx, "tasks: draft is ready (log notifier)",
		"task_id", draft.TaskID, "version", draft.Version, "agent", draft.Agent, "chars", len([]rune(draft.Text)),
		"media_key", draft.MediaKey, "note", draft.Note)

	return SentDraft{}, nil
}

func (n *LogNotifier) CloseDraft(ctx context.Context, data *CloseDraftData) error {
	n.log.InfoContext(ctx, "tasks: draft decided (log notifier)",
		"task_id", data.TaskID, "version", data.Version, "decision", data.Decision, "duplicate", data.Duplicate)

	return nil
}

func (n *LogNotifier) NotifyFailure(ctx context.Context, failure *Failure) error {
	n.log.WarnContext(ctx, "tasks: task failed (log notifier)",
		"task_id", failure.TaskID, "agent", failure.Agent, "message", failure.Message)

	return nil
}
