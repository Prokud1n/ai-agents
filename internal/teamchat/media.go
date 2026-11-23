package teamchat

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/media"
)

type mediaStore interface {
	Save(ctx context.Context, f *media.File, r io.ReadSeeker) (string, error)
}

// WantsMedia: the gateway downloads only a video for the editor (topic «Исходники» or a mention of the editor's
// bot), the one file the team chat stores. Voice messages, video notes, photos, audio files and videos for other
// roles are answered at once, without a download.
func (*Handler) WantsMedia(event *gateway.Event) bool {
	return event.Type == gateway.MessageEventType && event.Media != nil && mediaRefusal(event) == ""
}

// handleMedia stores a video for the editor and answers; any other file gets an honest answer. The file is copied
// here, in the event's slot, not in the background: it is already on this machine (the gateway's download from
// Telegram, the slow part, was made in this slot too), the Bot API server may delete it later, and the trainer is
// told «stored» only once it is safe in S3. A large video holds one of TELEGRAM_WORKERS slots for the copy
// (bounded by MEDIA_UPLOAD_TIMEOUT); button presses have their own slots.
func (h *Handler) handleMedia(ctx context.Context, event *gateway.Event) error {
	if refusal := mediaRefusal(event); refusal != "" {
		h.log.InfoContext(ctx, "teamchat: file not accepted", "event", event)

		return h.reply(ctx, event.Target, event, refusal)
	}

	if !event.Media.Downloaded() { // the gateway logged why
		return h.reply(ctx, event.Target, event, downloadFailedText)
	}

	key, err := h.saveFile(ctx, event)
	if err != nil {
		h.log.ErrorContext(ctx, "teamchat: failed to store file", "event", event, "error", err)

		return h.reply(ctx, event.Target, event, fileSaveFailedText)
	}

	h.log.InfoContext(ctx, "teamchat: source video stored", "event", event, "key", key)

	text := videoStoredText
	if strings.TrimSpace(event.Text) != "" {
		text += videoCaptionNote
	}

	return h.reply(ctx, event.Target, event, text)
}

// mediaRefusal is the answer to a file that will not be stored, "" for a video that will. Videos are the editor's
// (the «Исходники» topic or a mention of the editor's bot); voice messages are not processed on purpose: the
// trainer dictates text with the phone's dictation instead.
func mediaRefusal(event *gateway.Event) string {
	m := event.Media

	switch m.Type {
	case gateway.VideoMediaType:
		if event.Target != gateway.EditorRole {
			return videoToEditorText
		}

		if m.TooLarge {
			return fileTooLargeText(m.Size)
		}

		return ""
	case gateway.VoiceMediaType, gateway.VideoNoteMediaType:
		return voiceText
	default:
		return mediaText(m.Type, event.Text)
	}
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
