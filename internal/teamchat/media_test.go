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
	"github.com/Prokud1n/ai-agents/internal/tasks"
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

// TestHandlerAnswersStoredVideoOnStop: the copy ends after the gateway's context is cancelled (stop), the task is
// created and the trainer still gets the answer.
func TestHandlerAnswersStoredVideoOnStop(t *testing.T) {
	t.Parallel()

	dir := testFilesDir(t)
	sender := &ctxSender{}
	h := NewHandler(&fakeService{}, nil, sender, &fakeStore{}, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	require.NoError(t, h.HandleEvent(ctx, mediaEvent("", gateway.EditorRole, videoMedia(gateway.VideoMediaType, dir))))
	assert.Equal(t, []string{videoAcceptedHereText}, sender.sent)
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

			h := NewHandler(&fakeService{}, nil, &fakeSender{}, &fakeStore{}, slog.New(slog.DiscardHandler))

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

	editTask := func(caption string) []tasks.CreateTaskData {
		return []tasks.CreateTaskData{{
			Agent: string(gateway.EditorRole), Kind: ReelsEditTaskKind, Input: caption,
			Media: []string{"incoming/-1001000/100/" + testFileUniqueID + ".mov"}, Source: source(), AuthorID: testUserID,
		}}
	}

	tests := []struct {
		name          string
		store         *fakeStore
		service       *fakeService
		production    int64
		event         *gateway.Event
		expectedFiles []media.File
		expectedTasks []tasks.CreateTaskData
		expectedTexts []sentText
	}{
		{
			name:          "video in «Исходники» becomes the editor's task, the draft comes here",
			event:         mediaEvent("", gateway.EditorRole, videoMedia(gateway.VideoMediaType, dir)),
			expectedFiles: []media.File{savedFile},
			expectedTasks: editTask(""),
			expectedTexts: []sentText{reply(gateway.EditorRole, videoAcceptedHereText)},
		},
		{
			name:          "with «Продакшн» configured the trainer is told the draft goes there",
			production:    testTopicID + 1,
			event:         mediaEvent("", gateway.EditorRole, videoMedia(gateway.VideoMediaType, dir)),
			expectedFiles: []media.File{savedFile},
			expectedTasks: editTask(""),
			expectedTexts: []sentText{reply(gateway.EditorRole, videoAcceptedText)},
		},
		{
			name:          "video with a caption: the caption is kept as the task input",
			event:         mediaEvent(" первые 3 секунды убрать ", gateway.EditorRole, videoMedia(gateway.VideoMediaType, dir)),
			expectedFiles: []media.File{savedFile},
			expectedTasks: editTask("первые 3 секунды убрать"),
			expectedTexts: []sentText{reply(gateway.EditorRole, videoAcceptedHereText+videoCaptionNote)},
		},
		{
			name:          "redelivered video: the task exists, no second answer",
			service:       &fakeService{exists: true},
			event:         mediaEvent("", gateway.EditorRole, videoMedia(gateway.VideoMediaType, dir)),
			expectedFiles: []media.File{savedFile},
			expectedTasks: editTask(""),
		},
		{
			name:          "the task could not be created: the class of the error, no details",
			service:       &fakeService{createErr: errDatabaseDown},
			event:         mediaEvent("", gateway.EditorRole, videoMedia(gateway.VideoMediaType, dir)),
			expectedFiles: []media.File{savedFile},
			expectedTasks: editTask(""),
			expectedTexts: []sentText{reply(gateway.EditorRole, internalErr)},
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
			name:  "video above TELEGRAM_MAX_FILE_SIZE",
			event: mediaEvent("", gateway.EditorRole, tooLarge(videoMedia(gateway.VideoMediaType, ""))),
			expectedTexts: []sentText{reply(gateway.EditorRole, "Файл больше лимита 2 ГБ: такие я скачать не могу. "+
				"Сожмите видео или разрежьте его на части.")},
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

			service := tt.service
			if service == nil {
				service = &fakeService{}
			}

			sender := &fakeSender{}
			h := NewHandler(service, nil, sender, store, slog.New(slog.DiscardHandler))
			h.productionTopicID, h.maxFileSize = tt.production, 2<<30

			require.NoError(t, h.HandleEvent(t.Context(), tt.event))
			assert.Equal(t, tt.expectedFiles, store.Files())
			assert.Equal(t, tt.expectedTexts, sender.texts)
			assert.Equal(t, tt.expectedTasks, service.created, "only a stored video becomes a task")
		})
	}
}

// TestHandlerVideoRedelivery: the same message handled again writes the same key with the same content; a second
// answer is prevented by the gateway's mark of the event (TestIntegrationVideoIntake).
func TestHandlerVideoRedelivery(t *testing.T) {
	t.Parallel()

	dir := testFilesDir(t)
	store := &fakeStore{}
	h := NewHandler(&fakeService{}, nil, &fakeSender{}, store, slog.New(slog.DiscardHandler))

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

func TestFileTooLargeText(t *testing.T) {
	t.Parallel()

	const rest = ": такие я скачать не могу. Сожмите видео или разрежьте его на части."

	tests := []struct {
		name     string
		input    int64
		expected string
	}{
		{name: "the default 2 GiB", input: 2 << 30, expected: "Файл больше лимита 2 ГБ" + rest},
		{name: "a decimal with a comma", input: 3 << 29, expected: "Файл больше лимита 1,5 ГБ" + rest},
		{name: "below a gigabyte", input: 500 << 20, expected: "Файл больше лимита 500 МБ" + rest},
		{name: "unknown limit", input: 0, expected: "Файл слишком большой" + rest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, fileTooLargeText(tt.input))
		})
	}
}
