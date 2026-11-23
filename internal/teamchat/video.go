package teamchat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/tasks"
)

const (
	// Reels are rendered 1080×1920: Telegram shows the video in its frame without probing the file.
	reelWidth  = 1080
	reelHeight = 1920
	// outboxMode: the outbox is the app's own (the gateway creates it 0700 on start).
	outboxMode = 0o700
	// supergroupPrefix: a supergroup ID is -100<channel id>; its message links use the channel id.
	supergroupPrefix = "-100"
	sourceLinkLabel  = "Исходник: "
)

var errNoVideoSender = errors.New("video drafts are not configured")

type videoSender interface {
	SendMedia(ctx context.Context, role gateway.Role, media *gateway.OutgoingMedia) (int64, error)
}

// videoDrafts sends the editor's reels: the video is downloaded from S3 into TELEGRAM_OUTBOX_DIR (the gateway sends
// files only from there), sent as a video (it plays right in the chat; Telegram keeps the file as it is, so «Save to
// gallery» gives the rendered reel) and removed from the outbox.
//
// The draft goes to «Продакшн» when TELEGRAM_TOPIC_PRODUCTION_ID is set, otherwise to the topic of the source with a
// reply to it. A forum ties a reply to the topic of the replied message, so a reply across topics is not relied on:
// a draft in «Продакшн» links its source message in «Исходники» instead.
type videoDrafts struct {
	sender            videoSender
	files             mediaStore
	outboxDir         string
	maxFileSize       int64
	productionTopicID int64
}

// topic is where the video drafts of a task from the source topic go.
func (v *videoDrafts) topic(sourceTopicID int64) int64 {
	if v == nil || v.productionTopicID == 0 {
		return sourceTopicID
	}

	return v.productionTopicID
}

func (n *Notifier) sendVideoDraft(ctx context.Context, draft *tasks.Draft) (tasks.SentDraft, error) {
	v := n.videos
	if v == nil {
		return tasks.SentDraft{}, fmt.Errorf("failed to send video draft: %w", errNoVideoSender)
	}

	if err := os.MkdirAll(v.outboxDir, outboxMode); err != nil {
		return tasks.SentDraft{}, fmt.Errorf("failed to prepare the outbox: %w", err)
	}

	local, err := v.files.Download(ctx, draft.MediaKey, v.outboxDir, v.maxFileSize)
	if err != nil {
		return tasks.SentDraft{}, fmt.Errorf("failed to get the reel: %w", err)
	}

	defer func() {
		if err := local.Remove(); err != nil {
			n.log.ErrorContext(ctx, "teamchat: failed to remove the reel from the outbox", "error", err, "task_id", draft.TaskID)
		}
	}()

	topicID, replyTo := v.topic(draft.TopicID), draft.ReplyToMessageID
	caption := draftText(draft)

	if topicID != draft.TopicID {
		replyTo = 0

		if link := messageLink(draft.ChatID, draft.TopicID, draft.ReplyToMessageID); link != "" {
			caption += paragraphBreak + sourceLinkLabel + link
		}
	}

	id, err := v.sender.SendMedia(ctx, roleOf(draft.Agent), &gateway.OutgoingMedia{
		Type:    gateway.VideoMediaType,
		Path:    local.Path,
		TopicID: topicID,
		ReplyTo: replyTo,
		Caption: caption,
		Buttons: buttonRows(draft.Buttons),
		Width:   reelWidth,
		Height:  reelHeight,
	})
	if err != nil {
		return tasks.SentDraft{}, fmt.Errorf("failed to send video draft: %w", err)
	}

	return tasks.SentDraft{MessageID: id}, nil
}

// messageLink is the link to a message of a private supergroup (https://t.me/c/<channel id>/<topic>/<message>);
// "" for a chat that is not a supergroup.
func messageLink(chatID, topicID, messageID int64) string {
	channel, ok := strings.CutPrefix(strconv.FormatInt(chatID, 10), supergroupPrefix)
	if !ok || channel == "" || messageID <= 0 {
		return ""
	}

	link := "https://t.me/c/" + channel + "/"
	if topicID > 0 {
		link += strconv.FormatInt(topicID, 10) + "/"
	}

	return link + strconv.FormatInt(messageID, 10)
}
