package teamchat

import (
	"context"
	"errors"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/pilot"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

// feedbackInput is the text of a /feedback, cut to maxFeedbackLength.
type feedbackInput struct {
	text      string
	truncated bool
}

// parseFeedback reads a /feedback; refusal is the answer when it is not taken: a forwarded message is someone else's
// words, an empty one gets a hint.
func parseFeedback(event *gateway.Event) (input feedbackInput, refusal string) {
	if event.Forwarded {
		return feedbackInput{}, forwardedFeedbackText
	}

	text := strings.TrimSpace(event.Text)
	if text == "" {
		return feedbackInput{}, emptyFeedbackText
	}

	runes := []rune(text)
	if len(runes) <= maxFeedbackLength {
		return feedbackInput{text: text}, ""
	}

	return feedbackInput{text: strings.TrimSpace(string(runes[:maxFeedbackLength])), truncated: true}, ""
}

// feedback saves the trainer's /feedback, copies it to «Техничка» and thanks her. A reply to a draft links the feedback
// to its task and version. A redelivered message is saved once and answered once.
func (h *Handler) feedback(ctx context.Context, event *gateway.Event) error {
	input, refusal := parseFeedback(event)
	if refusal != "" {
		return h.reply(ctx, gateway.MarketerRole, event, refusal)
	}

	data := &pilot.SaveFeedbackData{
		AuthorID:   event.From.ID,
		AuthorName: authorName(event.From),
		Text:       input.text,
		Truncated:  input.truncated,
		Topic:      string(event.Topic),
		TopicID:    event.TopicID,
		ChatID:     event.ChatID,
		MessageID:  event.MessageID,
	}
	data.TaskID, data.TaskVersion = h.feedbackDraft(ctx, event)

	fb, created, err := h.pilot.SaveFeedback(ctx, data)
	if err != nil {
		h.log.ErrorContext(ctx, "teamchat: failed to save feedback", "event", event, "error", err)

		return h.reply(ctx, gateway.MarketerRole, event, feedbackFailedText)
	}

	if !created {
		h.log.InfoContext(ctx, "teamchat: feedback of the message is saved already", "feedback_id", fb.ID, "event", event)

		return nil
	}

	h.log.InfoContext(ctx, "teamchat: feedback saved", "feedback_id", fb.ID, "task_id", fb.TaskID)

	if err := h.tech.Report(ctx, string(gateway.MarketerRole), feedbackTechText(&fb)); err != nil {
		h.log.WarnContext(ctx, "teamchat: failed to copy feedback to the tech topic", "feedback_id", fb.ID, "error", err)
	}

	return h.reply(ctx, gateway.MarketerRole, event, feedbackThanks(fb.Truncated))
}

// feedbackDraft is the task and version of the draft the feedback replies to; zeros when it does not reply to a draft
// (or to an old one).
func (h *Handler) feedbackDraft(ctx context.Context, event *gateway.Event) (taskID int64, version int) {
	if event.ReplyToMessageID == 0 {
		return 0, 0
	}

	task, v, err := h.tasks.FindByDraftMessage(ctx, event.ChatID, event.ReplyToMessageID)
	if err != nil {
		if !errors.Is(err, tasks.ErrNotFound) && !errors.Is(err, tasks.ErrStaleVersion) {
			h.log.WarnContext(ctx, "teamchat: failed to find the draft of the feedback", "event", event, "error", err)
		}

		return 0, 0
	}

	return task.ID, v.Version
}
