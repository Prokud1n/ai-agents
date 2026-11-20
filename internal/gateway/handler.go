package gateway

import (
	"context"
	"fmt"
	"log/slog"
)

const bytesInMB = 1 << 20

// Handler receives every accepted event. Contract:
//   - fast: record the work (a task, keyed by chat_id+message_id or the callback ID) and return; anything heavy
//     (LLM calls, editing video) goes to a queue. Events run concurrently, TELEGRAM_WORKERS at a time, callbacks
//     in their own pool, so a slow handler delays other events;
//   - idempotent: the same message may come again after a restart or a Redis failure;
//   - delivery is at most once: on an error or a panic the event's mark in Redis is removed (and a callback is
//     answered without text), but the update is delivered again only if its offset was not saved yet.
//
// The default is AckHandler. Replace it with fx.Decorate (or fx.Replace) of gateway.Handler in the application
// options.
type Handler interface {
	HandleEvent(ctx context.Context, event *Event) error
}

// MediaFilter is optional for a Handler: the gateway downloads the file of an event only when WantsMedia
// returns true (a handler that does not process files yet answers at once, without waiting for a download).
// A Handler without it gets every file downloaded.
type MediaFilter interface {
	WantsMedia(event *Event) bool
}

func wantsMedia(h Handler, event *Event) bool {
	filter, ok := h.(MediaFilter)

	return !ok || filter.WantsMedia(event)
}

// replySender is the part of Sender the acknowledging handler needs.
type replySender interface {
	SendText(ctx context.Context, role Role, msg *OutgoingMessage) (int64, error)
	AnswerCallback(ctx context.Context, role Role, callbackID, text string) error
}

// AckHandler is the default handler of the gateway module alone (the application uses teamchat.Handler):
// it logs the event and answers in the same topic from the target bot, so the gateway can be checked by hand.
type AckHandler struct {
	sender replySender
	log    *slog.Logger
}

func NewAckHandler(sender *Sender, log *slog.Logger) *AckHandler {
	return &AckHandler{sender: sender, log: log}
}

func (h *AckHandler) HandleEvent(ctx context.Context, event *Event) error {
	h.log.InfoContext(ctx, "telegram gateway: event received", "event", event)

	if event.Type == CallbackEventType {
		if err := h.sender.AnswerCallback(ctx, event.ReceivedBy, event.Callback.ID, "Принято"); err != nil {
			return fmt.Errorf("failed to answer callback: %w", err)
		}

		return nil
	}

	if _, err := h.sender.SendText(ctx, event.Target, &OutgoingMessage{
		TopicID: event.TopicID,
		ReplyTo: event.MessageID,
		Text:    ackText(event),
	}); err != nil {
		return fmt.Errorf("failed to send acknowledgement: %w", err)
	}

	return nil
}

// ackText never includes the user's text or file paths.
func ackText(event *Event) string {
	const queueNote = "Задача появится, когда подключим очередь задач."

	if event.Type == CommandEventType {
		return fmt.Sprintf("Команда /%s принята. %s", event.Command, queueNote)
	}

	media := event.Media
	if media == nil {
		return "Принял. " + queueNote
	}

	switch {
	case media.TooLarge:
		return fmt.Sprintf("Файл (%s, %.1f МБ) больше лимита, скачать его не могу.", media.Type, megabytes(media.Size))
	case !media.Downloaded():
		return fmt.Sprintf("Не смог скачать файл (%s), попробуй отправить ещё раз.", media.Type)
	default:
		return fmt.Sprintf("Принял файл (%s, %.1f МБ). %s", media.Type, megabytes(media.Size), queueNote)
	}
}

func megabytes(size int64) float64 {
	return float64(size) / bytesInMB
}
