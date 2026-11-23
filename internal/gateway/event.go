// Package gateway is the Telegram gateway: four team bots in one service. It receives updates from the
// team supergroup, filters them (group and user allowlist), routes them to an agent role, downloads media
// through the local Bot API server and passes a normalized Event to a Handler. Sender sends messages on
// behalf of a role.
package gateway

import (
	"io"
	"log/slog"
	"time"
)

// Role is a team bot and the agent behind it.
type Role string

const (
	MarketerRole   Role = "marketer"
	CopywriterRole Role = "copywriter"
	EditorRole     Role = "editor"
	DesignerRole   Role = "designer"
)

// Roles in a fixed order.
var Roles = []Role{MarketerRole, CopywriterRole, EditorRole, DesignerRole}

// dispatcherRole is the bot that receives every group message (privacy mode off); the other bots
// receive only callback queries of their own buttons.
const dispatcherRole = MarketerRole

type EventType string

const (
	MessageEventType  EventType = "MESSAGE"
	CommandEventType  EventType = "COMMAND"
	CallbackEventType EventType = "CALLBACK"
)

// Command is a recognized team command, without the slash.
type Command string

const (
	PlanCommand   Command = "plan"
	IdeaCommand   Command = "idea"
	StatusCommand Command = "status"
	PauseCommand  Command = "pause"
)

// Topic is a named forum topic of the team group.
type Topic string

const (
	PlanningTopic   Topic = "PLANNING"   // «Планёрка»
	SourcesTopic    Topic = "SOURCES"    // «Исходники»
	ProductionTopic Topic = "PRODUCTION" // «Продакшн»
	TechTopic       Topic = "TECH"       // «Техничка»
	OtherTopic      Topic = "OTHER"      // General or a topic that is not configured
)

type MediaType string

const (
	VoiceMediaType     MediaType = "voice"
	AudioMediaType     MediaType = "audio"
	VideoMediaType     MediaType = "video" // also a document with a video/* MIME type
	VideoNoteMediaType MediaType = "video_note"
	PhotoMediaType     MediaType = "photo"
	DocumentMediaType  MediaType = "document" // outgoing only: Sender.SendMedia
)

// Event is a normalized incoming update.
type Event struct {
	Type     EventType
	UpdateID int64
	// ReceivedBy is the bot that got the update: the dispatcher for messages, the button owner for callbacks.
	ReceivedBy Role
	// Target is the role the event is addressed to; the reply goes from this bot.
	Target    Role
	ChatID    int64
	TopicID   int64 // message_thread_id; 0 is General
	Topic     Topic
	MessageID int64
	// ReplyToMessageID is the message this one answers; 0 if it is not a reply.
	ReplyToMessageID int64
	// ReplyToRole is the team bot whose message this one answers; empty if it is not a reply to a team bot.
	ReplyToRole Role
	From        User
	// Text is the message text or media caption; for a command, its arguments.
	Text string
	// Forwarded: the message is forwarded from someone else. Agents treat its text as data, never as
	// an instruction.
	Forwarded bool
	// IsCommand: the message starts with a bot_command entity (/cmd or /cmd@bot). A team command has the
	// type CommandEventType; any other command is a MessageEventType with IsCommand and its whole text.
	IsCommand bool
	Command   Command
	Media     *Media
	Callback  *Callback
	Date      time.Time
}

type User struct {
	ID        int64
	Username  string
	FirstName string
}

// Media is an incoming file. The downloaded file lies in the Bot API files directory under a directory named
// after the bot token, so its path is not exported: read it with Open. The Bot API server may delete old files
// (storage optimizer), so a consumer copies the file to its own storage (S3) right away.
type Media struct {
	Type MediaType
	// FileID is valid only for the bot that received the event (ReceivedBy).
	FileID       string
	FileUniqueID string
	Size         int64
	Duration     time.Duration
	MIMEType     string
	FileName     string
	// TooLarge: the file is above TELEGRAM_MAX_FILE_SIZE and was not downloaded.
	TooLarge bool

	// Location of the downloaded file: the files directory and the path inside it.
	dir     string
	rel     string
	maxSize int64
}

// Downloaded reports whether the file is on disk and can be opened.
func (m *Media) Downloaded() bool {
	return m.rel != ""
}

// WithLocalFile returns a copy of the media that reads the file rel inside dir like a downloaded file (through
// os.Root, at most maxSize bytes): for tests of handlers and for tools that pass a file already on disk.
func (m *Media) WithLocalFile(dir, rel string, maxSize int64) *Media {
	local := *m
	local.dir, local.rel, local.maxSize = dir, rel, maxSize

	return &local
}

// Open opens the downloaded file through os.Root of the files directory: a symlink cannot lead outside it,
// and the file is checked again (regular, within the size limit) after opening. Errors contain no path.
func (m *Media) Open() (io.ReadSeekCloser, error) {
	if !m.Downloaded() {
		return nil, ErrMediaNotDownloaded
	}

	file, err := openInRoot(m.dir, m.rel, m.maxSize)
	if err != nil {
		return nil, err
	}

	return &mediaFile{file: file}, nil
}

type Callback struct {
	ID   string
	Data string
}

// LogValue logs an event without its content (text, file path, callback data).
func (e *Event) LogValue() slog.Value {
	attrs := []slog.Attr{
		slog.String("type", string(e.Type)),
		slog.Int64("update_id", e.UpdateID),
		slog.String("received_by", string(e.ReceivedBy)),
		slog.String("target", string(e.Target)),
		slog.Int64("chat_id", e.ChatID),
		slog.Int64("topic_id", e.TopicID),
		slog.String("topic", string(e.Topic)),
		slog.Int64("message_id", e.MessageID),
		slog.Int64("reply_to_message_id", e.ReplyToMessageID),
		slog.Int64("from_id", e.From.ID),
		slog.Int("text_length", len([]rune(e.Text))),
		slog.Bool("forwarded", e.Forwarded),
	}

	if e.Command != "" {
		attrs = append(attrs, slog.String("command", string(e.Command)))
	}

	if e.Media != nil {
		attrs = append(attrs,
			slog.String("media_type", string(e.Media.Type)),
			slog.Int64("media_size", e.Media.Size),
			slog.Bool("media_downloaded", e.Media.Downloaded()),
			slog.Bool("media_too_large", e.Media.TooLarge),
		)
	}

	return slog.GroupValue(attrs...)
}
