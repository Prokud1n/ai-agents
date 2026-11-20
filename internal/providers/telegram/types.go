package telegram

import "os"

// Types of the Bot API (https://core.telegram.org/bots/api), only the fields the gateway uses.

// Update types for allowed_updates.
const (
	MessageUpdate       = "message"
	CallbackQueryUpdate = "callback_query"
)

// Message entity types.
const (
	MentionEntity     = "mention"      // @username
	TextMentionEntity = "text_mention" // a user without a username, entity.User is set
	BotCommandEntity  = "bot_command"  // /command or /command@botname
)

// PrivateChat is Chat.Type of a one-to-one chat with a bot.
const PrivateChat = "private"

// EmojiReaction is ReactionType.Type of an emoji reaction.
const EmojiReaction = "emoji"

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username,omitempty"`
}

type Chat struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Title   string `json:"title,omitempty"`
	IsForum bool   `json:"is_forum,omitempty"`
}

type Message struct {
	MessageID int64 `json:"message_id"`
	// MessageThreadID is the forum topic of the message; meaningful only with IsTopicMessage.
	MessageThreadID int64 `json:"message_thread_id,omitempty"`
	IsTopicMessage  bool  `json:"is_topic_message,omitempty"`
	From            *User `json:"from,omitempty"`
	// SenderChat is set when the message is sent on behalf of a chat (anonymous admin, linked channel).
	SenderChat *Chat `json:"sender_chat,omitempty"`
	// ForwardOrigin is set for forwarded messages.
	ForwardOrigin *MessageOrigin `json:"forward_origin,omitempty"`
	Date          int64          `json:"date"`
	Chat          Chat           `json:"chat"`
	// ReplyToMessage in a forum topic is the topic's service message when the message is not a reply.
	ReplyToMessage    *Message        `json:"reply_to_message,omitempty"`
	Text              string          `json:"text,omitempty"`
	Entities          []MessageEntity `json:"entities,omitempty"`
	Caption           string          `json:"caption,omitempty"`
	CaptionEntities   []MessageEntity `json:"caption_entities,omitempty"`
	Voice             *Voice          `json:"voice,omitempty"`
	Audio             *Audio          `json:"audio,omitempty"`
	Video             *Video          `json:"video,omitempty"`
	VideoNote         *VideoNote      `json:"video_note,omitempty"`
	Document          *Document       `json:"document,omitempty"`
	Photo             []PhotoSize     `json:"photo,omitempty"`
	ForumTopicCreated *struct{}       `json:"forum_topic_created,omitempty"`
}

// MessageOrigin is the origin of a forwarded message; only its type is used.
type MessageOrigin struct {
	Type string `json:"type"`
}

// MessageEntity offsets and lengths are in UTF-16 code units.
type MessageEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	User   *User  `json:"user,omitempty"`
}

type Voice struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Duration     int    `json:"duration"`
	MIMEType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type Audio struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Duration     int    `json:"duration"`
	FileName     string `json:"file_name,omitempty"`
	MIMEType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type Video struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	Duration     int    `json:"duration"`
	FileName     string `json:"file_name,omitempty"`
	MIMEType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type VideoNote struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Length       int    `json:"length"`
	Duration     int    `json:"duration"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name,omitempty"`
	MIMEType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type CallbackQuery struct {
	ID   string `json:"id"`
	From User   `json:"from"`
	// Message is the message with the button; nil if it is too old.
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// File is the result of getFile. With the local Bot API server (--local) FilePath is an absolute
// path on the server's disk; it contains the bot token as a directory name, so it must not be logged.
type File struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize     int64  `json:"file_size,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

type ReplyParameters struct {
	MessageID int64 `json:"message_id"`
	// AllowSendingWithoutReply sends the message even if the original one was deleted.
	AllowSendingWithoutReply bool `json:"allow_sending_without_reply,omitempty"`
}

type GetUpdatesParams struct {
	Offset int64 `json:"offset,omitempty"`
	Limit  int   `json:"limit,omitempty"`
	// Timeout of long polling in seconds.
	Timeout        int      `json:"timeout"`
	AllowedUpdates []string `json:"allowed_updates"`
}

type SendMessageParams struct {
	ChatID          int64                 `json:"chat_id"`
	MessageThreadID int64                 `json:"message_thread_id,omitempty"`
	Text            string                `json:"text"`
	ParseMode       string                `json:"parse_mode,omitempty"`
	ReplyParameters *ReplyParameters      `json:"reply_parameters,omitempty"`
	ReplyMarkup     *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// EditMessageTextParams: without ReplyMarkup the inline keyboard of the message is removed.
type EditMessageTextParams struct {
	ChatID      int64                 `json:"chat_id"`
	MessageID   int64                 `json:"message_id"`
	Text        string                `json:"text"`
	ParseMode   string                `json:"parse_mode,omitempty"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// EditMessageReplyMarkupParams: a nil ReplyMarkup removes the inline keyboard.
type EditMessageReplyMarkupParams struct {
	ChatID      int64                 `json:"chat_id"`
	MessageID   int64                 `json:"message_id"`
	ReplyMarkup *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
}

// ReactionType is a reaction on a message; only emoji reactions are used.
type ReactionType struct {
	Type  string `json:"type"`
	Emoji string `json:"emoji"`
}

// SetMessageReactionParams: an empty Reaction removes the bot's reactions.
type SetMessageReactionParams struct {
	ChatID    int64          `json:"chat_id"`
	MessageID int64          `json:"message_id"`
	Reaction  []ReactionType `json:"reaction"`
}

type AnswerCallbackQueryParams struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
	ShowAlert       bool   `json:"show_alert,omitempty"`
}

// SendFileParams describes sendVideo, sendPhoto and sendDocument with a file uploaded from an open file.
// The caller opens (and validates) the file and closes it after the call; the client only reads it with
// ReadAt, so retries re-read it from the start.
type SendFileParams struct {
	ChatID          int64
	MessageThreadID int64
	File            *os.File
	FileName        string
	Caption         string
	ParseMode       string
	ReplyParameters *ReplyParameters
	ReplyMarkup     *InlineKeyboardMarkup
}

type getFileParams struct {
	FileID string `json:"file_id"`
}

type responseParameters struct {
	RetryAfter      int   `json:"retry_after,omitempty"`
	MigrateToChatID int64 `json:"migrate_to_chat_id,omitempty"`
}
