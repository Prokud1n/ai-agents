package media

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	mimeMP4       = "video/mp4"
	mimeMOV       = "video/quicktime"
	extMOV        = ".mov"
	testPrefix    = "incoming"
	testChatID    = int64(-1001234567890)
	testMessageID = int64(42)
	testUniqueID  = "AgADBAADq-_x"
)

func testFile(mimeType, fileName string) *File {
	return &File{
		ChatID: testChatID, MessageID: testMessageID, FileUniqueID: testUniqueID, MIMEType: mimeType, FileName: fileName,
	}
}

func TestKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		prefix      string
		input       *File
		expected    string
		expectedErr error
	}{
		{
			name: "video by its MIME type", prefix: testPrefix, input: testFile(mimeMP4, "IMG_1.MOV"),
			expected: "incoming/-1001234567890/42/AgADBAADq-_x.mp4",
		},
		{
			name: "nested prefix", prefix: "dev/incoming", input: testFile(mimeMOV, ""),
			expected: "dev/incoming/-1001234567890/42/AgADBAADq-_x.mov",
		},
		{
			name: "unknown MIME type: the extension of the file name", prefix: testPrefix, input: testFile("", "clip.MKV"),
			expected: "incoming/-1001234567890/42/AgADBAADq-_x.mkv",
		},
		{
			name: "a path in the file name gives nothing", prefix: testPrefix,
			input:    testFile("application/x-unknown", "../../etc/passwd"),
			expected: "incoming/-1001234567890/42/AgADBAADq-_x.bin",
		},
		{
			name: "file name with a directory and an unknown extension", prefix: testPrefix,
			input:    testFile("", `C:\videos\evil.mp4\..\run.sh`),
			expected: "incoming/-1001234567890/42/AgADBAADq-_x.bin",
		},
		{
			name: "unsafe file unique id", prefix: testPrefix,
			input:       &File{ChatID: testChatID, MessageID: testMessageID, FileUniqueID: "../x", MIMEType: mimeMP4},
			expectedErr: ErrInvalidFile,
		},
		{
			name: "file unique id with a dot", prefix: testPrefix,
			input:       &File{ChatID: testChatID, MessageID: testMessageID, FileUniqueID: "a.mp4", MIMEType: mimeMP4},
			expectedErr: ErrInvalidFile,
		},
		{
			name: "too long file unique id", prefix: testPrefix,
			input:       &File{ChatID: testChatID, MessageID: testMessageID, FileUniqueID: strings.Repeat("a", 65)},
			expectedErr: ErrInvalidFile,
		},
		{
			name: "empty file unique id", prefix: testPrefix,
			input:       &File{ChatID: testChatID, MessageID: testMessageID},
			expectedErr: ErrInvalidFile,
		},
		{
			name: "no message", prefix: testPrefix,
			input:       &File{ChatID: testChatID, FileUniqueID: testUniqueID},
			expectedErr: ErrInvalidFile,
		},
		{name: "upper-case prefix", prefix: "Incoming", input: testFile(mimeMP4, ""), expectedErr: ErrInvalidConfig},
		{name: "prefix with dots", prefix: "../incoming", input: testFile(mimeMP4, ""), expectedErr: ErrInvalidConfig},
		{name: "prefix with an empty segment", prefix: "a//b", input: testFile(mimeMP4, ""), expectedErr: ErrInvalidConfig},
		{name: "empty prefix", prefix: "", input: testFile(mimeMP4, ""), expectedErr: ErrInvalidConfig},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			key, err := Key(tt.prefix, tt.input)
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, key)
		})
	}
}

func TestContentType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mimeType string
		ext      string
		expected string
	}{
		{name: "known type with parameters", mimeType: "video/MP4; codecs=avc1", ext: ".mp4", expected: mimeMP4},
		{name: "upper case", mimeType: "VIDEO/QUICKTIME", ext: extMOV, expected: mimeMOV},
		{name: "unknown type: by the extension", mimeType: "", ext: extMOV, expected: mimeMOV},
		{name: "an arbitrary type is never stored", mimeType: "text/html", ext: ".bin", expected: defaultContentType},
		{name: "broken type", mimeType: "video/", ext: ".bin", expected: defaultContentType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, ContentType(tt.mimeType, tt.ext))
		})
	}
}

func TestParseKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		expected    KeyInfo
		expectedErr error
	}{
		{
			name:  "key made by Key",
			input: "incoming/-1001234567890/42/AgADBAADq-_x.mp4",
			expected: KeyInfo{
				Prefix: testPrefix, ChatID: testChatID, MessageID: testMessageID, Name: testUniqueID, Extension: ".mp4",
			},
		},
		{
			name:     "nested prefix",
			input:    "dev/incoming/5/1/u.mov",
			expected: KeyInfo{Prefix: "dev/incoming", ChatID: 5, MessageID: 1, Name: "u", Extension: extMOV},
		},
		{name: "dots in the path", input: "incoming/../-100/42/u.mp4", expectedErr: ErrInvalidKey},
		{name: "no extension", input: "incoming/-100/42/u", expectedErr: ErrInvalidKey},
		{name: "zero message", input: "incoming/-100/0/u.mp4", expectedErr: ErrInvalidKey},
		{name: "no prefix", input: "/-100/42/u.mp4", expectedErr: ErrInvalidKey},
		{name: "absolute path", input: "/etc/passwd", expectedErr: ErrInvalidKey},
		{name: "chat id above int64", input: "incoming/9999999999999999999/1/u.mp4", expectedErr: ErrInvalidKey},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			info, err := ParseKey(tt.input)
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, info)
		})
	}
}

func TestResultKey(t *testing.T) {
	t.Parallel()

	source := KeyInfo{Prefix: testPrefix, ChatID: testChatID, MessageID: testMessageID, Name: testUniqueID, Extension: ".mov"}

	tests := []struct {
		name        string
		prefix      string
		file        string
		ext         string
		expected    string
		expectedErr error
	}{
		{
			name: "reel next to the source", prefix: testResultPrefix, file: testUniqueID + "-v1-reel", ext: mp4Extension,
			expected: "edits/-1001234567890/42/AgADBAADq-_x-v1-reel.mp4",
		},
		{name: "json", prefix: testResultPrefix, file: "u-plan", ext: ".json", expected: "edits/-1001234567890/42/u-plan.json"},
		{name: "path in the name", prefix: testResultPrefix, file: "a/b", ext: mp4Extension, expectedErr: ErrInvalidFile},
		{name: "long extension", prefix: testResultPrefix, file: "u", ext: ".mpeg4x", expectedErr: ErrInvalidFile},
		{name: "invalid prefix", prefix: "Edits", file: "u", ext: mp4Extension, expectedErr: ErrInvalidConfig},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			key, err := ResultKey(tt.prefix, source, tt.file, tt.ext)
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, key)
		})
	}
}
