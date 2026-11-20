package gateway

import (
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/Prokud1n/ai-agents/internal/providers/telegram"
)

// Reasons an update is ignored; logged at debug level, without the content.
const (
	privateChatReason       = "private chat"
	foreignChatReason       = "foreign chat"
	userNotAllowedReason    = "user not allowed"
	senderChatReason        = "sent on behalf of a chat"
	botSenderReason         = "sent by a bot"
	notDispatcherReason     = "message for a non-dispatcher bot"
	noContentReason         = "no text or supported media"
	foreignCommandReason    = "command for another bot"
	unsupportedUpdateReason = "unsupported update"
)

const (
	videoMIMEPrefix = "video/"
	imageMIMEPrefix = "image/"
	videoNoteMIME   = "video/mp4"
	photoMIME       = "image/jpeg"
)

// identity is what getMe tells about a bot.
type identity struct {
	ID       int64
	Username string
}

// routing is the static part of the routing rules (from config).
type routing struct {
	GroupID        int64
	AllowedUserIDs []int64
	Topics         map[int64]Topic // message_thread_id → named topic
	MaxFileSize    int64
}

// router turns updates into events: filter, routing to a role, command and media parsing. No I/O. Bot
// identities are added as bots connect, so it is safe for concurrent use.
type router struct {
	groupID     int64
	allowed     map[int64]struct{}
	topics      map[int64]Topic
	maxFileSize int64

	mu         sync.RWMutex
	byID       map[int64]Role
	byUsername map[string]Role // lower case: usernames are case-insensitive
}

func newRouter(r *routing, identities map[Role]identity) *router {
	rt := &router{
		groupID:     r.GroupID,
		allowed:     make(map[int64]struct{}, len(r.AllowedUserIDs)),
		topics:      r.Topics,
		maxFileSize: r.MaxFileSize,
		byID:        make(map[int64]Role, len(identities)),
		byUsername:  make(map[string]Role, len(identities)),
	}

	for _, id := range r.AllowedUserIDs {
		rt.allowed[id] = struct{}{}
	}

	for role, ident := range identities {
		rt.addIdentity(role, ident)
	}

	return rt
}

// addIdentity registers a connected bot for @mentions and replies.
func (r *router) addIdentity(role Role, ident identity) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.byID[ident.ID] = role
	if ident.Username != "" {
		r.byUsername[strings.ToLower(ident.Username)] = role
	}
}

func (r *router) roleByID(id int64) (Role, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	role, ok := r.byID[id]

	return role, ok
}

func (r *router) roleByUsername(username string) (Role, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	role, ok := r.byUsername[strings.ToLower(username)]

	return role, ok
}

// event returns the event of an update received by the receiver bot, or the reason to ignore it.
func (r *router) event(receiver Role, u *telegram.Update) (event *Event, ignoreReason string) {
	switch {
	case u.Message != nil:
		if receiver != dispatcherRole {
			return nil, notDispatcherReason
		}

		return r.messageEvent(receiver, u.UpdateID, u.Message)
	case u.CallbackQuery != nil:
		return r.callbackEvent(receiver, u.UpdateID, u.CallbackQuery)
	default:
		return nil, unsupportedUpdateReason
	}
}

func (r *router) messageEvent(
	receiver Role,
	updateID int64,
	msg *telegram.Message,
) (event *Event, ignoreReason string) {
	// An anonymous admin or a linked channel: the real author is unknown.
	if msg.SenderChat != nil {
		return nil, senderChatReason
	}

	if reason := r.filter(&msg.Chat, msg.From); reason != "" {
		return nil, reason
	}

	text, entities := content(msg)
	media := r.extractMedia(msg)

	if strings.TrimSpace(text) == "" && media == nil {
		return nil, noContentReason
	}

	topicID, topic := r.topicOf(msg)

	event = &Event{
		Type:             MessageEventType,
		UpdateID:         updateID,
		ReceivedBy:       receiver,
		ChatID:           msg.Chat.ID,
		TopicID:          topicID,
		Topic:            topic,
		MessageID:        msg.MessageID,
		ReplyToMessageID: replyToID(msg),
		From:             toUser(msg.From),
		Text:             text,
		Forwarded:        msg.ForwardOrigin != nil,
		Media:            media,
		Date:             time.Unix(msg.Date, 0).UTC(),
	}

	if role, ok := r.repliedRole(msg); ok {
		event.ReplyToRole = role
	}

	cmd, botName, args, isCommand := parseCommand(text, entities)
	event.IsCommand = isCommand

	if isCommand && botName != "" {
		role, ours := r.roleByUsername(botName)
		if !ours {
			return nil, foreignCommandReason
		}

		event.Target = role
	}

	if isCommand && isTeamCommand(cmd) {
		event.Type = CommandEventType
		event.Command = cmd
		event.Text = args

		// Team commands (plan, ideas, status, pause) belong to the marketer unless addressed to a bot.
		if event.Target == "" {
			event.Target = MarketerRole
		}
	}

	if event.Target == "" {
		event.Target = r.target(msg, entities, text, topic)
	}

	return event, ""
}

func (r *router) callbackEvent(
	receiver Role,
	updateID int64,
	cb *telegram.CallbackQuery,
) (event *Event, ignoreReason string) {
	if cb.Message == nil {
		return nil, foreignChatReason
	}

	if reason := r.filter(&cb.Message.Chat, &cb.From); reason != "" {
		return nil, reason
	}

	topicID, topic := r.topicOf(cb.Message)

	return &Event{
		Type:       CallbackEventType,
		UpdateID:   updateID,
		ReceivedBy: receiver,
		// Buttons belong to the bot that sent the message, so the callback is that role's.
		Target:    receiver,
		ChatID:    cb.Message.Chat.ID,
		TopicID:   topicID,
		Topic:     topic,
		MessageID: cb.Message.MessageID,
		From:      toUser(&cb.From),
		Callback:  &Callback{ID: cb.ID, Data: cb.Data},
	}, ""
}

// filter allows only the team group and users from the allowlist.
func (r *router) filter(chat *telegram.Chat, from *telegram.User) string {
	if chat.Type == telegram.PrivateChat {
		return privateChatReason
	}

	if chat.ID != r.groupID {
		return foreignChatReason
	}

	if from == nil {
		return userNotAllowedReason
	}

	if from.IsBot {
		return botSenderReason
	}

	if _, ok := r.allowed[from.ID]; !ok {
		return userNotAllowedReason
	}

	return ""
}

func (r *router) topicOf(msg *telegram.Message) (int64, Topic) {
	if !msg.IsTopicMessage || msg.MessageThreadID == 0 {
		return 0, OtherTopic
	}

	if topic, ok := r.topics[msg.MessageThreadID]; ok {
		return msg.MessageThreadID, topic
	}

	return msg.MessageThreadID, OtherTopic
}

// target: an explicit @mention of a bot, then a reply to a bot's message, then the topic.
func (r *router) target(msg *telegram.Message, entities []telegram.MessageEntity, text string, topic Topic) Role {
	if role, ok := r.mentionedRole(text, entities); ok {
		return role
	}

	if role, ok := r.repliedRole(msg); ok {
		return role
	}

	if topic == SourcesTopic {
		return EditorRole
	}

	return MarketerRole
}

func (r *router) mentionedRole(text string, entities []telegram.MessageEntity) (Role, bool) {
	for _, e := range entities {
		switch e.Type {
		case telegram.MentionEntity:
			name := strings.TrimPrefix(entityText(text, e.Offset, e.Length), "@")
			if role, ok := r.roleByUsername(name); ok {
				return role, true
			}
		case telegram.TextMentionEntity:
			if e.User == nil {
				continue
			}

			if role, ok := r.roleByID(e.User.ID); ok {
				return role, true
			}
		}
	}

	return "", false
}

func (r *router) repliedRole(msg *telegram.Message) (Role, bool) {
	if replyToID(msg) == 0 || msg.ReplyToMessage.From == nil {
		return "", false
	}

	return r.roleByID(msg.ReplyToMessage.From.ID)
}

// replyToID returns the ID of the message replied to. In a forum topic every message that is not
// a reply carries the topic's service message as reply_to_message: that is not a reply.
func replyToID(msg *telegram.Message) int64 {
	reply := msg.ReplyToMessage
	if reply == nil || reply.ForumTopicCreated != nil {
		return 0
	}

	if msg.IsTopicMessage && reply.MessageID == msg.MessageThreadID {
		return 0
	}

	return reply.MessageID
}

func (r *router) extractMedia(msg *telegram.Message) *Media {
	media := mediaOf(msg)
	if media != nil && media.Size > r.maxFileSize {
		media.TooLarge = true
	}

	return media
}

func mediaOf(msg *telegram.Message) *Media {
	switch {
	case msg.Voice != nil:
		v := msg.Voice

		return &Media{Type: VoiceMediaType, FileID: v.FileID, FileUniqueID: v.FileUniqueID, Size: v.FileSize,
			Duration: seconds(v.Duration), MIMEType: v.MIMEType}
	case msg.Audio != nil:
		a := msg.Audio

		return &Media{Type: AudioMediaType, FileID: a.FileID, FileUniqueID: a.FileUniqueID, Size: a.FileSize,
			Duration: seconds(a.Duration), MIMEType: a.MIMEType, FileName: a.FileName}
	case msg.Video != nil:
		v := msg.Video

		return &Media{Type: VideoMediaType, FileID: v.FileID, FileUniqueID: v.FileUniqueID, Size: v.FileSize,
			Duration: seconds(v.Duration), MIMEType: v.MIMEType, FileName: v.FileName}
	case msg.VideoNote != nil:
		v := msg.VideoNote

		return &Media{Type: VideoNoteMediaType, FileID: v.FileID, FileUniqueID: v.FileUniqueID, Size: v.FileSize,
			Duration: seconds(v.Duration), MIMEType: videoNoteMIME}
	case msg.Document != nil && strings.HasPrefix(strings.ToLower(msg.Document.MIMEType), videoMIMEPrefix):
		d := msg.Document

		return &Media{Type: VideoMediaType, FileID: d.FileID, FileUniqueID: d.FileUniqueID, Size: d.FileSize,
			MIMEType: d.MIMEType, FileName: d.FileName}
	case msg.Document != nil && strings.HasPrefix(strings.ToLower(msg.Document.MIMEType), imageMIMEPrefix):
		// An image sent as a file keeps its quality: for the designer.
		d := msg.Document

		return &Media{Type: PhotoMediaType, FileID: d.FileID, FileUniqueID: d.FileUniqueID, Size: d.FileSize,
			MIMEType: d.MIMEType, FileName: d.FileName}
	case len(msg.Photo) > 0:
		// Sizes go from the smallest to the largest.
		p := msg.Photo[len(msg.Photo)-1]

		return &Media{Type: PhotoMediaType, FileID: p.FileID, FileUniqueID: p.FileUniqueID, Size: p.FileSize,
			MIMEType: photoMIME}
	default:
		return nil
	}
}

// content returns the text with its entities, or the caption of a media message.
func content(msg *telegram.Message) (string, []telegram.MessageEntity) {
	if msg.Text != "" {
		return msg.Text, msg.Entities
	}

	return msg.Caption, msg.CaptionEntities
}

// parseCommand reads a command at the start of the text: "/plan@MarketerBot week" →
// ("plan", "MarketerBot", "week", true).
func parseCommand(
	text string,
	entities []telegram.MessageEntity,
) (cmd Command, botName, args string, ok bool) {
	for _, e := range entities {
		if e.Type != telegram.BotCommandEntity || e.Offset != 0 {
			continue
		}

		raw := strings.TrimPrefix(entityText(text, 0, e.Length), "/")
		name, mentioned, _ := strings.Cut(raw, "@")

		return Command(strings.ToLower(name)), mentioned, strings.TrimSpace(textAfter(text, e.Length)), true
	}

	return "", "", "", false
}

func isTeamCommand(cmd Command) bool {
	switch cmd {
	case PlanCommand, IdeaCommand, StatusCommand, PauseCommand:
		return true
	default:
		return false
	}
}

// entityText cuts an entity out of the text; offsets are in UTF-16 code units.
func entityText(text string, offset, length int) string {
	units := utf16.Encode([]rune(text))
	if offset < 0 || length < 0 || offset+length > len(units) {
		return ""
	}

	return string(utf16.Decode(units[offset : offset+length]))
}

// textAfter returns the text after offset UTF-16 code units.
func textAfter(text string, offset int) string {
	units := utf16.Encode([]rune(text))
	if offset < 0 || offset > len(units) {
		return ""
	}

	return string(utf16.Decode(units[offset:]))
}

func toUser(u *telegram.User) User {
	if u == nil {
		return User{}
	}

	return User{ID: u.ID, Username: u.Username, FirstName: u.FirstName}
}

func seconds(n int) time.Duration {
	return time.Duration(n) * time.Second
}
