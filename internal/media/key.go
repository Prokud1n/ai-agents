// Package media keeps the files the trainer sends to the team chat (source videos for the editor). A file is
// copied from the Bot API files directory to S3 as soon as it arrives (the Bot API server may delete old files),
// under a key made from the message, so a redelivered message writes the same key. Consumers get the file back
// as a local temporary file (Store.Download), e.g. the editor (T3.4b).
package media

import (
	"errors"
	"fmt"
	"mime"
	"regexp"
	"strconv"
	"strings"
)

// mp4Extension is shared by two MIME types.
const mp4Extension = ".mp4"

const (
	defaultExtension   = ".bin"
	defaultContentType = "application/octet-stream"
	keySeparator       = "/"
)

var (
	// ErrInvalidKey: the key is not a media key of this store.
	ErrInvalidKey = errors.New("invalid media key")
	// ErrInvalidFile: the file cannot get a key (no message, an unsafe file id).
	ErrInvalidFile = errors.New("invalid media file")
	// ErrInvalidConfig: an invalid MEDIA_* setting.
	ErrInvalidConfig = errors.New("invalid media config")
	// ErrFileNotFound: there is no file under the key (never stored, or deleted).
	ErrFileNotFound = errors.New("media file not found")
	// ErrFileTooLarge: the stored file is above the size the caller accepts.
	ErrFileTooLarge = errors.New("media file is too large")
)

var (
	// prefixPattern: path segments of lower-case letters, digits, "-" and "_"; no dots, so no "..".
	prefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*(/[a-z0-9][a-z0-9_-]*)*$`)
	// fileUniqueIDPattern: Telegram's file_unique_id is URL-safe base64; anything else is refused, so the key
	// never gets a path separator, a dot or a control character from outside.
	fileUniqueIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	// keyPattern: <prefix>/<chat id>/<message id>/<file unique id><ext>.
	keyPattern = regexp.MustCompile(
		`^([a-z0-9][a-z0-9_-]*(?:/[a-z0-9][a-z0-9_-]*)*)/(-?[1-9][0-9]{0,18})/([1-9][0-9]{0,18})/` +
			`([A-Za-z0-9_-]{1,64})(\.[a-z0-9]{1,5})$`)
)

// extensions by MIME type: the format is told by the MIME type Telegram reports, the file name is only
// a fallback (see Extension). ffmpeg reads the files by their content, the extension is for people and tools.
var extensions = map[string]string{
	"video/mp4":        mp4Extension,
	"video/x-m4v":      mp4Extension,
	"video/quicktime":  ".mov",
	"video/webm":       ".webm",
	"video/x-matroska": ".mkv",
	"video/x-msvideo":  ".avi",
	"video/3gpp":       ".3gp",
	"video/mpeg":       ".mpeg",
}

// contentTypes are the content types by extension, for a file whose MIME type is unknown; their keys are the
// only extensions a file name may give.
var contentTypes = map[string]string{
	mp4Extension: "video/mp4",
	".mov":       "video/quicktime",
	".webm":      "video/webm",
	".mkv":       "video/x-matroska",
	".avi":       "video/x-msvideo",
	".3gp":       "video/3gpp",
	".mpeg":      "video/mpeg",
}

// File is what the key and the content type of a stored file are made from.
type File struct {
	ChatID       int64
	MessageID    int64
	FileUniqueID string
	// MIMEType and FileName come from Telegram (the sender): they only choose the extension and the content type.
	MIMEType string
	FileName string
}

// Key is the S3 key of the file: <prefix>/<chat id>/<message id>/<file unique id><ext>. The same message always
// gets the same key, so a redelivery overwrites the same object.
func Key(prefix string, f *File) (string, error) {
	if err := validatePrefix(prefix); err != nil {
		return "", err
	}

	switch {
	case f.ChatID == 0 || f.MessageID <= 0:
		return "", fmt.Errorf("message is not set: %w", ErrInvalidFile)
	case !fileUniqueIDPattern.MatchString(f.FileUniqueID):
		return "", fmt.Errorf("unsafe file unique id: %w", ErrInvalidFile)
	}

	return strings.Join([]string{
		prefix, strconv.FormatInt(f.ChatID, 10), strconv.FormatInt(f.MessageID, 10),
		f.FileUniqueID + Extension(f.MIMEType, f.FileName),
	}, keySeparator), nil
}

// Extension is the file extension (with the dot) by the MIME type, then by the file name if it is a known video
// extension, otherwise ".bin". Nothing else of the file name is used: no path, no name.
func Extension(mimeType, fileName string) string {
	if ext, ok := extensions[normalizeMIME(mimeType)]; ok {
		return ext
	}

	name := fileName
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}

	if i := strings.LastIndex(name, "."); i >= 0 {
		if ext := strings.ToLower(name[i:]); contentTypes[ext] != "" {
			return ext
		}
	}

	return defaultExtension
}

// ContentType is the MIME type to store the file with: the reported one if it is a known video type, otherwise
// the type of the extension, otherwise application/octet-stream. An arbitrary reported type (text/html) is never
// stored.
func ContentType(mimeType, ext string) string {
	if mediaType := normalizeMIME(mimeType); mediaType != "" {
		if _, known := extensions[mediaType]; known {
			return mediaType
		}
	}

	if mediaType, ok := contentTypes[ext]; ok {
		return mediaType
	}

	return defaultContentType
}

// normalizeMIME returns the lower-case type/subtype without parameters, or "" if it is not one.
func normalizeMIME(mimeType string) string {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(mimeType))
	if err != nil {
		return ""
	}

	return strings.ToLower(mediaType)
}

// KeyInfo is what a media key tells.
type KeyInfo struct {
	Prefix    string
	ChatID    int64
	MessageID int64
	Extension string
}

// ParseKey reads a media key made by Key; anything else (another layout, a path with "..") is ErrInvalidKey.
func ParseKey(key string) (KeyInfo, error) {
	m := keyPattern.FindStringSubmatch(key)
	if m == nil {
		return KeyInfo{}, ErrInvalidKey
	}

	chatID, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		return KeyInfo{}, fmt.Errorf("chat id: %w", ErrInvalidKey)
	}

	messageID, err := strconv.ParseInt(m[3], 10, 64)
	if err != nil {
		return KeyInfo{}, fmt.Errorf("message id: %w", ErrInvalidKey)
	}

	return KeyInfo{Prefix: m[1], ChatID: chatID, MessageID: messageID, Extension: m[5]}, nil
}

func validatePrefix(prefix string) error {
	if !prefixPattern.MatchString(prefix) {
		return fmt.Errorf("MEDIA_KEY_PREFIX %q must be lower-case path segments of letters, digits, - and _: %w",
			prefix, ErrInvalidConfig)
	}

	return nil
}
