package gateway

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/telegram"
)

// Bot API limits.
const (
	maxCallbackDataSize = 64   // bytes
	maxMessageLength    = 4096 // UTF-16 code units of a message text
	maxCaptionLength    = 1024 // UTF-16 code units of a media caption
)

var (
	// ErrDisabled: no bot tokens are set, the gateway is off.
	ErrDisabled = errors.New("telegram gateway is disabled: set TELEGRAM_*_TOKEN")
	// ErrUnknownRole: no bot for the role.
	ErrUnknownRole = errors.New("unknown bot role")
	// ErrValidation: the outgoing message is malformed.
	ErrValidation = errors.New("invalid outgoing telegram message")
)

// Button is an inline keyboard button; Data comes back as Event.Callback.Data (1–64 bytes).
type Button struct {
	Text string
	Data string
}

// OutgoingMessage is a text message to the team group. A text above 4096 characters is sent in several
// messages; the buttons go with the last one.
type OutgoingMessage struct {
	TopicID int64 // 0 is General
	// ReplyTo is the message to answer (the first part answers it); sent even if that message was deleted.
	ReplyTo int64
	Text    string
	// Buttons are rows of inline buttons.
	Buttons [][]Button
}

// OutgoingMedia is a local file sent to the team group: Type is VideoMediaType, PhotoMediaType or
// DocumentMediaType. Path must be a regular file in TELEGRAM_OUTBOX_DIR. A caption above 1024
// characters is sent as text messages answering the file, with the buttons on the last one.
type OutgoingMedia struct {
	Type    MediaType
	Path    string
	TopicID int64
	ReplyTo int64
	Caption string
	Buttons [][]Button
}

// Sender sends messages to the team group on behalf of a role. Messages go only to the configured group,
// files only from the allowed directories. Texts are plain (no parse mode), so agent output needs no escaping.
type Sender struct {
	bots        *bots
	groupID     int64
	uploadRoots []string
	maxFileSize int64
}

func NewSender(b *bots, cfg *config.Telegram) *Sender {
	return &Sender{
		bots:        b,
		groupID:     cfg.GroupID,
		uploadRoots: []string{cfg.OutboxDir},
		maxFileSize: cfg.MaxFileSize,
	}
}

// SendText sends a message, with inline buttons if any, and returns the ID of the last message sent (the one
// with the buttons).
func (s *Sender) SendText(ctx context.Context, role Role, msg *OutgoingMessage) (int64, error) {
	if strings.TrimSpace(msg.Text) == "" {
		return 0, fmt.Errorf("empty text: %w", ErrValidation)
	}

	client, err := s.client(role)
	if err != nil {
		return 0, err
	}

	markup, err := keyboard(msg.Buttons)
	if err != nil {
		return 0, err
	}

	return s.sendParts(ctx, client, role, &textParts{
		topicID: msg.TopicID, replyTo: msg.ReplyTo, text: msg.Text, markup: markup,
	})
}

type textParts struct {
	topicID int64
	replyTo int64
	text    string
	markup  *telegram.InlineKeyboardMarkup
}

// sendParts sends a text split by the message limit: the first part answers replyTo, the last has the buttons.
func (s *Sender) sendParts(ctx context.Context, client *telegram.Client, role Role, p *textParts) (int64, error) {
	parts := splitText(p.text, maxMessageLength)

	var lastID int64

	for i, part := range parts {
		params := &telegram.SendMessageParams{ChatID: s.groupID, MessageThreadID: p.topicID, Text: part}
		if i == 0 {
			params.ReplyParameters = replyParameters(p.replyTo)
		}

		if i == len(parts)-1 {
			params.ReplyMarkup = p.markup
		}

		sent, err := client.SendMessage(ctx, params)
		if err != nil {
			return 0, fmt.Errorf("failed to send message part %d of %d as %s: %w", i+1, len(parts), role, err)
		}

		lastID = sent.MessageID
	}

	return lastID, nil
}

// SendMedia uploads a file (up to 2 GB with the local Bot API server) and returns the ID of the message with
// the buttons: the file itself, or the last caption message for a long caption.
func (s *Sender) SendMedia(ctx context.Context, role Role, media *OutgoingMedia) (int64, error) {
	client, err := s.client(role)
	if err != nil {
		return 0, err
	}

	if media.Type != VideoMediaType && media.Type != PhotoMediaType && media.Type != DocumentMediaType {
		return 0, fmt.Errorf("media type %q cannot be sent: %w", media.Type, ErrValidation)
	}

	markup, err := keyboard(media.Buttons)
	if err != nil {
		return 0, err
	}

	dir, rel, err := locate(s.uploadRoots, media.Path)
	if err != nil {
		return 0, fmt.Errorf("failed to send %s: %w", media.Type, err)
	}

	file, err := openInRoot(dir, rel, s.maxFileSize)
	if err != nil {
		return 0, fmt.Errorf("failed to send %s: %w", media.Type, err)
	}
	defer file.Close()

	longCaption := utf16Len(media.Caption) > maxCaptionLength

	params := &telegram.SendFileParams{
		ChatID:          s.groupID,
		MessageThreadID: media.TopicID,
		File:            file,
		FileName:        filepath.Base(rel),
		ReplyParameters: replyParameters(media.ReplyTo),
	}

	if !longCaption {
		params.Caption = media.Caption
		params.ReplyMarkup = markup
	}

	sent, err := sendFile(ctx, client, media.Type, params)
	if err != nil {
		return 0, fmt.Errorf("failed to send %s as %s: %w", media.Type, role, err)
	}

	if !longCaption {
		return sent.MessageID, nil
	}

	return s.sendParts(ctx, client, role, &textParts{
		topicID: media.TopicID, replyTo: sent.MessageID, text: media.Caption, markup: markup,
	})
}

func sendFile(
	ctx context.Context,
	client *telegram.Client,
	mediaType MediaType,
	params *telegram.SendFileParams,
) (*telegram.Message, error) {
	switch mediaType {
	case VideoMediaType:
		return client.SendVideo(ctx, params)
	case PhotoMediaType:
		return client.SendPhoto(ctx, params)
	case DocumentMediaType:
		return client.SendDocument(ctx, params)
	default:
		return nil, fmt.Errorf("media type %q cannot be sent: %w", mediaType, ErrValidation)
	}
}

// EditText replaces the text (up to 4096 characters) of the role's message; the keyboard is replaced by
// buttons (nil removes it).
func (s *Sender) EditText(ctx context.Context, role Role, messageID int64, text string, buttons [][]Button) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("empty text: %w", ErrValidation)
	}

	if utf16Len(text) > maxMessageLength {
		return fmt.Errorf("text above %d characters cannot be edited in: %w", maxMessageLength, ErrValidation)
	}

	return s.edit(role, buttons, func(client *telegram.Client, markup *telegram.InlineKeyboardMarkup) error {
		_, err := client.EditMessageText(ctx, &telegram.EditMessageTextParams{
			ChatID: s.groupID, MessageID: messageID, Text: text, ReplyMarkup: markup,
		})

		return err
	})
}

// EditButtons replaces the inline keyboard of the role's message; nil removes it.
func (s *Sender) EditButtons(ctx context.Context, role Role, messageID int64, buttons [][]Button) error {
	return s.edit(role, buttons, func(client *telegram.Client, markup *telegram.InlineKeyboardMarkup) error {
		_, err := client.EditMessageReplyMarkup(ctx, &telegram.EditMessageReplyMarkupParams{
			ChatID: s.groupID, MessageID: messageID, ReplyMarkup: markup,
		})

		return err
	})
}

func (s *Sender) edit(
	role Role,
	buttons [][]Button,
	call func(client *telegram.Client, markup *telegram.InlineKeyboardMarkup) error,
) error {
	client, err := s.client(role)
	if err != nil {
		return err
	}

	markup, err := keyboard(buttons)
	if err != nil {
		return err
	}

	if err := call(client, markup); err != nil {
		return fmt.Errorf("failed to edit message as %s: %w", role, err)
	}

	return nil
}

// AnswerCallback stops the button's loading indicator; text (optional) is shown as a short notification.
// The role must be the one that received the callback (Event.ReceivedBy). Telegram takes one answer per
// callback; if the Handler fails, the gateway answers without text itself.
func (s *Sender) AnswerCallback(ctx context.Context, role Role, callbackID, text string) error {
	client, err := s.client(role)
	if err != nil {
		return err
	}

	if err := client.AnswerCallbackQuery(ctx, &telegram.AnswerCallbackQueryParams{
		CallbackQueryID: callbackID,
		Text:            text,
	}); err != nil {
		return fmt.Errorf("failed to answer callback as %s: %w", role, err)
	}

	return nil
}

func (s *Sender) client(role Role) (*telegram.Client, error) {
	if !s.bots.enabled() {
		return nil, ErrDisabled
	}

	b, ok := s.bots.byRole[role]
	if !ok {
		return nil, fmt.Errorf("%q: %w", role, ErrUnknownRole)
	}

	return b.client, nil
}

func keyboard(buttons [][]Button) (*telegram.InlineKeyboardMarkup, error) {
	if len(buttons) == 0 {
		return nil, nil
	}

	rows := make([][]telegram.InlineKeyboardButton, 0, len(buttons))

	for _, row := range buttons {
		converted := make([]telegram.InlineKeyboardButton, 0, len(row))

		for _, b := range row {
			if b.Text == "" || b.Data == "" || len(b.Data) > maxCallbackDataSize {
				return nil, fmt.Errorf("button needs a text and 1-%d bytes of data: %w", maxCallbackDataSize, ErrValidation)
			}

			converted = append(converted, telegram.InlineKeyboardButton{Text: b.Text, CallbackData: b.Data})
		}

		rows = append(rows, converted)
	}

	return &telegram.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

func replyParameters(messageID int64) *telegram.ReplyParameters {
	if messageID == 0 {
		return nil
	}

	return &telegram.ReplyParameters{MessageID: messageID, AllowSendingWithoutReply: true}
}

// splitText splits a text into parts of at most limit UTF-16 code units (how Telegram counts), preferably
// after a line break, then after a space in the second half of a part.
func splitText(text string, limit int) []string {
	runes := []rune(text)
	parts := make([]string, 0, 1)

	for {
		end, units := 0, 0
		for end < len(runes) && units+runeUnits(runes[end]) <= limit {
			units += runeUnits(runes[end])
			end++
		}

		if end == len(runes) {
			return append(parts, string(runes))
		}

		cut := breakPoint(runes[:end])
		parts = append(parts, string(runes[:cut]))
		runes = runes[cut:]
	}
}

// breakPoint returns where to cut a too long part: after the last line break or space in its second half,
// or at its end.
func breakPoint(part []rune) int {
	for _, sep := range []rune{'\n', ' '} {
		for i := len(part); i > len(part)/2; i-- {
			if part[i-1] == sep {
				return i
			}
		}
	}

	return len(part)
}

func runeUnits(r rune) int {
	return max(utf16.RuneLen(r), 1)
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += runeUnits(r)
	}

	return n
}
