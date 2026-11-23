package teamchat

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/gateway"
	"github.com/Prokud1n/ai-agents/internal/media"
)

const (
	testVideoContent  = "video-bytes"
	testVideoName     = "video.mp4"
	testFileUniqueID  = "AgADuniq-1_x"
	testVideoFileName = "IMG_0001.MOV"
	testMaxFileSize   = 1 << 20
)

var errS3Down = errors.New("s3 is down: secret details")

// videoMedia is a video of the test message; local makes it downloaded from dir.
func videoMedia(mediaType gateway.MediaType, dir string) *gateway.Media {
	m := &gateway.Media{
		Type:         mediaType,
		FileUniqueID: testFileUniqueID,
		Size:         int64(len(testVideoContent)),
		MIMEType:     "video/quicktime",
		FileName:     testVideoFileName,
	}

	if dir == "" {
		return m
	}

	return m.WithLocalFile(dir, testVideoName, testMaxFileSize)
}

func mediaEvent(text string, target gateway.Role, m *gateway.Media) *gateway.Event {
	event := withTarget(messageEvent(text), target)
	event.Topic = gateway.SourcesTopic
	event.Media = m

	return event
}

func inTopic(event *gateway.Event, topic gateway.Topic) *gateway.Event {
	event.Topic = topic

	return event
}

// TestHandlerAnswersStoredVideoOnStop: the copy ends after the gateway's context is cancelled (stop), the
// trainer still gets the answer.
func TestHandlerAnswersStoredVideoOnStop(t *testing.T) {
	t.Parallel()

	dir := testFilesDir(t)
	sender := &ctxSender{}
	h := NewHandler(&fakeService{}, sender, &fakeStore{}, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.NoError(t, h.HandleEvent(ctx, mediaEvent("", gateway.EditorRole, videoMedia(gateway.VideoMediaType, dir))))
	assert.Equal(t, []string{videoStoredText}, sender.sent)
}

// ctxSender fails a send with a cancelled context, like the real Bot API client.
type ctxSender struct {
	fakeSender

	sent []string
}

func (s *ctxSender) SendText(ctx context.Context, role gateway.Role, msg *gateway.OutgoingMessage) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	s.sent = append(s.sent, msg.Text)

	return s.fakeSender.SendText(ctx, role, msg)
}

func tooLarge(m *gateway.Media) *gateway.Media {
	m.TooLarge = true
	m.Size = 3 << 30

	return m
}

func testFilesDir(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, testVideoName), []byte(testVideoContent), 0o600))

	return dir
}

func TestHandlerWantsMedia(t *testing.T) {
	t.Parallel()

	command := mediaEvent("", gateway.EditorRole, videoMedia(gateway.VideoMediaType, ""))
	command.Type = gateway.CommandEventType

	tests := []struct {
		name     string
		input    *gateway.Event
		expected bool
	}{
		{name: "video for the editor", input: mediaEvent("", gateway.EditorRole, videoMedia(gateway.VideoMediaType, "")), expected: true},
		{name: "video above the limit", input: mediaEvent("", gateway.EditorRole, tooLarge(videoMedia(gateway.VideoMediaType, "")))},
		{name: "video for the copywriter", input: mediaEvent("", gateway.CopywriterRole, videoMedia(gateway.VideoMediaType, ""))},
		{name: "voice message", input: withTarget(withMedia(messageEvent(""), gateway.VoiceMediaType), gateway.EditorRole)},
		{name: "video note", input: mediaEvent("", gateway.EditorRole, videoMedia(gateway.VideoNoteMediaType, ""))},
		{name: "photo", input: withTarget(withMedia(messageEvent(""), gateway.PhotoMediaType), gateway.DesignerRole)},
		{name: "audio file", input: withTarget(withMedia(messageEvent(""), gateway.AudioMediaType), gateway.EditorRole)},
		{name: "command with a video", input: command},
		{name: "text", input: messageEvent(ideaText)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := NewHandler(&fakeService{}, &fakeSender{}, &fakeStore{}, slog.New(slog.DiscardHandler))

			assert.Equal(t, tt.expected, h.WantsMedia(tt.input))
		})
	}
}

func TestHandlerMedia(t *testing.T) {
	t.Parallel()

	dir := testFilesDir(t)
	savedFile := media.File{
		ChatID: testChatID, MessageID: testMessageID, FileUniqueID: testFileUniqueID,
		MIMEType: "video/quicktime", FileName: testVideoFileName,
	}

	tests := []struct {
		name          string
		store         *fakeStore
		event         *gateway.Event
		expectedFiles []media.File
		expectedTexts []sentText
	}{
		{
			name:          "video in «Исходники» is stored and answered by the editor",
			event:         mediaEvent("", gateway.EditorRole, videoMedia(gateway.VideoMediaType, dir)),
			expectedFiles: []media.File{savedFile},
			expectedTexts: []sentText{reply(gateway.EditorRole, videoStoredText)},
		},
		{
			name:          "video with a caption: the caption is not kept, the trainer is told",
			event:         mediaEvent("первые 3 секунды убрать", gateway.EditorRole, videoMedia(gateway.VideoMediaType, dir)),
			expectedFiles: []media.File{savedFile},
			expectedTexts: []sentText{reply(gateway.EditorRole, videoStoredText+videoCaptionNote)},
		},
		{
			name:          "storage error: the trainer is asked to send again, without details",
			store:         &fakeStore{err: errS3Down},
			event:         mediaEvent("", gateway.EditorRole, videoMedia(gateway.VideoMediaType, dir)),
			expectedFiles: []media.File{savedFile},
			expectedTexts: []sentText{reply(gateway.EditorRole, fileSaveFailedText)},
		},
		{
			name:          "the gateway could not download the video",
			event:         mediaEvent("", gateway.EditorRole, videoMedia(gateway.VideoMediaType, "")),
			expectedTexts: []sentText{reply(gateway.EditorRole, downloadFailedText)},
		},
		{
			name:          "video above TELEGRAM_MAX_FILE_SIZE",
			event:         mediaEvent("", gateway.EditorRole, tooLarge(videoMedia(gateway.VideoMediaType, ""))),
			expectedTexts: []sentText{reply(gateway.EditorRole, fileTooLargeText(3<<30))},
		},
		{
			name:          "video in «Исходники» addressed to the copywriter: remove the mention",
			event:         mediaEvent("", gateway.CopywriterRole, videoMedia(gateway.VideoMediaType, dir)),
			expectedTexts: []sentText{reply(gateway.CopywriterRole, videoMentionText)},
		},
		{
			name:          "video with a caption in «Продакшн»: send it to «Исходники», the caption is not taken",
			event:         inTopic(mediaEvent("подпись", gateway.CopywriterRole, videoMedia(gateway.VideoMediaType, dir)), gateway.ProductionTopic),
			expectedTexts: []sentText{reply(gateway.CopywriterRole, videoToEditorText+captionNote)},
		},
		{
			name:          "video note is speech: dictate text instead",
			event:         mediaEvent("", gateway.EditorRole, videoMedia(gateway.VideoNoteMediaType, dir)),
			expectedTexts: []sentText{reply(gateway.EditorRole, voiceText)},
		},
		{
			name:          "voice message to the copywriter",
			event:         withTarget(withMedia(messageEvent(""), gateway.VoiceMediaType), gateway.CopywriterRole),
			expectedTexts: []sentText{reply(gateway.CopywriterRole, voiceText)},
		},
		{
			name:          "audio file with a caption",
			event:         withMedia(messageEvent("послушай"), gateway.AudioMediaType),
			expectedTexts: []sentText{reply(gateway.MarketerRole, audioText+captionNote)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := tt.store
			if store == nil {
				store = &fakeStore{}
			}

			service, sender := &fakeService{}, &fakeSender{}
			h := NewHandler(service, sender, store, slog.New(slog.DiscardHandler))

			require.NoError(t, h.HandleEvent(t.Context(), tt.event))
			assert.Equal(t, tt.expectedFiles, store.Files())
			assert.Equal(t, tt.expectedTexts, sender.texts)
			assert.Empty(t, service.created, "no task is created for a file")
		})
	}
}

// TestHandlerVideoRedelivery: the same message handled again writes the same key with the same content; a second
// answer is prevented by the gateway's mark of the event (TestIntegrationVideoIntake).
func TestHandlerVideoRedelivery(t *testing.T) {
	t.Parallel()

	dir := testFilesDir(t)
	store := &fakeStore{}
	h := NewHandler(&fakeService{}, &fakeSender{}, store, slog.New(slog.DiscardHandler))

	for range 2 {
		require.NoError(t, h.HandleEvent(t.Context(),
			mediaEvent("", gateway.EditorRole, videoMedia(gateway.VideoMediaType, dir))))
	}

	files := store.Files()
	require.Len(t, files, 2)

	first, err := media.Key(testKeyPrefix, &files[0])
	require.NoError(t, err)

	second, err := media.Key(testKeyPrefix, &files[1])
	require.NoError(t, err)

	assert.Equal(t, first, second)
	assert.Equal(t, testKeyPrefix+"/"+"-1001000/100/"+testFileUniqueID+".mov", first)
	assert.Equal(t, [][]byte{[]byte(testVideoContent), []byte(testVideoContent)}, store.Saved())
}
