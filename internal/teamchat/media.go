package teamchat

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/media"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

// mediaReplyTimeout bounds the answer after a copy, which is sent even when the app is stopping.
const mediaReplyTimeout = 10 * time.Second

// mediaStore keeps the trainer's videos (Handler) and gives back the editor's reels (Notifier).
type mediaStore interface {
	Save(ctx context.Context, f *media.File, r io.ReadSeeker) (string, error)
	Download(ctx context.Context, key, dir string, maxSize int64) (*media.LocalFile, error)
}

// WantsMedia: the gateway downloads only a video for the editor (topic «Исходники» or a mention of the editor's
// bot), the one file the team chat stores. Voice messages, video notes, photos, audio files and videos for other
// roles are answered at once, without a download.
func (h *Handler) WantsMedia(event *gateway.Event) bool {
	return event.Type == gateway.MessageEventType && event.Media != nil && h.mediaRefusal(event) == ""
}

// handleMedia stores a video for the editor and makes it the editor's task; any other file gets an honest answer.
// The file is copied here, in the event's slot, not in the background: it is already on this machine (the gateway's
// download from Telegram, the slow part, was made in this slot too), the Bot API server may delete it later, and the
// trainer is told «taken» only once it is safe in S3. A large video holds one of the TELEGRAM_MEDIA_WORKERS slots for
// the copy (bounded by MEDIA_UPLOAD_TIMEOUT); text messages and button presses have slots of their own.
func (h *Handler) handleMedia(ctx context.Context, event *gateway.Event) error {
	if refusal := h.mediaRefusal(event); refusal != "" {
		h.log.InfoContext(ctx, "teamchat: file not accepted", "event", event)

		return h.reply(ctx, event.Target, event, refusal)
	}

	if !event.Media.Downloaded() { // the gateway logged why
		return h.reply(ctx, event.Target, event, downloadFailedText)
	}

	key, err := h.saveFile(ctx, event)

	// The copy may take minutes and end with the stop of the app: the trainer still gets the outcome.
	replyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mediaReplyTimeout)
	defer cancel()

	if err != nil {
		h.log.ErrorContext(ctx, "teamchat: failed to store file", "event", event, "error", err)

		return h.reply(replyCtx, event.Target, event, fileSaveFailedText)
	}

	h.log.InfoContext(ctx, "teamchat: source video stored", "event", event, "key", key)

	return h.createEditTask(replyCtx, event, key)
}

// createEditTask makes the stored video the editor's task; its caption is kept as the task input (shown in the
// draft's description). A redelivered message finds its task and gets no second answer.
func (h *Handler) createEditTask(ctx context.Context, event *gateway.Event, key string) error {
	task, created, err := h.tasks.Create(ctx, &tasks.CreateTaskData{
		Agent:    string(gateway.EditorRole),
		Kind:     ReelsEditTaskKind,
		Input:    strings.TrimSpace(event.Text),
		Media:    []string{key},
		Source:   tasks.Source{ChatID: event.ChatID, TopicID: event.TopicID, MessageID: event.MessageID},
		AuthorID: event.From.ID,
	})
	if err != nil {
		return h.replyError(ctx, gateway.EditorRole, event, "create edit task", err)
	}

	if !created {
		h.log.InfoContext(ctx, "teamchat: task for the video already exists", "task_id", task.ID, "event", event)

		return nil
	}

	text := videoAcceptedHereText
	if h.productionTopicID != 0 && h.productionTopicID != event.TopicID {
		text = videoAcceptedText
	}

	if strings.TrimSpace(event.Text) != "" {
		text += videoCaptionNote
	}

	return h.reply(ctx, gateway.EditorRole, event, text)
}

// mediaRefusal is the answer to a file that will not be stored, "" for a video that will. Videos are the editor's
// (the «Исходники» topic or a mention of the editor's bot); voice messages are not processed on purpose: the
// trainer dictates text with the phone's dictation instead.
func (h *Handler) mediaRefusal(event *gateway.Event) string {
	m := event.Media

	switch m.Type {
	case gateway.VideoMediaType:
		if event.Target != gateway.EditorRole {
			return withCaptionNote(videoForOtherRoleText(event), event.Text)
		}

		if m.TooLarge {
			return fileTooLargeText(h.maxFileSize)
		}

		return ""
	case gateway.VoiceMediaType, gateway.VideoNoteMediaType:
		return voiceText
	default:
		return mediaText(m.Type, event.Text)
	}
}

// videoForOtherRoleText: a video already in «Исходники» went to another bot because of a mention or a reply.
func videoForOtherRoleText(event *gateway.Event) string {
	if event.Topic == gateway.SourcesTopic {
		return videoMentionText
	}

	return videoToEditorText
}

// saveFile copies the downloaded file of the event to S3 and returns its key.
func (h *Handler) saveFile(ctx context.Context, event *gateway.Event) (string, error) {
	m := event.Media

	file, err := m.Open()
	if err != nil {
		return "", fmt.Errorf("failed to open downloaded file: %w", err)
	}
	defer file.Close()

	key, err := h.files.Save(ctx, &media.File{
		ChatID:       event.ChatID,
		MessageID:    event.MessageID,
		FileUniqueID: m.FileUniqueID,
		MIMEType:     m.MIMEType,
		FileName:     m.FileName,
	}, file)
	if err != nil {
		return "", fmt.Errorf("failed to save file: %w", err)
	}

	return key, nil
}
